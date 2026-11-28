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
	"log/slog"
	"net/http"
	"strconv"
	"sync/atomic"
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
	// is where to look for the one in use. It is Codex's latest release,
	// rust-v0.169.1 (2026-11-17, issue #795), and the same as the version the
	// model list names (subscription/models.go, codexClientVersion).
	codexCLIVersion = "0.169.1"
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
	// Plan is the account's plan as its login read it (Codex: chatgpt_plan_type,
	// Antigravity: the tier).
	Plan string
	// Version says which of the account's tokens this is, for the one that
	// replaces it after a refusal.
	Version int
	// Verifying says Google asked for the account to be verified and this is
	// its next try (issue #822).
	Verifying bool
	// VerificationRetry is the deadline claimed by this request's retry, if any.
	VerificationRetry *atomic.Int64
}

// ErrNoAccount says no account of a kind can be used now: none is logged
// in, or every one needs a new login.
var ErrNoAccount = errors.New("no account can be used")

// ErrAccountResting says an account cannot be used before a later time, such
// as one Google wants verified that is not due for its next try (issue #822):
// its request goes to the next account.
var ErrAccountResting = errors.New("the account rests until its next try")

// ErrTokenUnavailable says a refused credential cannot be renewed for this request.
var ErrTokenUnavailable = errors.New("the refused account token cannot be renewed")

// CoolingError says every account of a kind waits for a usage limit to end.
type CoolingError struct {
	Until time.Time
	// Code is why the account free first rests (issue #1305); empty when nothing says. Reason is the provider's own
	// words for it.
	Code   CoolingCode
	Reason string
	// ListUnknown says no account is known to have the model asked for while an account's list of models is not
	// known yet (issue #814).
	ListUnknown bool
}

func (e *CoolingError) Error() string {
	return "every account waits for its usage limit, until " + e.Until.UTC().Format(time.RFC3339)
}

// Subscriptions gives a built-in provider the accounts logged in on the
// control plane; the wiring gives the subscription service's.
type Subscriptions interface {
	// Token returns a valid access token of an account of a runtime that can be
	// used and is not among skip, ordered by the session (sticky, so one
	// conversation keeps one account) or round-robin when the session is empty;
	// ErrNoAccount or a *CoolingError when none can.
	Token(ctx context.Context, runtime, session string, skip ...string) (AccountToken, error)
	// FirstFree is when the first account of a runtime is free again and why it rests, a model's own rest
	// (modelReset, by account) counted; zero when one is free now.
	FirstFree(ctx context.Context, runtime string, modelReset func(string) Rest) (Rest, error)
	// TokenAfterRefusal refreshes an account whose token the vendor refused
	// and returns the new one.
	TokenAfterRefusal(ctx context.Context, refused AccountToken) (AccountToken, error)
	MarkTokenRefused(ctx context.Context, refused AccountToken) error
	// CoolDown keeps an account out of use until a time, with why (code) and the provider's own words (reason).
	CoolDown(ctx context.Context, runtime, accountID string, until time.Time, code CoolingCode, reason string) error
	// Quota asks the vendor what is left of an account's quota for each
	// model, from 0 to 1; a model the vendor gives no quota for is absent.
	Quota(ctx context.Context, accountID string) (map[string]float64, error)
	// NoteQuota records what is left of an account's whole quota, from 0 to
	// 1, as an answer served under it said; Token then keeps an account that
	// is nearly used up for when no other can serve.
	NoteQuota(accountID string, left float64)
	// NoteAnswer gives the headers of an answer served under an account, for
	// what they say of its limits (issue #199).
	NoteAnswer(runtime, accountID string, h http.Header)
}

// ModelTokens is Subscriptions that can give, for a request for a model, a
// token of only the accounts whose own list of models has it (issue #814), as
// CLIProxyAPI routes a model only to the auths that registered it
// (sdk/cliproxy/auth/conductor_selection.go:1419-1431 authSupportsRouteModel,
// applied at :1687). While no account is known to have it and one's list is
// not known yet, it answers a *CoolingError; once every list is known and none
// has it, ErrModelNotServed. Subscriptions without it give any account, as
// Token does.
type ModelTokens interface {
	TokenFor(ctx context.Context, runtime, model, session string, skip ...string) (AccountToken, error)
	// TokenAfterRefusalFor is TokenAfterRefusal for a request for a model:
	// ErrModelNotServed when the account is no longer usable or its list no
	// longer has the model.
	TokenAfterRefusalFor(ctx context.Context, refused AccountToken, model string) (AccountToken, error)
	// Serves says an account is still usable, not cooling, and its list has
	// the model.
	Serves(ctx context.Context, accountID, model string) bool
}

// ErrModelNotServed says no account's list of models has the model asked for
// (issue #814); a client is told so with codeModelNotServed.
var ErrModelNotServed = errors.New("the model is not served by any account")

const codeModelNotServed = "model_not_served"

// ErrNoModel says a token was asked for without the model it will call, of a
// runtime whose accounts are each served their own models (issue #814).
var ErrNoModel = errors.New("a token is asked for without its model")

// forModel is the model a request calls under a runtime's accounts (issue
// #814). A Codex or Antigravity request names it: underAccounts refuses one
// that does not.
type forModel struct {
	name string
	// listed says the vendor's list of an account's models names the model,
	// so it goes only to the accounts whose list has it (ModelTokens). Codex's
	// image and search models and Antigravity's search model are not on those
	// lists: their requests go to any account, as before (owner, 2026-11-18).
	listed bool
}

// listedModel is a model the accounts' lists name; unlistedModel one they do not.
func listedModel(name string) forModel   { return forModel{name: name, listed: true} }
func unlistedModel(name string) forModel { return forModel{name: name} }

