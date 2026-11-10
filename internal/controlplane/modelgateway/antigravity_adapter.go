package modelgateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/cloudcode"
	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway/antigravity"
)

// The Antigravity subscription (issue #20, step 4): a built-in provider kind
// that serves Gemini, Claude and gpt-oss models from a Google account the
// owner logged in on the control plane. The translation is package
// antigravity's; this file is the call itself and what an account's health
// needs: the project and the IDE's user agent, a limit on requests in
// flight, a model that waits for its quota, and an account Google suspended.
//
// Antigravity's terms name this use as a breach that may end the account;
// the owner chose to build it knowing that. The call sends what the
// references and Google's own client send, with nothing added to pass for the
// IDE beyond its user agent. Followed: Google's Gemini CLI
// (utils/googleQuotaErrors.ts, the kinds of 429), omp and the opencode
// plugin (their waits), 9router-go (the limit of requests in flight,
// internal/connlimit; a suspended account, providers/errorclassify.go).

const (
	antigravityRuntime = "antigravity"
	// antigravityInFlight is how many requests one account runs at once;
	// more are refused at once rather than queued (9router-go).
	antigravityInFlight = 3
	// How long a model of an account waits when the backend does not say:
	// omp's and the opencode plugin's numbers.
	antigravityQuotaWait = 30 * time.Minute
	antigravityRateWait  = 30 * time.Second
	// A wait this long or longer is a quota that is used up, not a rate
	// limit (Google's client, MAX_RETRYABLE_DELAY_SECONDS).
	antigravityLongWait = 5 * time.Minute
	// antigravitySuspended is how long a suspended account is left alone
	// before it is tried again (9router-go).
	antigravitySuspended = 24 * time.Hour
	// antigravityVerify is how long an account Google wants verified is left
	// alone (the opencode plugin).
	antigravityVerify = 10 * time.Minute
)

// antigravitySubscription serves chat from Google's Cloud Code backend.
type antigravitySubscription struct{}

func (antigravitySubscription) declared() bool { return false }

// antigravityState is what the gateway remembers of Antigravity accounts
// while it runs: the models that wait, and the requests in flight. It is in
// memory, as in every reference: a restart forgets it and the backend says it
// again.
type antigravityState struct {
	identity *cloudcode.Identity

	mu       sync.Mutex
	waiting  map[string]time.Time // account and model to when it is free
	inFlight map[string]int       // account to requests running
}

func newAntigravityState(manifestURL string, now func() time.Time) *antigravityState {
	return &antigravityState{
		identity: cloudcode.NewIdentity(manifestURL, now), waiting: map[string]time.Time{}, inFlight: map[string]int{},
	}
}

// enter takes a place among an account's requests in flight for a model, or
// says why not: the model waits (until), or the account is busy.
func (a *antigravityState) enter(account, model string, now time.Time) (until time.Time, ok bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	key := account + "\x00" + model
	if until = a.waiting[key]; until.After(now) {
		return until, false
	}

	delete(a.waiting, key)

	if a.inFlight[account] >= antigravityInFlight {
		return time.Time{}, false
	}

	a.inFlight[account]++

	return time.Time{}, true
}

func (a *antigravityState) leave(account string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.inFlight[account]--; a.inFlight[account] <= 0 {
		delete(a.inFlight, account)
	}
}

func (a *antigravityState) wait(account, model string, until time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if key := account + "\x00" + model; until.After(a.waiting[key]) {
		a.waiting[key] = until
	}
}

func (antigravitySubscription) do(ctx context.Context, s *Server, call chatCall) (*http.Response, error) {
	if s.accounts == nil {
		return nil, fmt.Errorf("%w: provider %q has no accounts to run under", ErrNoAccount, call.upstream.Provider)
	}

	model := call.upstream.Model
	now := s.svc.clock.Now()

	// An account near the end of its quota for this model is used after the others.
	nearQuota := func(token AccountToken) bool { return s.accountState.nearQuota(token.AccountID, model, now) }

	resp, err := s.underAccounts(ctx, antigravityRuntime, nearQuota, func(token AccountToken) (*http.Response, error) {
		return s.antigravityAttempt(ctx, call, token)
	})
	if err != nil {
		return nil, fmt.Errorf("provider %q: %w", call.upstream.Provider, err)
	}

	return resp, nil
}

// antigravityAttempt serves a request under one account.
func (s *Server) antigravityAttempt(ctx context.Context, call chatCall, token AccountToken) (*http.Response, error) {
	now := s.svc.clock.Now()
	model := call.upstream.Model

	until, entered := s.antigravity.enter(token.AccountID, model, now)
	if !entered {
		message, wait := "the Antigravity account is at its limit of requests in flight", time.Second
		if !until.IsZero() {
			message, wait = "the Antigravity account's quota for this model is used up", until.Sub(now)
		}

		return openAIError(http.StatusTooManyRequests, "rate_limit_error", "rate_limit_exceeded", message, wait), nil
	}

	release := sync.OnceFunc(func() { s.antigravity.leave(token.AccountID) })

	built, err := antigravity.BuildRequest(call.req.raw, antigravity.Options{
		Model: model, Project: token.Project, RequestID: "agent-" + cacheKey(s.svc.newID("req")),
	})
	if err != nil {
		release()

		// A request that cannot be translated is the client's to fix: it
		// gets a 400, not a failover.
		return openAIError(http.StatusBadRequest, typeInvalidRequest, "invalid_request", err.Error(), 0), nil
	}

	resp, err := s.antigravityPost(ctx, token, built)
	if err != nil {
		release()

		return nil, err
	}

	// A token the backend refuses is refreshed once, and the request sent again.
	if resp.StatusCode == http.StatusUnauthorized {
		drain(resp)

		if token, err = s.accounts.TokenAfterRefusal(ctx, token); err == nil {
			resp, err = s.antigravityPost(ctx, token, built)
		}

		if err != nil {
			release()

			return nil, err
		}
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBytes))
		_ = resp.Body.Close()

		release()

		return s.antigravityRefused(ctx, token, model, antigravity.ParseFailure(resp.StatusCode, body)), nil
	}

	s.accountState.served(token.AccountID)
	s.readQuota(ctx, token.AccountID)

	stream := antigravity.NewStream(s.svc.newID("chatcmpl"), now.Unix(), call.model, built.Names)

	return s.translated(ctx, call, resp, translation{
		event: stream.Event, finished: stream.Finished, completion: stream.Completion,
		failed:  func() bool { return stream.Failed() != nil },
		release: release,
		refusal: func() *http.Response {
			failure := stream.Failed()
			if failure == nil {
				failure = &antigravity.Failure{
					Status: http.StatusBadGateway, Code: "upstream_truncated", Message: "Antigravity ended its answer early",
				}
			}

			return s.antigravityRefused(ctx, token, model, failure)
		},
	}), nil
}

