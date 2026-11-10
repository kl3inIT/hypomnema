package subscription

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

// The Codex subscription: a ChatGPT account, logged in as the Codex CLI does.
//
// Read on 2026-11-10:
//   - the Codex CLI, commit f5ac7a7: codex-rs/login/src/device_code_auth.rs
//     (the device code flow) and auth/manager.rs (which refresh failures are
//     for good);
//   - CLIProxyAPI, commit c5251b5: sdk/auth/codex_device.go and
//     internal/auth/codex/openai_auth.go, jwt_parser.go, pkce.go (the same
//     flow in Go, the authorize URL, the claims of the id token),
//     internal/misc/oauth.go (reading a pasted callback address).

const (
	codexRuntime = "codex"
	// codexIssuer is OpenAI's authorization server.
	codexIssuer = "https://auth.openai.com"
	// codexClientID is the Codex CLI's public client id.
	codexClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	// codexLoopbackRedirect is the only redirect the browser login accepts.
	// Nothing listens there: the owner copies the address the browser ends on.
	codexLoopbackRedirect = "http://localhost:1455/auth/callback"
	codexScope            = "openid profile email offline_access"
	// codexDeviceTimeout is how long a device code lasts, as the CLI says.
	codexDeviceTimeout = 15 * time.Minute
	// codexPasteTimeout is how long a pending browser login is kept, as
	// CLIProxyAPI's oauth_sessions.go.
	codexPasteTimeout = 30 * time.Minute
	codexDefaultPoll  = 5 * time.Second
	// codexRefreshLead follows 9router-go's background refresh, not
	// CLIProxyAPI's 96 hours: the refresh token is spent by each use.
	codexRefreshLead = 30 * time.Minute
	codexHTTPTimeout = 45 * time.Second
	// maxTokenResponseBytes bounds an answer of the authorization server.
	maxTokenResponseBytes = 1 << 20
	// codexAuthClaim is the id token's claim holding the ChatGPT account.
	codexAuthClaim = "https://api.openai.com/auth"
)

// codexDeadGrants are the error codes that end a login for good: the Codex
// CLI's three and invalid_grant, and CLIProxyAPI's two more.
var codexDeadGrants = []string{
	"refresh_token_expired", "refresh_token_reused", "refresh_token_invalidated",
	"invalid_grant", "token_revoked", "invalid_client",
}

// Codex is the Vendor of ChatGPT subscriptions used through Codex.
type Codex struct {
	issuer string
	client *http.Client
	clock  platform.Clock
	// after is time.After outside tests: the pause between two polls.
	after func(time.Duration) <-chan time.Time
}

// NewCodex returns the Vendor for Codex. issuer is empty outside tests.
func NewCodex(issuer string, clock platform.Clock) *Codex {
	if issuer == "" {
		issuer = codexIssuer
	}

	return &Codex{
		issuer: strings.TrimRight(issuer, "/"), client: &http.Client{Timeout: codexHTTPTimeout}, clock: clock,
		after: time.After,
	}
}

// Runtime implements Vendor.
func (c *Codex) Runtime() string { return codexRuntime }

// RefreshLead implements Vendor.
func (c *Codex) RefreshLead() time.Duration { return codexRefreshLead }

// RefreshSpacing implements Vendor: 9router-go's pause between two accounts
// (internal/proxy/oauth/background.go).
func (c *Codex) RefreshSpacing() time.Duration { return 1500 * time.Millisecond }

