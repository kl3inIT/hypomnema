package integration

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
)

// A user Connection (issue #468): the owner signs in to an app Integration as
// himself, through one of its apps, so the main Bot can read what he can read.
// lark-cli is the one reference that acts in Lark as a person, and it signs a
// user in with an app's id and secret by device code
// (internal/auth/device_flow.go, cmd/auth/login.go): the device authorization
// with the app as Basic and offline_access always asked, then the token
// endpoint polled every five seconds, five more on slow_down. The Connection
// names the app Connection it signs in with and keeps the person's tokens as
// its vault secret, as a Google Connection keeps its account's
// (integration.go): a refresh under the vault's lease, which stores the new
// refresh token before the access token is used, since Lark's is single use
// (uat_client.go, token_store.go). Lark's codes that end a sign-in for good
// mark it as needing a sign-in (output/lark_errors.go). Disconnecting revokes
// it (revoke.go, cmd/auth/logout.go). A token issued without a DPoP proof is a
// plain Bearer that another process may send (tried on 2026-11-15).

const (
	// devicePoll is the pause between two polls when the service names none, and slowDown what slow_down adds,
	// up to maxPoll (lark-cli's pollDeviceToken).
	devicePoll = 5 * time.Second
	slowDown   = 5 * time.Second
	maxPoll    = 60 * time.Second
	// deviceGrant is the grant type of the device code (RFC 8628).
	deviceGrant = "urn:ietf:params:oauth:grant-type:device_code"
	// offlineAccess makes the service give a refresh token (lark-cli adds it to every device authorization).
	offlineAccess = "offline_access"
)

// larkDeadCodes are Lark's codes for a refresh token it will never take again: invalid, expired, revoked and
// already used (lark-cli output/lark_errors.go). invalid_grant says the same in OAuth's words.
var larkDeadCodes = []int{20026, 20037, 20064, 20073}

// deviceSignIn is what a person's sign-in by device code keeps until it ends.
type deviceSignIn struct {
	// app is the app Connection it signs in with.
	app        orchestration.Connection
	deviceCode string
	every      time.Duration
	expires    time.Duration
}

func errUserLabel(d Definition, label string) error {
	return fmt.Errorf("%w: %s:%s is your own sign-in, not an app; sign it in again with "+
		"`hypomnema integrations connect %s --label %s --app APP --as-user`, or name the app another label",
		ErrInvalid, d.ID, label, d.ID, label)
}

// BeginAsUser starts the owner's own sign-in to an app Integration through
// the app of the Connection appLabel, as a user Connection under label. The
// owner opens the link on any device and enters the code; Complete then waits
// for it. A label that is his sign-in already is signed in again.
func (s *Service) BeginAsUser(ctx context.Context, integration, label, appLabel string) (SignIn, error) {
	d, err := s.definition(integration)
	if err != nil {
		return SignIn{}, err
	}

	if !d.SignsInUsers() {
		return SignIn{}, fmt.Errorf("%w: %s has no sign-in as yourself", ErrInvalid, d.ID)
	}

	if !orchestration.ValidConnectionLabel(label) {
		return SignIn{}, fmt.Errorf("%w: a label is lower-case letters, digits and dashes, such as me", ErrInvalid)
	}

	app, err := s.store.ConnectionOf(ctx, d.ID, appLabel)
	if errors.Is(err, orchestration.ErrNotFound) || (err == nil && app.User()) {
		return SignIn{}, fmt.Errorf("%w: %s has no app %q to sign in with; connect one first with "+
			"`hypomnema integrations connect %s --label LABEL --app-file FILE`", ErrInvalid, d.ID, appLabel, d.ID)
	}

	if err != nil {
		return SignIn{}, err
	}

	switch existing, err := s.store.ConnectionOf(ctx, d.ID, label); {
	case errors.Is(err, orchestration.ErrNotFound):
	case err != nil:
		return SignIn{}, err
	case !existing.User():
		return SignIn{}, fmt.Errorf("%w: %s:%s is an app; give your own sign-in another label, such as me",
			ErrInvalid, d.ID, label)
	}

	secret, err := s.appOf(ctx, d, app)
	if err != nil {
		return SignIn{}, err
	}

	code, err := s.deviceAuthorization(ctx, d, secret)
	if err != nil {
		return SignIn{}, fmt.Errorf("%w: %w", ErrSignInFailed, err)
	}

	pending := &signIn{
		def: d, label: label, started: s.clock.Now(),
		device: &deviceSignIn{
			app: app, deviceCode: code.DeviceCode, every: code.every(), expires: min(code.lifetime(), signInTimeout),
		},
	}

	id := s.newID("signin")

	s.mu.Lock()
	defer s.mu.Unlock()

	for old, p := range s.signIns {
		if pending.started.Sub(p.started) > signInTimeout {
			delete(s.signIns, old)
		}
	}

	s.signIns[id] = pending

	return SignIn{ID: id, URL: code.link(), UserCode: code.UserCode, ExpiresIn: pending.device.expires}, nil
}

