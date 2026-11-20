package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
)

// larkScope is one scope of a Lark app, with the tokens that may use it.
type larkScope struct {
	Scope      string   `json:"scope"`
	TokenTypes []string `json:"token_types"`
}

// What a Connection's account can really do (issue #567). The owner's answer
// for an action (allow, ask, never) says whether agents may; whether the
// account can is the service's to say, and it is read there: the grants a
// Google account was given at its sign-in (Hermes keeps them the same way,
// google_chat/oauth.py), the scopes a Lark app has enabled (lark-cli's
// `auth scopes`, cmd/auth/auth.go getAppInfo) or a person's sign-in was
// granted (lark-cli keeps them with the token, token_store.go), and a
// Superset account's roles with their permissions (Superset's
// GET /api/v1/me/roles/, views/users/api.py get_my_roles). This file reads
// them; the service proxy, which has the actions, says from them what each
// action is (serviceproxy.Assess). They are read when a Connection is made or
// signed in again and when the owner checks it again, as Onyx checks a
// credential on demand and lark-cli on `auth check`; nothing polls.

// Holdings is what a Connection's account was found to hold at its service.
type Holdings struct {
	Integration string
	Label       string
	// Account is the account's own name at the service.
	Account string
	// Grants are a Google account's services, as its sign-in gave them.
	Grants []Grant
	// Scopes are what a Lark app has enabled, or what a person's sign-in through an app was granted when User.
	Scopes []string
	User   bool
	// Roles are a Superset account's roles, each with its permissions as (permission, view menu) pairs.
	Roles map[string][][2]string
	// Unknown says why nothing could be read: every action is then unknown.
	Unknown string
}

// Checker reads what a Connection's account can do and keeps it on the
// Connection: the control plane's own wiring of Holdings and the service
// proxy's catalog.
type Checker func(ctx context.Context, integration, label string) error

// WithChecker sets what reads a Connection's access after it is made or
// signed in again, and on Check.
func (s *Service) WithChecker(c Checker) *Service {
	s.checker = c

	return s
}

// Check reads again what a Connection's account can do and returns the
// Connection with it: the owner's "Check again".
func (s *Service) Check(ctx context.Context, integration, label string) (Connection, error) {
	d, err := s.definition(integration)
	if err != nil {
		return Connection{}, err
	}

	if s.checker == nil {
		return Connection{}, fmt.Errorf("%w: this control plane reads no account's access", orchestration.ErrUnavailable)
	}

	if _, err := s.store.ConnectionOf(ctx, d.ID, label); err != nil {
		return Connection{}, err
	}

	// A service account is checked as when it was connected, step by step (issue #976).
	if d.IsServiceAccount() {
		if err := s.checkServiceAccountAgain(ctx, d, label); err != nil {
			return Connection{}, err
		}
	}

	if err := s.checker(ctx, d.ID, label); err != nil {
		return Connection{}, err
	}

	conn, err := s.store.ConnectionOf(ctx, d.ID, label)
	if err != nil {
		return Connection{}, err
	}

	return s.view(ctx, d, conn), nil
}

// checked reads the access of a Connection just saved and returns it. A read
// that fails leaves the Connection as it was saved: it is made, and the owner
// can check it again.
func (s *Service) checked(ctx context.Context, d Definition, saved orchestration.Connection) Connection {
	if s.checker == nil || s.checker(ctx, d.ID, saved.Label) != nil {
		return s.view(ctx, d, saved)
	}

	if again, err := s.store.ConnectionOf(ctx, d.ID, saved.Label); err == nil {
		saved = again
	}

	return s.view(ctx, d, saved)
}

// Holdings reads what a Connection's account holds at its service. What the
// service would not say is Unknown, with why; an error is a Connection that
// does not exist or a store that cannot be read.
func (s *Service) Holdings(ctx context.Context, integration, label string) (Holdings, error) {
	d, err := s.definition(integration)
	if err != nil {
		return Holdings{}, err
	}

	conn, err := s.store.ConnectionOf(ctx, d.ID, label)
	if err != nil {
		return Holdings{}, err
	}

	h := Holdings{Integration: d.ID, Label: label, Account: conn.Account}

	switch {
	case d.IsDevice():
		// A linked account does all its owner does; there is nothing to read (issue #615).
		h.Unknown = "a linked account acts as its owner"
	case d.IsRole():
		// No reference reads what an AWS role may do without simulating its policies (issue #567, not in it).
		h.Unknown = "what an AWS role may do is not read"
	case d.IsServiceAccount():
		// What a service account may read is its IAM roles at Google, which the connect check tries (issue #976).
		h.Unknown = "what a service account may read is its IAM roles at Google"
	case d.IsAccount():
		s.supersetRoles(ctx, d, conn, &h)
	case d.IsApp() && conn.User():
		s.userScopes(ctx, conn, &h)
	case d.IsApp():
		s.appScopes(ctx, d, conn, &h)
	default:
		// What is stored was checked when it was written; a service since removed from the catalog is left out.
		h.Grants, _ = d.ParseGrants(conn.Services)
	}

	return h, nil
}

