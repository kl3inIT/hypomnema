package modelgateway

import (
	"bytes"
	"context"
	"crypto/sha1" //nolint:gosec // UUID version 5 is defined over SHA-1; nothing secret depends on it.
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway/codex"
)

// The Codex subscription (issue #20, step 3): a built-in provider kind. The
// client speaks chat completions; the backend takes Responses, always as a
// stream, under a ChatGPT account the owner logged in on the control plane.
// The translation is package codex's; this file is the call itself: the
// account's token, the headers the backend expects of the Codex CLI, a token
// the backend refuses, a usage limit, and an error inside the stream.
// It follows 9router-go (internal/proxy/grokcli.go ForwardCodex,
// handlers/chat/fallback.go, codex_quota_error.go) and, for the status of a
// failure inside a stream, CLIProxyAPI (codex_executor_terminal.go).

const (
	// codexBaseURL is the backend the Codex CLI calls with a ChatGPT login.
	codexBaseURL = "https://chatgpt.com/backend-api/codex"
	// codexRuntime is the kind of account the provider runs under.
	codexRuntime = "codex"
	// The Codex CLI the gateway presents itself as. The backend may refuse a
	// version that grows too old; 9router-go's providers/client_identity.go
	// is where to look for the one in use.
	codexCLIVersion = "0.159.0"
	codexOriginator = "codex_cli_rs"
	codexUserAgent  = "codex_cli_rs/" + codexCLIVersion
	// codexLiteHeader tells the backend a body has the lite shape.
	codexLiteHeader = "x-openai-internal-codex-responses-lite"
	// maxEventBytes bounds one event of the backend's stream.
	maxEventBytes = 10 << 20
)

// AccountToken is an account's access token with what the vendor knows the
// account by. It is never logged.
type AccountToken struct {
	AccountID string
	// ExternalID is the vendor's id of the account (Codex: the ChatGPT account).
	ExternalID  string
	AccessToken string
	// Project is the vendor's project the account works in, when it has one.
	Project string
	// Version says which of the account's tokens this is, for the one that
	// replaces it after a refusal.
	Version int
}

// ErrNoAccount says no account of a kind can be used now: none is logged
// in, or every one needs a new login.
var ErrNoAccount = errors.New("no account can be used")

// CoolingError says every account of a kind waits for a usage limit to end.
type CoolingError struct{ Until time.Time }

func (e *CoolingError) Error() string {
	return "every account waits for its usage limit, until " + e.Until.UTC().Format(time.RFC3339)
}

// Subscriptions gives a built-in provider the accounts logged in on the
// control plane; the wiring gives the subscription service's.
type Subscriptions interface {
	// Token returns a valid access token of the first account of a runtime
	// that can be used and is not among skip; ErrNoAccount or a *CoolingError
	// when none can.
	Token(ctx context.Context, runtime string, skip ...string) (AccountToken, error)
	// TokenAfterRefusal refreshes an account whose token the vendor refused
	// and returns the new one.
	TokenAfterRefusal(ctx context.Context, refused AccountToken) (AccountToken, error)
	// CoolDown keeps an account out of use until its usage limit ends.
	CoolDown(ctx context.Context, runtime, accountID string, until time.Time, reason string) error
	// Quota asks the vendor what is left of an account's quota for each
	// model, from 0 to 1; a model the vendor gives no quota for is absent.
	Quota(ctx context.Context, accountID string) (map[string]float64, error)
}

// codexSubscription serves chat from the Codex backend.
type codexSubscription struct{}

func (codexSubscription) declared() bool { return false }

func (codexSubscription) do(ctx context.Context, s *Server, call chatCall) (*http.Response, error) {
	if s.accounts == nil {
		return nil, fmt.Errorf("%w: provider %q has no accounts to run under", ErrNoAccount, call.upstream.Provider)
	}

	built, err := codex.BuildRequest(call.req.raw, codex.Options{
		Model: call.upstream.Model, CacheKey: cacheKey(call.keyID),
	})
	if err != nil {
		//nolint:nilerr // A request that cannot be translated is the client's to fix: it gets a 400, not a failover.
		return codexRefusal(&codex.Failure{
			Status: http.StatusBadRequest, Type: typeInvalidRequest, Code: "invalid_request", Message: err.Error(),
		}, s.svc.clock.Now()), nil
	}

	resp, err := s.underAccounts(ctx, codexRuntime, nil, func(token AccountToken) (*http.Response, error) {
		resp, err := s.codexCall(ctx, &token, built)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode != http.StatusOK {
			return s.codexRefused(ctx, token, resp), nil
		}

		s.accountState.served(token.AccountID)

		return s.codexAnswer(ctx, call, token, built, resp), nil
	})
	if err != nil {
		return nil, fmt.Errorf("provider %q: %w", call.upstream.Provider, err)
	}

	return resp, nil
}