// deviceCode is the service's answer to a device authorization.
type deviceCode struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int64  `json:"expires_in"`
	Interval                int64  `json:"interval"`
}

// link is the page the owner opens: the one with the code in it when the service gives it.
func (c deviceCode) link() string {
	if c.VerificationURIComplete != "" {
		return c.VerificationURIComplete
	}

	return c.VerificationURI
}

func (c deviceCode) every() time.Duration {
	if c.Interval < 1 {
		return devicePoll
	}

	return secondsOf(c.Interval)
}

// lifetime is how long the code may be entered: lark-cli's 240 s when the service does not say.
func (c deviceCode) lifetime() time.Duration {
	if c.ExpiresIn < 1 {
		return 240 * time.Second
	}

	return secondsOf(c.ExpiresIn)
}

// deviceAuthorization asks the service for a device code for the app. Its errors hold the status and the
// service's error word only.
func (s *Service) deviceAuthorization(ctx context.Context, d Definition, app App) (deviceCode, error) {
	form := url.Values{"client_id": {app.ID}, "scope": {offlineAccess}}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.DeviceAuthURL, strings.NewReader(form.Encode()))
	if err != nil {
		return deviceCode{}, fmt.Errorf("asking %s for a sign-in code: %w", d.Name, err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(app.ID+":"+app.Secret)))

	status, body, err := s.do(req)
	if err != nil {
		return deviceCode{}, fmt.Errorf("asking %s for a sign-in code: %w", d.Name, err)
	}

	var code deviceCode
	if status != http.StatusOK || json.Unmarshal(body, &code) != nil || code.DeviceCode == "" || code.UserCode == "" ||
		code.link() == "" {
		return deviceCode{}, fmt.Errorf("%s gave no sign-in code: status %d %s", d.Name, status, errorCode(body))
	}

	return code, nil
}

// completeDevice waits until the owner entered the code, then keeps the sign-in. A sign-in is completed once; one
// whose wait was given up before it ended can be waited for again.
func (s *Service) completeDevice(ctx context.Context, id string, pending *signIn, pasted string) (Connection, error) {
	if strings.TrimSpace(pasted) != "" {
		return Connection{}, fmt.Errorf("%w: this sign-in waits for the code to be entered on %s's page, not for an "+
			"address", ErrInvalid, pending.def.Name)
	}

	s.mu.Lock()
	_, waiting := s.signIns[id]
	delete(s.signIns, id)
	s.mu.Unlock()

	if !waiting {
		return Connection{}, fmt.Errorf("%w: sign-in %q is already being completed", ErrNotFound, id)
	}

	d, dev := pending.def, pending.device

	app, err := s.appOf(ctx, d, dev.app)
	if err != nil {
		return Connection{}, err
	}

	tokens, err := s.pollDevice(ctx, d, app, pending)
	if err != nil {
		if ctx.Err() != nil && s.clock.Now().Sub(pending.started) < dev.expires {
			s.mu.Lock()
			s.signIns[id] = pending
			s.mu.Unlock()
		}

		return Connection{}, err
	}

	if tokens.TokenType != "" && !strings.EqualFold(tokens.TokenType, "Bearer") {
		return Connection{}, fmt.Errorf("%w: %s gave a %s token, which only the device that asked can send",
			ErrSignInFailed, d.Name, tokens.TokenType)
	}

	// Without a refresh token the sign-in would end in two hours (lark-cli internal/auth/no_refresh_token.go).
	if tokens.RefreshToken == "" {
		return Connection{}, fmt.Errorf("%w: %s gave no refresh token, so the sign-in would end in two hours. In the "+
			"developer console, turn on \"Refresh user_access_token\" in the security settings of the app %s "+
			"(https://open.larksuite.com/app/%s/safe), publish a new version of the app, then sign in again",
			ErrSignInFailed, d.Name, dev.app.Account, url.PathEscape(app.ID))
	}

	account, err := s.person(ctx, d, tokens.AccessToken)
	if err != nil {
		return Connection{}, fmt.Errorf("%w: %w", ErrSignInFailed, err)
	}

	return s.saveUser(ctx, d, pending.label, account, dev.app, vault.Refreshed{
		Payload: vault.Payload{
			vault.KeyAccessToken: tokens.AccessToken, vault.KeyRefreshToken: tokens.RefreshToken,
		},
		AccessTokenExpiresAt: s.expiry(tokens.tokenAnswer),
	})
}