// appScopes reads the scopes a Lark app has enabled, as lark-cli's `auth
// scopes` does (getAppInfo): the app's own information, with its token. Of
// the scopes, those a tenant token may use, the app's own; lark-cli keeps
// the user ones for its sign-in. A scope list without token types is taken
// whole, as lark-cli takes it.
func (s *Service) appScopes(ctx context.Context, d Definition, conn orchestration.Connection, h *Holdings) {
	token, err := s.appToken(ctx, d, conn)
	if err != nil {
		h.Unknown = "the app's token could not be had: " + err.Error()

		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		d.AppScopesURL+url.PathEscape(conn.AppID)+"?lang=en_us", nil)
	if err != nil {
		h.Unknown = "the app's scopes could not be asked for"

		return
	}

	req.Header.Set("Authorization", "Bearer "+token.AccessToken)

	status, body, err := s.do(req)
	if err != nil {
		h.Unknown = d.Name + " could not be reached"

		return
	}

	var answer struct {
		Code int `json:"code"`
		Data struct {
			App struct {
				Scopes []larkScope `json:"scopes"`
			} `json:"app"`
		} `json:"data"`
	}

	if json.Unmarshal(body, &answer) != nil || answer.Code != 0 || status != http.StatusOK {
		h.Unknown = fmt.Sprintf("%s did not say which scopes the app has: status %d, code %d", d.Name, status,
			answer.Code)

		return
	}

	typed := slices.ContainsFunc(answer.Data.App.Scopes, func(sc larkScope) bool { return len(sc.TokenTypes) > 0 })

	h.Scopes = []string{}

	for _, sc := range answer.Data.App.Scopes {
		if sc.Scope != "" && (!typed || slices.Contains(sc.TokenTypes, "tenant")) {
			h.Scopes = append(h.Scopes, sc.Scope)
		}
	}
}

// userScopes reads the scopes a person's sign-in was granted, kept with its
// tokens. lark-cli skips its check when a token names none
// (checkServiceScopes), and so is a sign-in that kept none unknown.
func (s *Service) userScopes(ctx context.Context, conn orchestration.Connection, h *Holdings) {
	h.User = true

	opened, err := s.vault.Open(ctx, conn.Secret)
	if err != nil {
		h.Unknown = "the sign-in could not be read: " + vaultError(err).Error()

		return
	}

	scope := strings.Fields(opened.Payload[vault.KeyScope])
	if len(scope) == 0 {
		h.Unknown = "the sign-in named no scopes; sign in again to read them"

		return
	}

	h.Scopes = scope
}

// supersetRoles reads a Superset account's roles with their permissions.
// The answer is bootstrap_user_data with its permissions
// (views/utils.py): {"result": {"roles": {"Gamma": [["can_read",
// "Dashboard"], ...]}}}. A refusal leaves every action unknown.
func (s *Service) supersetRoles(ctx context.Context, d Definition, conn orchestration.Connection, h *Holdings) {
	answer, err := s.Call(ctx, d.ID, conn.Label, AccountRequest{Method: http.MethodGet, Path: d.RolesPath})

	switch {
	case errors.Is(err, ErrSignInRequired):
		h.Unknown = d.Name + " did not take the account's password"

		return
	case err != nil:
		h.Unknown = d.Name + " could not be reached"

		return
	case answer.Status != http.StatusOK:
		h.Unknown = fmt.Sprintf("%s answered %d to %s", d.Name, answer.Status, d.RolesPath)

		return
	}

	roles, ok := SupersetRolesOf(answer.Body)
	if !ok {
		h.Unknown = fmt.Sprintf("%s's answer to %s names no roles", d.Name, d.RolesPath)

		return
	}

	h.Roles = roles
}

// SupersetRolesOf reads the roles of Superset's answer to GET
// /api/v1/me/roles/, each with its (permission, view menu) pairs; false when
// the answer names none.
func SupersetRolesOf(answer []byte) (map[string][][2]string, bool) {
	var said struct {
		Result struct {
			Roles map[string][][]string `json:"roles"`
		} `json:"result"`
	}

	if json.Unmarshal(answer, &said) != nil || said.Result.Roles == nil {
		return nil, false
	}

	out := make(map[string][][2]string, len(said.Result.Roles))

	for role, perms := range said.Result.Roles {
		pairs := make([][2]string, 0, len(perms))

		for _, p := range perms {
			if len(p) == 2 {
				pairs = append(pairs, [2]string{p[0], p[1]})
			}
		}

		out[role] = pairs
	}

	return out, true
}
