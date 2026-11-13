package subscription

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

// The Claude subscription vendor (issue #20, step 5): the owner's Claude
// Pro/Max account, logged in with the OAuth flow Claude Code uses, so the
// gateway can serve Claude models from it. Ported from 9router-go
// (internal/handlers/oauth/pkce.go, internal/proxy/oauth/claude.go).

const (
	claudeRuntime = "claude"
	// claudeClientID is Claude Code's own OAuth client.
	claudeClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	claudeScope    = "org:create_api_key user:profile user:inference"
	// claudeRedirect is the manual-flow redirect that shows the code to paste.
	claudeRedirect = "https://console.anthropic.com/oauth/code/callback"
	// claudeRefreshLead is 9router-go's lead before a token's end.
	claudeRefreshLead  = 4 * time.Hour
	claudeLoginTimeout = 15 * time.Minute
	claudeHTTPTimeout  = 30 * time.Second
)

// Claude is the Vendor for a Claude subscription.
type Claude struct {
	// authBase is claude.ai's OAuth host; tokenBase is where the code and the
	// refresh token are exchanged; profileBase answers who logged in. Empty
	// outside tests.
	authBase, tokenBase, profileBase string
	client                           *http.Client
	clock                            platform.Clock
}

// ClaudeOptions override the vendor's hosts, for tests.
type ClaudeOptions struct {
	AuthBase, TokenBase, ProfileBase string
}

// NewClaude returns the Vendor for a Claude subscription.
func NewClaude(opt ClaudeOptions, clock platform.Clock) *Claude {
	base := func(v, fallback string) string {
		if v == "" {
			return fallback
		}

		return strings.TrimRight(v, "/")
	}

	return &Claude{
		authBase:    base(opt.AuthBase, "https://claude.ai"),
		tokenBase:   base(opt.TokenBase, "https://console.anthropic.com"),
		profileBase: base(opt.ProfileBase, "https://api.anthropic.com"),
		client:      &http.Client{Timeout: claudeHTTPTimeout},
		clock:       clock,
	}
}

// Runtime implements Vendor.
func (c *Claude) Runtime() string { return claudeRuntime }

// RefreshLead implements Vendor.
func (c *Claude) RefreshLead() time.Duration { return claudeRefreshLead }

// RefreshSpacing implements Vendor.
func (c *Claude) RefreshSpacing() time.Duration { return 1500 * time.Millisecond }

// Begin implements Vendor: the owner opens the link, signs in, and pastes the
// code the page shows (`code#state`).
func (c *Claude) Begin(context.Context) (*Attempt, error) {
	verifier := base64.RawURLEncoding.EncodeToString(randomBytes(32))
	challenge := sha256.Sum256([]byte(verifier))
	state := hex.EncodeToString(randomBytes(16))
	started := c.clock.Now()

	query := url.Values{
		"code":                  {"true"},
		"client_id":             {claudeClientID},
		"response_type":         {"code"},
		"redirect_uri":          {claudeRedirect},
		"scope":                 {claudeScope},
		"state":                 {state},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(challenge[:])},
		"code_challenge_method": {"S256"},
	}

	return &Attempt{
		URL: c.authBase + "/oauth/authorize?" + query.Encode(), Paste: true, ExpiresIn: claudeLoginTimeout,
		finish: func(ctx context.Context, pasted string) (Login, error) {
			if c.clock.Now().Sub(started) > claudeLoginTimeout {
				return Login{}, errors.New("the login was started too long ago; start it again")
			}

			code, err := claudeCode(pasted, state)
			if err != nil {
				return Login{}, err
			}

			return c.exchange(ctx, code, state, verifier)
		},
	}, nil
}

// claudeCode reads the code from what the owner pasted. Claude's manual flow
// shows `code#state`; a whole callback address is accepted too.
func claudeCode(pasted, state string) (string, error) {
	pasted = strings.TrimSpace(pasted)

	if strings.Contains(pasted, "://") {
		return CallbackCode(pasted, state)
	}

	code, gotState, found := strings.Cut(pasted, "#")
	switch {
	case code == "":
		return "", errors.New("the paste has no code; copy the whole value the page showed")
	case found && gotState != state:
		return "", errors.New("the paste belongs to another login; start again and use the new link")
	}

	return code, nil
}

type claudeTokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Account      struct {
		UUID         string `json:"uuid"`
		EmailAddress string `json:"email_address"`
	} `json:"account"`
	Organization struct {
		UUID string `json:"uuid"`
		Name string `json:"name"`
	} `json:"organization"`
}

func (c *Claude) exchange(ctx context.Context, code, state, verifier string) (Login, error) {
	var answer claudeTokens

	err := c.postJSON(ctx, c.tokenBase+"/v1/oauth/token", "exchanging the code", map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     claudeClientID,
		"code":          code,
		"state":         state,
		"redirect_uri":  claudeRedirect,
		"code_verifier": verifier,
	}, &answer, nil)
	if err != nil {
		return Login{}, err
	}

	if answer.AccessToken == "" || answer.RefreshToken == "" {
		return Login{}, errors.New("the token answer lacks an access or a refresh token")
	}

	tokens := vault.Payload{vault.KeyAccessToken: answer.AccessToken, vault.KeyRefreshToken: answer.RefreshToken}

	who := c.whoLoggedIn(ctx, answer)

	return Login{
		Identity: who,
		Tokens:   vault.Refreshed{Payload: tokens, AccessTokenExpiresAt: c.expiry(answer.ExpiresIn)},
	}, nil
}

