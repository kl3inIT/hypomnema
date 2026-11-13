// Package integration signs the owner in to the services Hypomnema works
// with and keeps a fresh access token for each Connection (issue #103).
//
// An Integration is a service; a Connection is one account of it the owner
// authorized, under a label. The control plane runs the sign-in and every
// refresh, as Omnigent, Omnara, Coder, Companion and Onyx do: no agent and no
// node ever holds a refresh token or a client's secret.
//
// A git host is an Integration too, but its credential is a token the owner
// types in, so it lives with the git hosts (the control plane's own wiring).
// This package is for the Integrations whose credential the control plane
// mints: Google signs an account in with OAuth; Lark is an app, whose id and
// secret give a token with no sign-in (issue #135); Superset is an account at
// an address of its own, whose user name and password give one (issue #203).
package integration

import (
	"fmt"
	"slices"
	"strings"
)

// Kind is how an Integration's credential is obtained (issue #103, credential
// kinds).
type Kind string

const (
	// KindOAuth: an account signs in on the service's consent page, and the
	// control plane keeps its refresh token.
	KindOAuth Kind = "oauth"
	// KindApp: the client is an app of the service; its id and secret give an
	// access token that acts as the app, with no sign-in.
	KindApp Kind = "app"
	// KindAccount: a Connection is an address of the service, a user name and
	// a password; the control plane signs in there and keeps the tokens. The
	// address belongs to the Connection, because the service is self-hosted.
	KindAccount Kind = "account"
	// KindRole: a Connection is a role of the owner's cloud account, assumed
	// with a key the owner gave once; the control plane keeps the role's
	// session (issue #331).
	KindRole Kind = "role"
)

// Definition is what an Integration declares. It is code, not something the
// owner writes, as Onyx's providers and Companion's definitions are.
type Definition struct {
	// ID names the Integration in commands and addresses: google.
	ID   string
	Name string
	// Kind is how its credential is obtained; empty is KindOAuth.
	Kind Kind
	// AppTokenURL gives an app's access token for its id and secret, and
	// AppInfoURL says which app the token is; both are for KindApp.
	AppTokenURL string
	AppInfoURL  string
	// LoginPath signs an account in, RefreshPath gives a new access token for
	// a refresh token, CSRFPath gives the token a request that is not a GET
	// carries, and VersionPath says what the service runs; all are paths on a
	// Connection's own address, for KindAccount.
	LoginPath   string
	RefreshPath string
	CSRFPath    string
	VersionPath string
	// AuthURL is the consent page; TokenURL exchanges a code or a refresh
	// token; RevokeURL ends a token; UserInfoURL says whose account signed in.
	AuthURL     string
	TokenURL    string
	RevokeURL   string
	UserInfoURL string
	// Identity are the scopes asked of every Connection, to read whose
	// account it is.
	Identity []string
	// Services are the parts of the Integration a Connection can be granted.
	Services []ServiceDef
}

// ServiceDef is one service of an Integration with its OAuth scopes.
type ServiceDef struct {
	// Name is how a grant names it: gmail.
	Name string
	// Read are the scopes of a read-only grant; Write of one that may also
	// change things.
	Read  []string
	Write []string
}

// Grant is a service a Connection is asked for or was given. It is written
// "gmail" for read and write and "gmail:read" for read only.
type Grant struct {
	Service string
	// ReadOnly says the Connection may not change anything in the service.
	ReadOnly bool
}

// String is the form a grant is typed and stored in.
func (g Grant) String() string {
	if g.ReadOnly {
		return g.Service + ":read"
	}

	return g.Service
}

// ParseGrants reads grants as the owner types them, checks each against the
// Integration's services, and returns them in the Integration's order with
// one grant a service.
func (d Definition) ParseGrants(given []string) ([]Grant, error) {
	asked := map[string]Grant{}

	for _, raw := range given {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}

		name, mode, _ := strings.Cut(raw, ":")
		if mode != "" && mode != "read" {
			return nil, fmt.Errorf("%w: %q: a service is named alone, or with :read for read only", ErrInvalid, raw)
		}

		if !slices.ContainsFunc(d.Services, func(s ServiceDef) bool { return s.Name == name }) {
			return nil, fmt.Errorf("%w: %s has no service %q; it has %s", ErrInvalid, d.ID, name,
				strings.Join(d.serviceNames(), ", "))
		}

		if _, twice := asked[name]; twice {
			return nil, fmt.Errorf("%w: service %q is named twice", ErrInvalid, name)
		}

		asked[name] = Grant{Service: name, ReadOnly: mode == "read"}
	}

	if len(asked) == 0 {
		return nil, fmt.Errorf("%w: name the services to connect: %s", ErrInvalid, strings.Join(d.serviceNames(), ", "))
	}

	out := make([]Grant, 0, len(asked))

	for _, s := range d.Services {
		if g, ok := asked[s.Name]; ok {
			out = append(out, g)
		}
	}

	return out, nil
}

func (d Definition) serviceNames() []string {
	out := make([]string, 0, len(d.Services))
	for _, s := range d.Services {
		out = append(out, s.Name)
	}

	return out
}

// scopes are the OAuth scopes a sign-in for these grants asks for.
func (d Definition) scopes(grants []Grant) []string {
	out := slices.Clone(d.Identity)

	for _, s := range d.Services {
		for _, g := range grants {
			if g.Service != s.Name {
				continue
			}

			if g.ReadOnly {
				out = append(out, s.Read...)
			} else {
				out = append(out, s.Write...)
			}
		}
	}

	return out
}