// codexCall sends a built request under an account. A token the backend
// refuses is refreshed once and the request sent again; token is then the
// new one.
func (s *Server) codexCall(ctx context.Context, token *AccountToken, built codex.Request) (*http.Response, error) {
	resp, err := s.codexPost(ctx, *token, built)
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}

	drain(resp)

	if *token, err = s.accounts.TokenAfterRefusal(ctx, *token); err != nil {
		return nil, err
	}

	return s.codexPost(ctx, *token, built)
}

// codexPost sends a built request under an account, as the Codex CLI would.
func (s *Server) codexPost(ctx context.Context, token AccountToken, built codex.Request) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.codexURL+"/responses", bytes.NewReader(built.Body))
	if err != nil {
		return nil, fmt.Errorf("building the request to Codex: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("User-Agent", codexUserAgent)
	// The backend reads these names as written, not in Go's canonical case.
	req.Header["originator"] = []string{codexOriginator}
	req.Header["version"] = []string{codexCLIVersion}
	req.Header["chatgpt-account-id"] = []string{token.ExternalID}

	if built.CacheKey != "" {
		req.Header["session_id"] = []string{built.CacheKey}
	}

	if built.Lite {
		req.Header[codexLiteHeader] = []string{"true"}
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling Codex: %w", err)
	}

	return resp, nil
}

// codexRefused passes on a request the backend refused: its status and its
// body, as 9router-go does. The account cools down for as long as the kind of
// failure calls for (accounts.go).
func (s *Server) codexRefused(ctx context.Context, token AccountToken, resp *http.Response) *http.Response {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBytes))
	_ = resp.Body.Close()

	failure := codex.ParseFailure(resp.StatusCode, body)
	s.codexCool(ctx, token, failure)

	out := &http.Response{
		StatusCode: failure.Status, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(bytes.NewReader(body)),
	}

	if retry := retryAfter(failure, s.svc.clock.Now()); retry != "" {
		out.Header.Set("Retry-After", retry)
	} else if retry := resp.Header.Get("Retry-After"); retry != "" {
		out.Header.Set("Retry-After", retry)
	}

	return out
}

func (s *Server) codexCool(ctx context.Context, token AccountToken, failure *codex.Failure) {
	s.cool(ctx, codexRuntime, token, failure.Status, failure.Type == "usage_limit_reached", failure.ResetsAt,
		"usage limit")
}

// codexAnswer turns the backend's event stream into the answer.
func (s *Server) codexAnswer(
	ctx context.Context, call chatCall, token AccountToken, built codex.Request, resp *http.Response,
) *http.Response {
	now := s.svc.clock.Now()
	stream := codex.NewStream(s.svc.newID("chatcmpl"), now.Unix(), call.model, built.Names)

	return s.translated(ctx, call, resp, translation{
		event: stream.Event, finished: stream.Finished, completion: stream.Completion,
		failed: func() bool { return stream.Failed() != nil },
		refusal: func() *http.Response {
			failure := stream.Failed()
			if failure == nil {
				failure = &codex.Failure{
					Status: http.StatusBadGateway, Type: typeServer, Code: "upstream_truncated",
					Message: "Codex ended its answer early",
				}
			}

			s.codexCool(ctx, token, failure)

			return codexRefusal(failure, now)
		},
	})
}

// codexRefusal is an error of the backend as an answer in OpenAI's shape.
func codexRefusal(failure *codex.Failure, now time.Time) *http.Response {
	var body errorBody

	body.Error.Message, body.Error.Type, body.Error.Code = failure.Message, failure.Type, failure.Code
	if body.Error.Type == "" {
		body.Error.Type = typeServer
	}

	encoded, _ := json.Marshal(body) //nolint:errchkjson // A struct of strings always encodes.
	resp := &http.Response{
		StatusCode: failure.Status, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(bytes.NewReader(encoded)),
	}

	if retry := retryAfter(failure, now); retry != "" {
		resp.Header.Set("Retry-After", retry)
	}

	return resp
}

// retryAfter is the seconds until a usage limit ends, for the Retry-After header.
func retryAfter(failure *codex.Failure, now time.Time) string {
	if failure.ResetsAt.IsZero() || !failure.ResetsAt.After(now) {
		return ""
	}

	return strconv.Itoa(int(failure.ResetsAt.Sub(now).Seconds()) + 1)
}

func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBytes))
	_ = resp.Body.Close()
}

// cacheKey is the prompt cache key, and the session, for a client that names
// none: a UUID made from the gateway key, so one client keeps one cache
// (CLIProxyAPI, codex_executor_request.go cacheHelperDoc).
func cacheKey(keyID string) string {
	sum := sha1.Sum([]byte("hypomnema:codex:prompt-cache:" + keyID)) //nolint:gosec // See the import.
	sum[6] = (sum[6] & 0x0f) | 0x50
	sum[8] = (sum[8] & 0x3f) | 0x80

	h := hex.EncodeToString(sum[:16])

	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}