// whoLoggedIn reads the account from the token answer, else the profile. Its
// id is the organization's, else the user's (issue #326): one person can have
// a personal plan and a seat in a Team organization, two subscriptions with
// their own limits, told apart by the organization as CLIProxyAPI does
// (internal/auth/claude/filename.go CredentialFileName); the user's id is then
// the id an earlier login may have been recorded under. An id that is nowhere
// is derived from the refresh token, so the account still has a stable id for
// the owner's list.
func (c *Claude) whoLoggedIn(ctx context.Context, answer claudeTokens) Identity {
	user, org := answer.Account.UUID, answer.Organization.UUID
	email, plan := answer.Account.EmailAddress, answer.Organization.Name

	if user == "" || org == "" || email == "" {
		var profile struct {
			Account struct {
				UUID         string `json:"uuid"`
				EmailAddress string `json:"email_address"`
			} `json:"account"`
			Organization struct {
				UUID string `json:"uuid"`
				Name string `json:"name"`
			} `json:"organization"`
		}

		if c.getJSON(ctx, c.profileBase+"/api/oauth/profile", answer.AccessToken, &profile) == nil {
			user = firstNonEmpty(user, profile.Account.UUID)
			org = firstNonEmpty(org, profile.Organization.UUID)
			email = firstNonEmpty(email, profile.Account.EmailAddress)
			plan = firstNonEmpty(plan, profile.Organization.Name)
		}
	}

	who := Identity{ExternalID: firstNonEmpty(org, user), Email: email, Plan: plan}
	if org != "" && user != "" {
		who.FormerExternalID = user
	}

	if who.ExternalID == "" {
		sum := sha256.Sum256([]byte(answer.RefreshToken))
		who.ExternalID = "claude-" + hex.EncodeToString(sum[:])[:16]
	}

	if who.Email == "" {
		who.Email = "Claude subscription"
	}

	return who
}

// Refresh implements Vendor. Anthropic may or may not return a new refresh
// token; the old one is kept when it does not.
func (c *Claude) Refresh(ctx context.Context, current vault.Payload) (vault.Refreshed, error) {
	var answer claudeTokens

	var status int

	err := c.postJSON(ctx, c.tokenBase+"/v1/oauth/token", "refreshing the tokens", map[string]string{
		"grant_type":    "refresh_token",
		"client_id":     claudeClientID,
		"refresh_token": current[vault.KeyRefreshToken],
	}, &answer, &status)
	if err != nil {
		if status == http.StatusBadRequest || status == http.StatusUnauthorized || status == http.StatusForbidden {
			return vault.Refreshed{}, fmt.Errorf("%w: %w", vault.ErrLoginRejected, err)
		}

		return vault.Refreshed{}, err
	}

	if answer.AccessToken == "" {
		return vault.Refreshed{}, errors.New("the refresh answer lacks an access token")
	}

	return vault.Refreshed{
		Payload: vault.Payload{
			vault.KeyAccessToken:  answer.AccessToken,
			vault.KeyRefreshToken: firstNonEmpty(answer.RefreshToken, current[vault.KeyRefreshToken]),
		},
		AccessTokenExpiresAt: c.expiry(answer.ExpiresIn),
	}, nil
}

// Identify implements Vendor. A Claude OAuth token is opaque, so who it
// belongs to is not in it: the login's recorded identity is kept.
func (c *Claude) Identify(vault.Payload) (Identity, error) {
	return Identity{}, errors.New("a Claude token names nobody; the login recorded whose it is")
}

func (c *Claude) expiry(expiresIn int64) time.Time {
	if expiresIn <= 0 {
		return time.Time{}
	}

	return c.clock.Now().Add(time.Duration(expiresIn) * time.Second)
}

// postJSON sends a JSON body and reads a JSON answer. status, when given, is
// set to the HTTP status even on an error.
func (c *Claude) postJSON(
	ctx context.Context, endpoint, doing string, body map[string]string, out any, status *int,
) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("%s: encoding the request: %w", doing, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("%s: %w", doing, err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", doing, err)
	}
	defer resp.Body.Close() //nolint:errcheck // the error is the request's

	answer, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if status != nil {
		*status = resp.StatusCode
	}

	if resp.StatusCode != http.StatusOK {
		return statusError(doing, resp.StatusCode, answer)
	}

	if err := json.Unmarshal(answer, out); err != nil {
		return fmt.Errorf("%s: reading the answer: %w", doing, err)
	}

	return nil
}

func (c *Claude) getJSON(ctx context.Context, endpoint, token string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck // read only

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("the profile answered %d", resp.StatusCode)
	}

	return json.NewDecoder(resp.Body).Decode(out)
}
