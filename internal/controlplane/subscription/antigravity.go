package subscription

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/cloudcode"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

// The Antigravity subscription: a Google account, logged in as the
// Antigravity IDE does, with the Cloud Code project the account works in.
//
// Antigravity's terms name access through other software as a breach that
// may end the account; the owner chose to build this knowing it (issue #20,
// step 4). What is sent is what the references and Google's own client send,
// with nothing added to pass for the IDE beyond its user agent.
//
// Read on 2026-11-10:
//   - CLIProxyAPI (MIT, Copyright (c) 2025 Luis Pater, Router-For.ME), commit
//     c5251b5: internal/auth/antigravity/auth.go and constants.go (the login,
//     the user's email, the project);
//   - Google's Gemini CLI, commit 1930f25: packages/core/src/code_assist/setup.ts
//     (loadCodeAssist, the tiers, onboardUser and its operation), the client
//     this backend is made for;
//   - omp, commit 660a31d: packages/ai/src/registry/oauth/google-antigravity.ts
//     (the same steps for an Antigravity account, an account to be verified).

const (
	antigravityRuntime = "antigravity"
	// antigravityClientID is the Antigravity IDE's OAuth client. Its secret
	// is configuration (AntigravityOptions.ClientSecret), never source.
	//nolint:gosec // A public client id, as every client of Google's carries; the secret is configuration.
	antigravityClientID = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"
	// antigravityRedirect is where the IDE listens. Nothing listens here: the
	// owner copies the address the browser ends on.
	antigravityRedirect     = "http://localhost:51121/oauth-callback"
	antigravityLoginTimeout = 30 * time.Minute
	// antigravityRefreshLead follows 9router-go's background refresh and CLIProxyAPI.
	antigravityRefreshLead = 30 * time.Minute
	// The project is created in the background; its operation is asked for
	// this often, this long (omp: every second for 30 s; Google's client:
	// every 5 s).
	antigravityOnboardEvery = 2 * time.Second
	antigravityOnboardFor   = 60 * time.Second
	antigravityFreeTier     = "free-tier"
)

var antigravityScopes = []string{
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/userinfo.email",
	"https://www.googleapis.com/auth/userinfo.profile",
	"https://www.googleapis.com/auth/aicode",
	"https://www.googleapis.com/auth/cclog",
	"https://www.googleapis.com/auth/experimentsandconfigs",
}

// AntigravityOptions configures the Antigravity vendor. Only ClientSecret is
// set outside tests.
type AntigravityOptions struct {
	// ClientSecret is the OAuth secret of the Antigravity IDE's client, from
	// HYPOMNEMA_ANTIGRAVITY_CLIENT_SECRET. Without it no login can be made.
	ClientSecret string
	// AuthURL, TokenURL and UserInfoURL are Google's; CodeAssistHosts are the
	// Cloud Code hosts tried in order for the project.
	AuthURL         string
	TokenURL        string
	UserInfoURL     string
	CodeAssistHosts []string
	// ManifestURL is the IDE's update manifest.
	ManifestURL string
}

// Antigravity is the Vendor of Google accounts used through Antigravity.
type Antigravity struct {
	opt      AntigravityOptions
	client   *http.Client
	clock    platform.Clock
	identity *cloudcode.Identity
	after    func(time.Duration) <-chan time.Time
}

// NewAntigravity returns the Vendor for Antigravity.
func NewAntigravity(opt AntigravityOptions, clock platform.Clock) *Antigravity {
	if opt.AuthURL == "" {
		opt.AuthURL = "https://accounts.google.com/o/oauth2/v2/auth"
	}

	if opt.TokenURL == "" {
		opt.TokenURL = "https://oauth2.googleapis.com/token"
	}

	if opt.UserInfoURL == "" {
		opt.UserInfoURL = "https://www.googleapis.com/oauth2/v2/userinfo?alt=json"
	}

	if len(opt.CodeAssistHosts) == 0 {
		// The daily host first, as CLIProxyAPI, omp and the opencode plugin;
		// 9router found accounts for which only the other one answers.
		opt.CodeAssistHosts = []string{cloudcode.DailyHost, cloudcode.ProdHost}
	}

	return &Antigravity{
		opt: opt, client: &http.Client{Timeout: codexHTTPTimeout}, clock: clock,
		identity: cloudcode.NewIdentity(opt.ManifestURL, clock.Now), after: time.After,
	}
}

// Runtime implements Vendor.
func (a *Antigravity) Runtime() string { return antigravityRuntime }

// RefreshLead implements Vendor.
func (a *Antigravity) RefreshLead() time.Duration { return antigravityRefreshLead }