// granted reads which of the asked grants a sign-in gave. The owner can
// untick a box on the consent page, so the answer's scopes decide: a grant to
// write that got only the scopes to read is kept as read only, and one that
// got neither is dropped. Hermes checks the granted scopes the same way and
// keeps a partial sign-in (scripts/setup.py, exchange_auth_code).
func (d Definition) granted(asked []Grant, scopes []string) []Grant {
	has := func(need []string) bool {
		for _, s := range need {
			if !slices.Contains(scopes, s) {
				return false
			}
		}

		return true
	}

	var out []Grant

	for _, s := range d.Services {
		for _, g := range asked {
			switch {
			case g.Service != s.Name:
			case !g.ReadOnly && has(s.Write):
				out = append(out, g)
			case has(s.Read):
				out = append(out, Grant{Service: s.Name, ReadOnly: true})
			}
		}
	}

	return out
}

// IsApp says whether the Integration's credential is an app's.
func (d Definition) IsApp() bool { return d.Kind == KindApp }

// IsAccount says whether a Connection of the Integration is an address, a
// user name and a password.
func (d Definition) IsAccount() bool { return d.Kind == KindAccount }

// IsRole says whether a Connection of the Integration is a role assumed
// with a key.
func (d Definition) IsRole() bool { return d.Kind == KindRole }

// CredentialKind is the Integration's Kind, with the default spelled out.
func (d Definition) CredentialKind() Kind {
	if d.Kind == "" {
		return KindOAuth
	}

	return d.Kind
}

// GoogleID is the id of the Google Integration.
const GoogleID = "google"

// Google is Google Workspace for one account: Gmail, Calendar, Drive, Docs
// and Sheets, as one Integration with its services chosen at sign-in (gog).
//
// The scopes to write are the ones Hermes' google-workspace skill asks for
// (scripts/setup.py, SCOPES); the scopes to read are the read-only set of the
// Google Workspace CLI (crates/google-workspace-cli/src/auth_commands.rs,
// READONLY_SCOPES). The scope for the account's email is the one the
// Antigravity login in this repository already uses.
func Google() Definition {
	const scope = "https://www.googleapis.com/auth/"

	//nolint:gosec // Addresses and scope names of Google's, public; no credential is here.
	return Definition{
		ID:          GoogleID,
		Name:        "Google",
		AuthURL:     "https://accounts.google.com/o/oauth2/v2/auth",
		TokenURL:    "https://oauth2.googleapis.com/token",
		RevokeURL:   "https://oauth2.googleapis.com/revoke",
		UserInfoURL: "https://www.googleapis.com/oauth2/v2/userinfo?alt=json",
		Identity:    []string{scope + "userinfo.email"},
		Services: []ServiceDef{
			{
				Name: "gmail", Read: []string{scope + "gmail.readonly"},
				Write: []string{scope + "gmail.modify", scope + "gmail.send"},
			},
			{Name: "calendar", Read: []string{scope + "calendar.readonly"}, Write: []string{scope + "calendar"}},
			{Name: "drive", Read: []string{scope + "drive.readonly"}, Write: []string{scope + "drive"}},
			{Name: "docs", Read: []string{scope + "documents.readonly"}, Write: []string{scope + "documents"}},
			{Name: "sheets", Read: []string{scope + "spreadsheets.readonly"}, Write: []string{scope + "spreadsheets"}},
		},
	}
}

// LarkID is the id of the Lark Integration.
const LarkID = "lark"

// Lark is Lark (larksuite.com) as a custom app of the owner's organization:
// the app's id and secret give a tenant access token, good for at most two
// hours, that acts as the app (issue #135). What the app may read and change
// is what its scopes in the Lark Developer Console allow, so a Connection has
// no services to choose here.
//
// The token endpoint is the one Lark documents for a custom app and lark-cli
// names (internal/core/types.go); the bot's own information is what says
// which app a token is.
func Lark() Definition {
	const host = "https://open.larksuite.com"

	return Definition{
		ID:          LarkID,
		Name:        "Lark",
		Kind:        KindApp,
		AppTokenURL: host + "/open-apis/auth/v3/tenant_access_token/internal",
		AppInfoURL:  host + "/open-apis/bot/v3/info",
	}
}

// SupersetID is the id of the Superset Integration.
const SupersetID = "superset"

// Superset is Apache Superset as an account of one deployment (issue #203):
// its address, a user name and a password, as both Superset MCP servers take
// them (Winding2020/superset-mcp, src/client/base-client.ts; aptro/superset-mcp,
// main.py). The sign-in is Superset's own for a local account, which answers
// with an access and a refresh token; what the account may read and change
// is what its roles at Superset allow, so a Connection has no services to
// choose here. Tried against Superset 6.1.0 on 2026-11-12.
func Superset() Definition {
	return Definition{
		ID:          SupersetID,
		Name:        "Superset",
		Kind:        KindAccount,
		LoginPath:   "/api/v1/security/login",
		RefreshPath: "/api/v1/security/refresh",
		CSRFPath:    "/api/v1/security/csrf_token/",
		VersionPath: "/version",
	}
}

// AWSID is the id of the AWS Integration.
const AWSID = "aws"

// AWS is the owner's AWS accounts, a role a Connection (issue #331): the
// owner gives an IAM user's access key once, which may only assume roles, and
// each role has the rights its work needs, as the AWS CLI has one profile
// with the key and one profile a role (role_arn, source_profile). What a role
// may do is what its policies at AWS allow, so a Connection has no services to
// choose here.
func AWS() Definition {
	return Definition{ID: AWSID, Name: "AWS", Kind: KindRole}
}