// Begin implements Vendor: a device code, or the browser login whose address
// the owner pastes back when the server offers no device code.
func (c *Codex) Begin(ctx context.Context) (*Attempt, error) {
	var code struct {
		DeviceAuthID string          `json:"device_auth_id"`
		UserCode     string          `json:"user_code"`
		UserCodeAlt  string          `json:"usercode"`
		Interval     json.RawMessage `json:"interval"`
	}

	status, body, err := c.post(ctx, "/api/accounts/deviceauth/usercode", "application/json",
		mustJSON(map[string]string{"client_id": codexClientID}))

	switch {
	case err != nil:
		return nil, err
	case status == http.StatusNotFound:
		return c.beginPaste()
	case status/100 != 2:
		return nil, statusError("asking for a device code", status, body)
	}

	if err := json.Unmarshal(body, &code); err != nil {
		return nil, fmt.Errorf("reading the device code: %w", err)
	}

	if code.UserCode == "" {
		code.UserCode = code.UserCodeAlt
	}

	if code.DeviceAuthID == "" || code.UserCode == "" {
		return nil, errors.New("the device code answer lacks its code")
	}

	every := pollInterval(code.Interval)

	return &Attempt{
		URL: c.issuer + "/codex/device", UserCode: code.UserCode, ExpiresIn: codexDeviceTimeout,
		finish: func(ctx context.Context, _ string) (Login, error) {
			return c.finishDevice(ctx, code.DeviceAuthID, code.UserCode, every)
		},
	}, nil
}

// finishDevice waits until the owner has typed the code, then exchanges what
// the server gives for tokens.
func (c *Codex) finishDevice(ctx context.Context, deviceAuthID, userCode string, every time.Duration) (Login, error) {
	ctx, cancel := context.WithTimeout(ctx, codexDeviceTimeout)
	defer cancel()

	request := mustJSON(map[string]string{"device_auth_id": deviceAuthID, "user_code": userCode})

	for {
		status, body, err := c.post(ctx, "/api/accounts/deviceauth/token", "application/json", request)

		switch {
		case err != nil:
			return Login{}, err
		case status/100 == 2:
			var granted struct {
				AuthorizationCode string `json:"authorization_code"`
				CodeVerifier      string `json:"code_verifier"`
			}

			if err := json.Unmarshal(body, &granted); err != nil || granted.AuthorizationCode == "" ||
				granted.CodeVerifier == "" {
				return Login{}, errors.New("the device login answer lacks its authorization code")
			}

			return c.exchange(ctx, granted.AuthorizationCode, granted.CodeVerifier, c.issuer+"/deviceauth/callback")
		case status != http.StatusForbidden && status != http.StatusNotFound:
			// 403 and 404 say the owner has not finished yet.
			return Login{}, statusError("waiting for the device login", status, body)
		}

		select {
		case <-ctx.Done():
			return Login{}, fmt.Errorf("the device code was not used in time: %w", ctx.Err())
		case <-c.after(every):
		}
	}
}

// beginPaste starts the browser login. The control plane keeps the PKCE
// verifier and the state; the owner brings back the address with the code.
func (c *Codex) beginPaste() (*Attempt, error) {
	verifier := base64.RawURLEncoding.EncodeToString(randomBytes(96))
	challenge := sha256.Sum256([]byte(verifier))
	state := hex.EncodeToString(randomBytes(16))
	started := c.clock.Now()

	query := url.Values{
		"client_id":                  {codexClientID},
		"response_type":              {"code"},
		"redirect_uri":               {codexLoopbackRedirect},
		"scope":                      {codexScope},
		"state":                      {state},
		"code_challenge":             {base64.RawURLEncoding.EncodeToString(challenge[:])},
		"code_challenge_method":      {"S256"},
		"prompt":                     {"login"},
		"id_token_add_organizations": {"true"},
		"codex_cli_simplified_flow":  {"true"},
	}

	return &Attempt{
		URL: c.issuer + "/oauth/authorize?" + query.Encode(), Paste: true, ExpiresIn: codexPasteTimeout,
		finish: func(ctx context.Context, pasted string) (Login, error) {
			if c.clock.Now().Sub(started) > codexPasteTimeout {
				return Login{}, errors.New("the login was started too long ago; start it again")
			}

			code, err := CallbackCode(pasted, state)
			if err != nil {
				return Login{}, err
			}

			return c.exchange(ctx, code, verifier, codexLoopbackRedirect)
		},
	}, nil
}