// RefreshSpacing implements Vendor: 9router-go's longer pause for the
// providers it calls sensitive (internal/proxy/oauth/background.go).
func (a *Antigravity) RefreshSpacing() time.Duration { return 12 * time.Second }

// Identify implements Vendor. A Google token set does not say whose it is:
// the account keeps what its login found.
func (a *Antigravity) Identify(vault.Payload) (Identity, error) {
	return Identity{}, errors.New("an Antigravity token set names nobody")
}

// Begin implements Vendor: Google's consent page, whose loopback address the
// owner pastes back.
func (a *Antigravity) Begin(context.Context) (*Attempt, error) {
	if a.opt.ClientSecret == "" {
		return nil, errors.New("the Antigravity client secret is not configured " +
			"(HYPOMNEMA_ANTIGRAVITY_CLIENT_SECRET in the control plane's environment)")
	}

	state := hex.EncodeToString(randomBytes(16))
	started := a.clock.Now()

	query := url.Values{
		"access_type":   {"offline"},
		"client_id":     {antigravityClientID},
		"prompt":        {"consent"},
		"redirect_uri":  {antigravityRedirect},
		"response_type": {"code"},
		"scope":         {strings.Join(antigravityScopes, " ")},
		"state":         {state},
	}

	return &Attempt{
		URL: a.opt.AuthURL + "?" + query.Encode(), Paste: true, ExpiresIn: antigravityLoginTimeout,
		finish: func(ctx context.Context, pasted string) (Login, error) {
			if a.clock.Now().Sub(started) > antigravityLoginTimeout {
				return Login{}, errors.New("the login was started too long ago; start it again")
			}

			code, err := callbackCode(pasted, state)
			if err != nil {
				return Login{}, err
			}

			return a.login(ctx, code)
		},
	}, nil
}

type googleTokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

// login exchanges the code, then finds who logged in and the project the
// account works in. A login without a project could serve nothing, so it fails.
func (a *Antigravity) login(ctx context.Context, code string) (Login, error) {
	tokens, err := a.token(ctx, "exchanging the code for tokens", url.Values{
		"grant_type":   {"authorization_code"},
		"code":         {code},
		"redirect_uri": {antigravityRedirect},
	})
	if err != nil {
		return Login{}, err
	}

	if tokens.RefreshToken == "" {
		return Login{}, errors.New("google gave no refresh token; remove Antigravity's access in the Google " +
			"account's security settings, then log in again")
	}

	var user struct {
		ID    string `json:"id"`
		Email string `json:"email"`
	}

	if err := a.call(ctx, http.MethodGet, a.opt.UserInfoURL, tokens.AccessToken, nil, &user); err != nil {
		return Login{}, fmt.Errorf("reading who logged in: %w", err)
	}

	if user.Email == "" {
		return Login{}, errors.New("google did not say whose account this is")
	}

	project, tier, err := a.project(ctx, tokens.AccessToken)
	if err != nil {
		return Login{}, err
	}

	return Login{
		Identity: Identity{
			ExternalID: firstNonEmpty(user.ID, user.Email), Email: user.Email, Plan: tier, Project: project,
		},
		Tokens: vault.Refreshed{
			Payload: vault.Payload{
				vault.KeyAccessToken: tokens.AccessToken, vault.KeyRefreshToken: tokens.RefreshToken,
			},
			AccessTokenExpiresAt: a.expiry(tokens),
		},
	}, nil
}

// Refresh implements Vendor. Google keeps the refresh token as it is.
func (a *Antigravity) Refresh(ctx context.Context, current vault.Payload) (vault.Refreshed, error) {
	tokens, err := a.token(ctx, "refreshing the tokens", url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {current[vault.KeyRefreshToken]},
	})
	if err != nil {
		return vault.Refreshed{}, err
	}

	return vault.Refreshed{
		Payload: vault.Payload{
			vault.KeyAccessToken:  tokens.AccessToken,
			vault.KeyRefreshToken: firstNonEmpty(tokens.RefreshToken, current[vault.KeyRefreshToken]),
		},
		AccessTokenExpiresAt: a.expiry(tokens),
	}, nil
}

func (a *Antigravity) expiry(tokens googleTokens) time.Time {
	if tokens.ExpiresIn <= 0 {
		return time.Time{}
	}

	return a.clock.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second)
}