// userTokens is an answer of the token endpoint, with its OAuth error and Lark's code.
type userTokens struct {
	tokenAnswer

	TokenType string `json:"token_type"`
	Error     string `json:"error"`
	Code      int    `json:"code"`
}

// pollDevice asks the token endpoint until the code was entered, refused or ended (lark-cli pollDeviceToken).
func (s *Service) pollDevice(ctx context.Context, d Definition, app App, pending *signIn) (userTokens, error) {
	dev := pending.device
	every := dev.every
	form := url.Values{
		"grant_type": {deviceGrant}, "device_code": {dev.deviceCode},
		"client_id": {app.ID}, "client_secret": {app.Secret},
	}

	for {
		select {
		case <-ctx.Done():
			return userTokens{}, fmt.Errorf("waiting for the code to be entered: %w", ctx.Err())
		case <-s.after(every):
		}

		if s.clock.Now().Sub(pending.started) >= dev.expires {
			return userTokens{}, fmt.Errorf("%w: the code was not entered in time; start again", ErrSignInFailed)
		}

		status, body, err := s.post(ctx, d.TokenURL, form)
		if err != nil {
			// A network failure may pass, as lark-cli waits a second more and asks again.
			every = min(every+time.Second, maxPoll)

			continue
		}

		var answer userTokens
		if json.Unmarshal(body, &answer) != nil {
			return userTokens{}, fmt.Errorf("%w: %s answered status %d, not JSON", ErrSignInFailed, d.Name, status)
		}

		if answer.Error == "" && answer.AccessToken != "" {
			return answer, nil
		}

		switch errorCode(body) {
		case "authorization_pending":
		case "slow_down":
			every = min(every+slowDown, maxPoll)
		case "access_denied":
			return userTokens{}, fmt.Errorf("%w: the sign-in was refused on %s's page", ErrSignInFailed, d.Name)
		case "expired_token", "invalid_grant":
			return userTokens{}, fmt.Errorf("%w: the code expired; start again", ErrSignInFailed)
		default:
			return userTokens{}, fmt.Errorf("%w: %s refused the sign-in: status %d %s, code %d", ErrSignInFailed,
				d.Name, status, errorCode(body), answer.Code)
		}
	}
}