// antigravityPost sends a built request under an account, with the IDE's
// user agent and no other Google header.
func (s *Server) antigravityPost(
	ctx context.Context, token AccountToken, built antigravity.Request,
) (*http.Response, error) {
	address := s.antigravityURL + "/" + cloudcode.APIVersion + ":streamGenerateContent?alt=sse"

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, address, bytes.NewReader(built.Body))
	if err != nil {
		return nil, fmt.Errorf("building the request to Antigravity: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("User-Agent", s.antigravity.identity.UserAgent(ctx))

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling Antigravity: %w", err)
	}

	return resp, nil
}

// antigravityRefused does what a refusal calls for and gives the client the
// error in OpenAI's shape.
//
//   - A quota that is used up: that model of that account waits until the
//     reset, half an hour when the backend names none.
//   - A rate limit: the model waits as long as the backend asks.
//   - No capacity at Google: nothing waits; the model's next target is tried.
//   - A suspended account, or one to verify: the account is taken out of use
//     and its cooldown says why, with Google's link, for `accounts list`. The
//     answer is a 503, so the model's next target serves the request.
func (s *Server) antigravityRefused(
	ctx context.Context, token AccountToken, model string, f *antigravity.Failure,
) *http.Response {
	now := s.svc.clock.Now()
	wait := f.RetryAfter

	switch {
	case f.Reason == antigravity.ReasonTermsViolation:
		s.antigravityPark(ctx, token, now.Add(antigravitySuspended), "suspended by Google for its terms of service", f)

		return openAIError(http.StatusServiceUnavailable, typeServer, "account_suspended",
			"Google has suspended the Antigravity account", 0)
	case f.Reason == antigravity.ReasonVerification:
		s.antigravityPark(ctx, token, now.Add(antigravityVerify), "Google asks for the account to be verified", f)

		return openAIError(http.StatusServiceUnavailable, typeServer, "account_verification_required",
			"Google asks for the Antigravity account to be verified", 0)
	case f.Reason == antigravity.ReasonNoCapacity:
		// Nothing of the account's is used up: Google has no room for the model.
	case f.Status != http.StatusTooManyRequests:
		// Not a limit of a model: the account cools down as any account
		// does after such a failure (accounts.go).
		s.cool(ctx, antigravityRuntime, token, f.Status, false, time.Time{}, f.Code)
	case f.Reason == antigravity.ReasonQuotaExhausted || wait >= antigravityLongWait:
		if wait <= 0 {
			wait = antigravityQuotaWait
		}

		s.antigravity.wait(token.AccountID, model, now.Add(wait))
	default:
		if wait <= 0 {
			wait = antigravityRateWait
		}

		s.antigravity.wait(token.AccountID, model, now.Add(wait))
	}

	kind := typeServer
	if f.Status == http.StatusTooManyRequests {
		kind = "rate_limit_error"
	} else if f.Status < http.StatusInternalServerError {
		kind = typeInvalidRequest
	}

	return openAIError(f.Status, kind, f.Code, f.Message, wait)
}

// antigravityPark takes an account out of use until a time, with the reason
// the owner will read.
func (s *Server) antigravityPark(
	ctx context.Context, token AccountToken, until time.Time, reason string, f *antigravity.Failure,
) {
	if f.URL != "" {
		reason += ": " + f.URL
	}

	s.logger.ErrorContext(ctx, "an Antigravity account was taken out of use",
		slog.String("event", "modelgateway.antigravity_account_parked"), slog.String("account_id", token.AccountID),
		slog.String("reason", f.Reason), slog.Time("until", until))

	err := s.accounts.CoolDown(context.WithoutCancel(ctx), antigravityRuntime, token.AccountID, until, reason)
	if err != nil {
		s.logger.ErrorContext(ctx, "parking an Antigravity account",
			slog.String("event", "modelgateway.antigravity_park_failed"), slog.String("account_id", token.AccountID),
			slog.String("error", err.Error()))
	}
}

// openAIError is an error as an answer in OpenAI's shape, with the wait the
// client should observe when there is one.
func openAIError(status int, kind, code, message string, retryAfter time.Duration) *http.Response {
	var body errorBody

	body.Error.Message, body.Error.Type, body.Error.Code = message, kind, code

	encoded, _ := json.Marshal(body) //nolint:errchkjson // A struct of strings always encodes.
	resp := &http.Response{
		StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(bytes.NewReader(encoded)),
	}

	if retryAfter > 0 {
		resp.Header.Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())+1))
	}

	return resp
}