// token calls Google's token endpoint. A grant Google refuses for good
// (invalid_grant: revoked, expired, or the password changed) wraps
// vault.ErrLoginRejected; its errors hold the status and the error code only.
func (a *Antigravity) token(ctx context.Context, doing string, form url.Values) (googleTokens, error) {
	form.Set("client_id", antigravityClientID)
	form.Set("client_secret", a.opt.ClientSecret)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.opt.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return googleTokens{}, fmt.Errorf("%s: building the request: %w", doing, err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	status, body, err := a.do(req)
	if err != nil {
		return googleTokens{}, fmt.Errorf("%s: %w", doing, err)
	}

	if status != http.StatusOK {
		err := statusError(doing, status, body)
		if code := errorCode(body); status == http.StatusUnauthorized || code == "invalid_grant" {
			return googleTokens{}, fmt.Errorf("%w: %w", vault.ErrLoginRejected, err)
		}

		return googleTokens{}, err
	}

	var tokens googleTokens
	if err := json.Unmarshal(body, &tokens); err != nil || tokens.AccessToken == "" {
		return googleTokens{}, fmt.Errorf("%s: the answer lacks an access token", doing)
	}

	return tokens, nil
}

// Models implements ModelLister: the models the backend serves this account,
// with each one's remaining quota (fetchAvailableModels, as 9router-go's
// handlers/chat/antigravity_quota.go and CLIProxyAPI's sdk/auth/antigravity_models.go
// read it). The names are the ones a request must carry.
func (a *Antigravity) Models(ctx context.Context, token Token) ([]ServedModel, error) {
	var (
		answer struct {
			Models map[string]struct {
				IsInternal bool `json:"isInternal"`
				QuotaInfo  *struct {
					RemainingFraction *float64 `json:"remainingFraction"`
					ResetTime         string   `json:"resetTime"`
				} `json:"quotaInfo"`
			} `json:"models"`
		}
		failed []error
	)

	for _, host := range a.opt.CodeAssistHosts {
		address := strings.TrimRight(host, "/") + "/" + cloudcode.APIVersion + ":fetchAvailableModels"

		err := a.call(ctx, http.MethodPost, address, token.AccessToken, map[string]string{"project": token.Project}, &answer)
		if err != nil {
			failed = append(failed, err)

			continue
		}

		models := make([]ServedModel, 0, len(answer.Models))

		for name, info := range answer.Models {
			if info.IsInternal {
				continue
			}

			model := ServedModel{Name: name, Remaining: -1}

			if q := info.QuotaInfo; q != nil {
				if q.RemainingFraction != nil {
					model.Remaining = *q.RemainingFraction
				}

				model.ResetsAt, _ = time.Parse(time.RFC3339, q.ResetTime)
			}

			models = append(models, model)
		}

		slices.SortFunc(models, func(x, y ServedModel) int { return strings.Compare(x.Name, y.Name) })

		return models, nil
	}

	return nil, fmt.Errorf("asking which models the account is served: %w", errors.Join(failed...))
}

// codeAssist is what loadCodeAssist says of an account.
type codeAssist struct {
	Project     json.RawMessage `json:"cloudaicompanionProject"`
	CurrentTier *struct {
		ID string `json:"id"`
	} `json:"currentTier"`
	PaidTier *struct {
		ID string `json:"id"`
	} `json:"paidTier"`
	AllowedTiers []struct {
		ID        string `json:"id"`
		IsDefault bool   `json:"isDefault"`
	} `json:"allowedTiers"`
	IneligibleTiers []struct {
		ReasonCode    string `json:"reasonCode"`
		ReasonMessage string `json:"reasonMessage"`
		ValidationURL string `json:"validationUrl"`
	} `json:"ineligibleTiers"`
}

// project finds the Cloud Code project of an account and its tier, creating
// the project when the account never used Antigravity. The hosts are tried in
// order; the first that knows the account answers.
func (a *Antigravity) project(ctx context.Context, accessToken string) (project, tier string, err error) {
	var failed []error

	for _, host := range a.opt.CodeAssistHosts {
		project, tier, err = a.projectAt(ctx, host, accessToken)
		if err == nil {
			return project, tier, nil
		}

		failed = append(failed, err)

		var verify *VerificationError
		if errors.As(err, &verify) || ctx.Err() != nil {
			break // The other host would say the same.
		}
	}

	return "", "", fmt.Errorf("finding the account's Cloud Code project: %w", errors.Join(failed...))
}

// VerificationError says Google wants the account verified before it can be
// used; URL is where.
type VerificationError struct {
	Message string
	URL     string
}

func (e *VerificationError) Error() string {
	return "Google asks for this account to be verified first: " + clip(e.Message) + " " + e.URL
}

func (a *Antigravity) projectAt(ctx context.Context, host, accessToken string) (project, tier string, err error) {
	base := strings.TrimRight(host, "/") + "/" + cloudcode.APIVersion
	metadata := map[string]string{"ideType": "ANTIGRAVITY"}

	var loaded codeAssist
	if err := a.call(ctx, http.MethodPost, base+":loadCodeAssist", accessToken,
		map[string]any{"metadata": metadata}, &loaded); err != nil {
		return "", "", err
	}

	tier = antigravityFreeTier

	for _, allowed := range loaded.AllowedTiers {
		if allowed.IsDefault && allowed.ID != "" {
			tier = allowed.ID
		}
	}

	if loaded.CurrentTier != nil && loaded.CurrentTier.ID != "" {
		tier = loaded.CurrentTier.ID
	}

	if loaded.PaidTier != nil && loaded.PaidTier.ID != "" {
		tier = loaded.PaidTier.ID
	}

	if project = projectID(loaded.Project); project != "" {
		return project, tier, nil
	}

	if loaded.CurrentTier == nil {
		for _, no := range loaded.IneligibleTiers {
			if no.ReasonCode == "VALIDATION_REQUIRED" && no.ValidationURL != "" {
				return "", "", &VerificationError{Message: no.ReasonMessage, URL: no.ValidationURL}
			}
		}
	}

	// The account has no project yet: Google makes one, in the background.
	var operation struct {
		Name     string `json:"name"`
		Done     bool   `json:"done"`
		Response struct {
			Project json.RawMessage `json:"cloudaicompanionProject"`
		} `json:"response"`
	}

	onboard := map[string]any{"tierId": tier, "metadata": metadata}
	if err := a.call(ctx, http.MethodPost, base+":onboardUser", accessToken, onboard, &operation); err != nil {
		return "", "", err
	}

	deadline := a.clock.Now().Add(antigravityOnboardFor)

	for !operation.Done && a.clock.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return "", "", fmt.Errorf("waiting for the project: %w", ctx.Err())
		case <-a.after(antigravityOnboardEvery):
		}

		// The operation is asked for by its name, as Google's client does;
		// one with no name is asked for again as the references do.
		if operation.Name != "" {
			err = a.call(ctx, http.MethodGet, base+"/"+operation.Name, accessToken, nil, &operation)
		} else {
			err = a.call(ctx, http.MethodPost, base+":onboardUser", accessToken, onboard, &operation)
		}

		if err != nil {
			return "", "", err
		}
	}

	if project = projectID(operation.Response.Project); project == "" {
		return "", "", errors.New("google made no Cloud Code project for this account")
	}

	return project, tier, nil
}