// namesModels are the runtimes whose requests name their model (issue #814).
var namesModels = map[string]bool{codexRuntime: true, antigravityRuntime: true, openCodeZenRuntime: true}

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

	model := call.upstream.Model

	resp, err := s.underModelAccounts(ctx, codexRuntime, listedModel(model), built.CacheKey, nil,
		s.codexModelReset(model),
		func(token AccountToken) (*http.Response, error) {
			if resting := s.codexResting(token, model); resting != nil {
				return resting, nil
			}

			resp, err := s.codexCall(ctx, &token, listedModel(model), built)
			if err != nil {
				return nil, err
			}

			if resp.StatusCode != http.StatusOK {
				return s.codexRefused(ctx, token, model, resp), nil
			}

			s.accountState.served(token.AccountID)

			return s.codexAnswer(ctx, call, token, built, resp), nil
		})
	if err != nil {
		return nil, fmt.Errorf("provider %q: %w", call.upstream.Provider, err)
	}

	return resp, nil
}

// codexCall sends a built request for model under an account. A token the
// backend refuses is refreshed once and the request sent again; token is then
// the new one.
func (s *Server) codexCall(
	ctx context.Context, token *AccountToken, model forModel, built codex.Request,
) (*http.Response, error) {
	resp, err := s.codexPost(ctx, *token, built)
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}

	return s.retryRefusedAccount(ctx, resp, token, model, true, func(fresh AccountToken) (*http.Response, error) {
		return s.codexPost(ctx, fresh, built)
	})
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

	s.noteQuota(token, resp.Header, codexQuotaLeft)
	s.accounts.NoteAnswer(codexRuntime, token.AccountID, resp.Header)

	return resp, nil
}

// codexRefused passes on a request for model the backend refused: its status
// and its body, as 9router-go does. The account, or that model of it, cools
// down for as long as the kind of failure calls for (codexCool).
func (s *Server) codexRefused(
	ctx context.Context, token AccountToken, model string, resp *http.Response,
) *http.Response {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBytes))
	_ = resp.Body.Close()

	failure := codex.ParseFailure(resp.StatusCode, body)
	s.codexCool(ctx, token, model, failure)

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

// codexCool keeps an account out of use after the backend refused it a request
// for model, as long as the failure calls for (accounts.go), and names the
// refusal (issue #1147). A lack of capacity for the model (an overload) rests
// that model of the account only, as OmniRoute locks only the exact model on a
// Codex server_is_overloaded (src/sse/services/auth.ts:3460-3482); a usage
// limit, a rate limit or a refused token cools the whole account as before.
// The model rests for the delay the backend names, or the growing backoff, at
// most maxModelRest (issue #1159).
func (s *Server) codexCool(ctx context.Context, token AccountToken, model string, failure *codex.Failure) {
	if model == "" || !failure.ModelCapacity() {
		s.cool(ctx, codexRuntime, token, failure.Status, failure.UsageLimit(), failure.ResetsAt, codexCode(failure),
			failure.Message)

		return
	}

	until, ok := s.coolingUntil(token, failure.Status, false, failure.ResetsAt, codexCode(failure))
	if !ok {
		return
	}

	if latest := s.svc.clock.Now().Add(maxModelRest); until.After(latest) {
		until = latest
	}

	s.accountState.rest(token.AccountID, model, until)
	s.logger.WarnContext(ctx, "a model of an account rests",
		slog.String("event", "modelgateway.model_rests"), slog.String("account_id", token.AccountID),
		slog.String("model", model), slog.Time("until", until), slog.String("reason", failure.Reason()))
}

// codexResting is the answer of an account whose model rests (codexCool): a
// 429 the backend is not asked for, so the request goes on to the next account,
// as an Antigravity model that waits answers (antigravityAttempt); nil when the
// model does not rest.
func (s *Server) codexResting(token AccountToken, model string) *http.Response {
	now := s.svc.clock.Now()

	until := s.accountState.restsUntil(token.AccountID, model, now)
	if until.IsZero() {
		return nil
	}

	return openAIError(http.StatusTooManyRequests, "rate_limit_error", "model_resting",
		fmt.Sprintf("the Codex account rests for model %s after the backend refused it", model), until.Sub(now))
}

// codexModelReset is until when model rests on an account, for FirstFree: a lack of capacity for the model, so an
// overload (codexCool).
func (s *Server) codexModelReset(model string) func(string) Rest {
	return func(account string) Rest {
		until := s.accountState.restsUntil(account, model, s.svc.clock.Now())
		if until.IsZero() {
			return Rest{}
		}

		return Rest{Until: until, Code: CoolingOverloaded}
	}
}

// codexCode is why a Codex refusal rests the account (classifyFailure): its type and code, then its status and its
// words.
func codexCode(failure *codex.Failure) CoolingCode {
	return classifyFailure(failure.Status, []string{failure.Type, failure.Code}, failure.Message)
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

			s.codexCool(ctx, token, call.upstream.Model, failure)
			// Read only when the refusal is the answer, before any output.
			noteStreamRefusal(ctx, failure.Text())

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

// accountsCoolingMessage is what the gateway says when every account of a
// runtime waits for a limit to reset.
const accountsCoolingMessage = "no account of this provider can be used for now"

// codexWaitsUpTo is the longest wait for a free Codex account a client is
// left to sit out itself, after the Retry-After of a plain 429; a longer one
// is a usage limit. It is OpenClaw's maxRetryDelayMs,
// past which a 429 fails over at once (src/llm/provider-rate-limit.ts:99-114,
// 270).
const codexWaitsUpTo = time.Minute

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