// person reads whose account a user's token is: their name at the service.
func (s *Service) person(ctx context.Context, d Definition, accessToken string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.UserInfoURL, nil)
	if err != nil {
		return "", fmt.Errorf("reading who signed in: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)

	status, body, err := s.do(req)
	if err != nil {
		return "", fmt.Errorf("reading who signed in: %w", err)
	}

	var answer struct {
		Code int `json:"code"`
		Data struct {
			Name   string `json:"name"`
			EnName string `json:"en_name"`
			Email  string `json:"email"`
		} `json:"data"`
	}

	if status != http.StatusOK || json.Unmarshal(body, &answer) != nil || answer.Code != 0 {
		return "", fmt.Errorf("reading who signed in: status %d, code %d", status, answer.Code)
	}

	for _, name := range []string{answer.Data.Name, answer.Data.EnName, answer.Data.Email} {
		if name != "" {
			return name, nil
		}
	}

	return "", errors.New("reading who signed in: the answer has no name")
}

// saveUser keeps a user Connection's tokens as its vault secret and records it, naming the app it signed in
// with and that app's id.
func (s *Service) saveUser(ctx context.Context, d Definition, label, account string, app orchestration.Connection,
	login vault.Refreshed,
) (Connection, error) {
	name := connectionSecretName(d.ID, label)

	_, err := s.vault.Get(ctx, name)

	switch {
	case err == nil:
		_, err = s.vault.RotateTokenSet(ctx, name, login)
	case errors.Is(err, vault.ErrNotFound):
		_, err = s.vault.Create(ctx, vault.NewSecret{
			Name: name, Kind: vault.KindOAuthTokenSet, Payload: login.Payload,
			Metadata: map[string]string{
				"integration": d.ID, "connection": label, "account": account, "app": app.Label,
				"what": "a person's own sign-in through an app",
			},
			AccessTokenExpiresAt: login.AccessTokenExpiresAt,
		})
	}

	if err != nil {
		return Connection{}, vaultError(err)
	}

	saved, err := s.store.SaveServiceConnection(ctx, orchestration.Connection{
		Integration: d.ID, Label: label, Account: account, Secret: name, AppID: app.AppID, AppLabel: app.Label,
	})
	if err != nil {
		return Connection{}, err
	}

	return s.view(ctx, d, saved), nil
}

// appStillSignsIn says whether a user Connection's app Connection is still the app it signed in with: a refresh
// token is of the app that issued it.
func (s *Service) appStillSignsIn(ctx context.Context, d Definition, user orchestration.Connection) bool {
	app, err := s.store.ConnectionOf(ctx, d.ID, user.AppLabel)

	return err == nil && !app.User() && app.AppID == user.AppID
}

// userToken is a user Connection's access token, refreshed first when it has less than the Integration's lead
// left.
func (s *Service) userToken(ctx context.Context, d Definition, conn orchestration.Connection) (Token, error) {
	opened, err := s.vault.Open(ctx, conn.Secret)
	if err != nil {
		return Token{}, vaultError(err)
	}

	if fresh(opened, s.clock.Now(), d.lead()) {
		return userTokenOf(opened, conn), nil
	}

	if opened.LoginRequired {
		return Token{}, fmt.Errorf("%w: %s:%s", ErrSignInRequired, d.ID, conn.Label)
	}

	opened, err = s.vault.Refresh(ctx, conn.Secret, opened.Version, func(ctx context.Context, current vault.Payload,
	) (vault.Refreshed, error) {
		return s.refreshUser(ctx, d, conn, current)
	})
	if errors.Is(err, vault.ErrLoginRequired) {
		return Token{}, fmt.Errorf("%w: %s:%s", ErrSignInRequired, d.ID, conn.Label)
	}

	if err != nil {
		return Token{}, fmt.Errorf("refreshing the token of %s:%s: %w", d.ID, conn.Label, err)
	}

	return userTokenOf(opened, conn), nil
}

// userTokenOf is a user's token with the id of the app it signed in with, which Lark's tool needs beside it.
func userTokenOf(o vault.Opened, conn orchestration.Connection) Token {
	t := tokenOf(o)
	t.ClientID = conn.AppID

	return t
}

// refreshUser is the vault.RefreshFunc of a user Connection: lark-cli's refresh, a JSON body to the token
// endpoint with the app's id and secret (uat_client.go). The answer's refresh token replaces the spent one.
func (s *Service) refreshUser(ctx context.Context, d Definition, conn orchestration.Connection, current vault.Payload,
) (vault.Refreshed, error) {
	if !s.appStillSignsIn(ctx, d, conn) {
		return vault.Refreshed{}, fmt.Errorf("%w: the app of %s:%s is no longer the one it signed in with",
			vault.ErrLoginRejected, d.ID, conn.Label)
	}

	appConn, err := s.store.ConnectionOf(ctx, d.ID, conn.AppLabel)
	if err != nil {
		return vault.Refreshed{}, err
	}

	app, err := s.appOf(ctx, d, appConn)
	if err != nil {
		return vault.Refreshed{}, err
	}

	body, err := json.Marshal(map[string]string{
		"grant_type": "refresh_token", "refresh_token": current[vault.KeyRefreshToken],
		"client_id": app.ID, "client_secret": app.Secret,
	})
	if err != nil {
		return vault.Refreshed{}, fmt.Errorf("refreshing the token: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.TokenURL, bytes.NewReader(body))
	if err != nil {
		return vault.Refreshed{}, fmt.Errorf("refreshing the token: %w", err)
	}

	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	status, raw, err := s.do(req)
	if err != nil {
		return vault.Refreshed{}, fmt.Errorf("refreshing the token: %w", err)
	}

	var answer userTokens
	_ = json.Unmarshal(raw, &answer) // An answer that is not JSON is the failure below.

	if slices.Contains(larkDeadCodes, answer.Code) || errorCode(raw) == "invalid_grant" {
		return vault.Refreshed{}, fmt.Errorf("%w: refreshing the token: status %d, code %d", vault.ErrLoginRejected,
			status, answer.Code)
	}

	if status != http.StatusOK || answer.AccessToken == "" || answer.Error != "" {
		return vault.Refreshed{}, fmt.Errorf("refreshing the token: status %d %s, code %d", status, errorCode(raw),
			answer.Code)
	}

	kept := answer.RefreshToken
	if kept == "" {
		kept = current[vault.KeyRefreshToken]
	}

	return vault.Refreshed{
		Payload:              vault.Payload{vault.KeyAccessToken: answer.AccessToken, vault.KeyRefreshToken: kept},
		AccessTokenExpiresAt: s.expiry(answer.tokenAnswer),
	}, nil
}

// forgetUser revokes a user Connection's tokens at the service, then forgets it and its secret (lark-cli's
// logout). A service that cannot be asked does not keep the Connection here; the answer says so, and the owner
// can remove the app's access in his own account.
func (s *Service) forgetUser(ctx context.Context, d Definition, conn orchestration.Connection) (bool, error) {
	revoked := false

	if appConn, err := s.store.ConnectionOf(ctx, d.ID, conn.AppLabel); err == nil && !appConn.User() {
		app, appErr := s.appOf(ctx, d, appConn)
		opened, openErr := s.vault.Open(ctx, conn.Secret)

		if appErr == nil && openErr == nil {
			revoked = s.revokeUser(ctx, d, app, opened.Payload[vault.KeyRefreshToken], "refresh_token") == nil
			// The access token ends in two hours anyway; it is revoked too, and its answer changes nothing.
			_ = s.revokeUser(ctx, d, app, opened.Payload[vault.KeyAccessToken], "access_token")
		}
	}

	if _, err := s.store.RemoveConnection(ctx, d.ID, conn.Label); err != nil {
		return false, err
	}

	if err := s.vault.Delete(ctx, conn.Secret); err != nil && !errors.Is(err, vault.ErrNotFound) {
		return revoked, vaultError(err)
	}

	return revoked, nil
}

// revokeUser asks the service to end one token (lark-cli internal/auth/revoke.go): the app's id and secret, the
// token and what it is. A refusal answers with a status or a code.
func (s *Service) revokeUser(ctx context.Context, d Definition, app App, token, hint string) error {
	if token == "" {
		return errors.New("nothing to revoke")
	}

	status, body, err := s.post(ctx, d.RevokeURL, url.Values{
		"client_id": {app.ID}, "client_secret": {app.Secret}, "token": {token}, "token_type_hint": {hint},
	})
	if err != nil {
		return err
	}

	var answer struct {
		Code int `json:"code"`
	}

	_ = json.Unmarshal(body, &answer) // An empty or other body says nothing more than the status.

	if status != http.StatusOK || answer.Code != 0 || errorCode(body) != "" {
		return fmt.Errorf("revoking: status %d, code %d", status, answer.Code)
	}

	return nil
}