// projectID reads a project given as its id or as an object holding it.
func projectID(raw json.RawMessage) string {
	var id string
	if json.Unmarshal(raw, &id) == nil {
		return strings.TrimSpace(id)
	}

	var object struct {
		ID string `json:"id"`
	}

	_ = json.Unmarshal(raw, &object)

	return strings.TrimSpace(object.ID)
}

// call sends one request to Google under the account, as the IDE would: the
// token and the IDE's user agent, and no other Google header, since those
// made the backend refuse (9router, opencode-antigravity-auth).
func (a *Antigravity) call(ctx context.Context, method, address, accessToken string, body, into any) error {
	var payload io.Reader

	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encoding the request: %w", err)
		}

		payload = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, address, payload)
	if err != nil {
		return fmt.Errorf("building the request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("User-Agent", a.identity.UserAgent(ctx))

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	status, answer, err := a.do(req)
	if err != nil {
		return err
	}

	if status/100 != 2 {
		return statusError("calling "+req.URL.Host+req.URL.Path, status, googleError(answer))
	}

	if err := json.Unmarshal(answer, into); err != nil {
		return fmt.Errorf("reading the answer of %s: not JSON", req.URL.Path)
	}

	return nil
}

func (a *Antigravity) do(req *http.Request) (int, []byte, error) {
	resp, err := a.client.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err // The address may carry a code; net/http would repeat it.
		}

		return 0, nil, fmt.Errorf("calling %s: %w", req.URL.Host, err)
	}
	defer resp.Body.Close() //nolint:errcheck // The answer was read or the read failed; closing adds nothing.

	answer, err := io.ReadAll(io.LimitReader(resp.Body, maxTokenResponseBytes))
	if err != nil {
		return 0, nil, fmt.Errorf("reading the answer of %s: %w", req.URL.Host, err)
	}

	return resp.StatusCode, answer, nil
}

// googleError turns a Google API error body into the shape statusError
// reads: its status word as the code.
func googleError(body []byte) []byte {
	var parsed struct {
		Error struct {
			Status string `json:"status"`
		} `json:"error"`
	}

	if json.Unmarshal(body, &parsed) != nil || parsed.Error.Status == "" {
		return body
	}

	encoded, _ := json.Marshal(map[string]string{"error": parsed.Error.Status}) //nolint:errchkjson // Strings encode.

	return encoded
}