// CallbackCode reads the authorization code from what the owner pasted: the
// address the browser ended on, or only its query. The state must be the one
// this login was started with.
func CallbackCode(pasted, state string) (string, error) {
	pasted = strings.TrimSpace(pasted)

	if !strings.Contains(pasted, "://") {
		pasted = "http://localhost/?" + strings.TrimPrefix(pasted, "?")
	}

	u, err := url.Parse(pasted)
	if err != nil {
		return "", errors.New("what was pasted is not an address")
	}

	q := u.Query()

	switch {
	case q.Get("error") != "":
		return "", fmt.Errorf("the vendor refused the login: %s", clip(q.Get("error")+" "+q.Get("error_description")))
	case q.Get("code") == "":
		return "", errors.New("the pasted address has no code; copy the whole address the browser ended on")
	case q.Get("state") != state:
		return "", errors.New("the pasted address belongs to another login; start again and use the new link")
	}

	return q.Get("code"), nil
}

// tokenAnswer is what the token endpoint gives for a code or a refresh token.
type tokenAnswer struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

func (c *Codex) exchange(ctx context.Context, code, verifier, redirect string) (Login, error) {
	status, body, err := c.post(ctx, "/oauth/token", "application/x-www-form-urlencoded", []byte(url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {codexClientID},
		"code":          {code},
		"redirect_uri":  {redirect},
		"code_verifier": {verifier},
	}.Encode()))
	if err != nil {
		return Login{}, err
	}

	if status != http.StatusOK {
		return Login{}, statusError("exchanging the code for tokens", status, body)
	}

	var answer tokenAnswer
	if err := json.Unmarshal(body, &answer); err != nil || answer.AccessToken == "" || answer.RefreshToken == "" {
		return Login{}, errors.New("the token answer lacks an access or a refresh token")
	}

	tokens := vault.Payload{vault.KeyAccessToken: answer.AccessToken, vault.KeyRefreshToken: answer.RefreshToken}
	if answer.IDToken != "" {
		tokens[vault.KeyIDToken] = answer.IDToken
	}

	who, err := c.Identify(tokens)
	if err != nil {
		return Login{}, err
	}

	return Login{
		Identity: who,
		Tokens:   vault.Refreshed{Payload: tokens, AccessTokenExpiresAt: c.expiry(answer)},
	}, nil
}

// Refresh implements Vendor. The refresh token is spent by the exchange; an
// answer without a new one keeps the old, as the Codex CLI does.
func (c *Codex) Refresh(ctx context.Context, current vault.Payload) (vault.Refreshed, error) {
	status, body, err := c.post(ctx, "/oauth/token", "application/x-www-form-urlencoded", []byte(url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {codexClientID},
		"refresh_token": {current[vault.KeyRefreshToken]},
	}.Encode()))
	if err != nil {
		return vault.Refreshed{}, err
	}

	if status != http.StatusOK {
		err := statusError("refreshing the tokens", status, body)
		if status == http.StatusUnauthorized || slices.Contains(codexDeadGrants, errorCode(body)) {
			return vault.Refreshed{}, fmt.Errorf("%w: %w", vault.ErrLoginRejected, err)
		}

		return vault.Refreshed{}, err
	}

	var answer tokenAnswer
	if err := json.Unmarshal(body, &answer); err != nil || answer.AccessToken == "" {
		return vault.Refreshed{}, errors.New("the refresh answer lacks an access token")
	}

	tokens := vault.Payload{
		vault.KeyAccessToken:  answer.AccessToken,
		vault.KeyRefreshToken: firstNonEmpty(answer.RefreshToken, current[vault.KeyRefreshToken]),
	}
	if id := firstNonEmpty(answer.IDToken, current[vault.KeyIDToken]); id != "" {
		tokens[vault.KeyIDToken] = id
	}

	return vault.Refreshed{Payload: tokens, AccessTokenExpiresAt: c.expiry(answer)}, nil
}

// Identify implements Vendor: the ChatGPT account, email and plan the id
// token claims. The token came from the authorization server over TLS, so
// its signature is not checked, as neither reference checks it.
func (c *Codex) Identify(tokens vault.Payload) (Identity, error) {
	var claims struct {
		Email string `json:"email"`
		Auth  struct {
			AccountID string `json:"chatgpt_account_id"`
			Plan      string `json:"chatgpt_plan_type"`
		} `json:"https://api.openai.com/auth"`
	}

	if err := jwtClaims(tokens[vault.KeyIDToken], &claims); err != nil {
		return Identity{}, fmt.Errorf("reading who logged in: %w", err)
	}

	if claims.Auth.AccountID == "" {
		return Identity{}, fmt.Errorf("the login names no ChatGPT account (claim %s)", codexAuthClaim)
	}

	return Identity{ExternalID: claims.Auth.AccountID, Email: claims.Email, Plan: claims.Auth.Plan}, nil
}

// expiry is when the access token of an answer ends: what the server said,
// else what the token itself claims, else not known.
func (c *Codex) expiry(answer tokenAnswer) time.Time {
	if answer.ExpiresIn > 0 {
		return c.clock.Now().Add(time.Duration(answer.ExpiresIn) * time.Second)
	}

	var claims struct {
		Exp int64 `json:"exp"`
	}

	if err := jwtClaims(answer.AccessToken, &claims); err != nil || claims.Exp <= 0 {
		return time.Time{}
	}

	return time.Unix(claims.Exp, 0)
}

// post sends a body to the authorization server and returns its status and
// answer. Its errors hold neither the request nor the answer.
func (c *Codex) post(ctx context.Context, path, contentType string, body []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.issuer+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, fmt.Errorf("building the request to %s: %w", path, err)
	}

	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err // The address is ours to say; net/http would repeat it.
		}

		return 0, nil, fmt.Errorf("calling %s%s: %w", c.issuer, path, err)
	}
	defer resp.Body.Close() //nolint:errcheck // The answer was read or the read failed; closing adds nothing.

	answer, err := io.ReadAll(io.LimitReader(resp.Body, maxTokenResponseBytes))
	if err != nil {
		return 0, nil, fmt.Errorf("reading the answer of %s: %w", path, err)
	}

	return resp.StatusCode, answer, nil
}

// statusError describes a refusal by its status and error code only: the
// body may repeat a token.
func statusError(doing string, status int, body []byte) error {
	if code := errorCode(body); code != "" {
		return fmt.Errorf("%s: status %d, %s", doing, status, clip(code))
	}

	return fmt.Errorf("%s: status %d", doing, status)
}

// errorCode is the error code of an OAuth error body, read as the Codex CLI
// reads it (codex-rs/login/src/oauth/error.rs): "error" as a string, else
// its "code", else a top-level "code".
func errorCode(body []byte) string {
	var parsed struct {
		Error json.RawMessage `json:"error"`
		Code  string          `json:"code"`
	}

	if json.Unmarshal(body, &parsed) != nil {
		return ""
	}

	var text string
	if json.Unmarshal(parsed.Error, &text) == nil && text != "" {
		return strings.ToLower(text)
	}

	var object struct {
		Code string `json:"code"`
	}

	if json.Unmarshal(parsed.Error, &object) == nil && object.Code != "" {
		return strings.ToLower(object.Code)
	}

	return strings.ToLower(parsed.Code)
}

// jwtClaims decodes the claims of a JWT into v without checking its signature.
func jwtClaims(token string, v any) error {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return errors.New("not a JWT")
	}

	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return errors.New("the JWT's claims are not base64")
	}

	if err := json.Unmarshal(raw, v); err != nil {
		return errors.New("the JWT's claims are not JSON")
	}

	return nil
}

// pollInterval reads the seconds between two polls, sent as a number or a
// string; five when absent.
func pollInterval(raw json.RawMessage) time.Duration {
	seconds, err := strconv.Atoi(strings.Trim(strings.TrimSpace(string(raw)), `"`))
	if err != nil || seconds <= 0 {
		return codexDefaultPoll
	}

	return time.Duration(seconds) * time.Second
}

func mustJSON(v map[string]string) []byte {
	b, _ := json.Marshal(v) //nolint:errchkjson // A map of strings always encodes.

	return b
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b) // crypto/rand.Read never fails on supported platforms.

	return b
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}

	return b
}

// clip keeps a vendor's word short enough for one line.
func clip(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 120 {
		return s[:120]
	}

	return s
}
