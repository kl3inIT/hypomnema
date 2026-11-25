package modelgateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Several accounts of one built-in provider (issue #20, §6). Nothing here is
// the gateway's own invention; each rule names where it is learned from
// (docs/research/2026-11-10-several-accounts.md):
//
//   - accounts are taken in their order, the first that can serve until it
//     fails: 9router-go's and 9router's default ("fill-first"), and how
//     Hypomnema places workers;
//   - inside one request the next account is tried on 401, 402, 403, 429,
//     502, 503 and 504, each account once, and only while nothing was sent to
//     the client: 9router-go handlers/chat/fallback.go, CLIProxyAPI the same;
//   - a failure cools the account down for a time that depends on its kind:
//     9router-go providers/errorclassify.go, with 9router's same numbers;
//   - an account near the end of its quota is used after the others, not
//     refused: omp's reserve (packages/ai/src/auth/rank.ts), at the 90 % of
//     opencode-antigravity-auth (src/plugin/accounts.ts, isOverSoftQuota).

const (
	// The wait after a rate limit or an overload: 2 s, doubling with each
	// failure in a row, at most 5 minutes (9router-go: BaseMs, MaxMs).
	backoffFirst = 2 * time.Second
	backoffMax   = 5 * time.Minute
	// backoffLevels is past where the wait stops growing.
	backoffLevels = 15
	// quotaCooldown is for a quota that is used up when the provider names
	// no reset (9router-go: QuotaExhaustedCooldownMs).
	quotaCooldown = time.Hour
	// refusedCooldown is for 401, 402 and 403 (9router-go: 2 minutes).
	refusedCooldown = 2 * time.Minute
	// transientCooldown is for a 5xx that is no overload (9router-go:
	// TransientCooldownMs).
	transientCooldown = 30 * time.Second
	// maxProviderReset bounds a reset time the provider names (9router-go:
	// maxResetCooldown).
	maxProviderReset = 2 * time.Hour

	// softQuotaLeft is the part of a model's quota below which an account is
	// used last: 90 % used in opencode-antigravity-auth, a 10 % reserve in omp.
	softQuotaLeft = 0.10
	// A quota reading is asked for after a successful request, at most this
	// often, and trusted this long; an account with none is used
	// (opencode-antigravity-auth: quota_refresh_interval_minutes and its cache).
	quotaEvery   = 15 * time.Minute
	quotaTrusted = 30 * time.Minute
)

// movesOn reports whether an answer of one account sends the request to the
// next: the statuses 9router-go retries on another account.
func movesOn(status int) bool {
	switch status {
	case http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden, http.StatusTooManyRequests,
		http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func (s *Server) retryRefusedAccount(
	ctx context.Context, resp *http.Response, token *AccountToken, model forModel, keepRefusalOnRefreshError bool,
	attempt func(AccountToken) (*http.Response, error),
) (*http.Response, error) {
	fresh, err := s.tokenAfterRefusal(ctx, *token, model)
	// An account whose list no longer has the model keeps its refusal, so the next account is tried (issue #814).
	if err != nil && ctx.Err() == nil && (errors.Is(err, ErrTokenUnavailable) || errors.Is(err, ErrModelNotServed) ||
		keepRefusalOnRefreshError && !errors.Is(err, ErrAccountResting)) {
		return resp, nil
	}

	drain(resp)
	if err != nil {
		return nil, err
	}

	*token = fresh
	resp, err = attempt(fresh)
	if err == nil && resp.StatusCode == http.StatusUnauthorized {
		s.markTokenRefused(ctx, fresh)
	}
	return resp, err
}

func (s *Server) markTokenRefused(ctx context.Context, token AccountToken) {
	if ctx.Err() == nil {
		if err := s.accounts.MarkTokenRefused(ctx, token); err != nil {
			s.logger.WarnContext(ctx, "marking a refused account",
				slog.String("account_id", token.AccountID), slog.Any("error", err))
		}
	}
}

// accountState is what the gateway remembers of accounts while it runs: how
// many times in a row each was limited, the last reading of its quota, and
// the models each rests for. It is in memory, as the references keep theirs.
type accountState struct {
	mu     sync.Mutex
	levels map[string]int
	quotas map[string]quotaReading
	// rests are the models an account rests for while it serves its others
	// (issue #1147), by account and model, to when.
	rests map[string]time.Time
}

type quotaReading struct {
	at time.Time
	// left is the part of each model's quota that remains, from 0 to 1.
	left map[string]float64
	// asking says a reading is on its way.
	asking bool
}

func newAccountState() *accountState {
	return &accountState{levels: map[string]int{}, quotas: map[string]quotaReading{}, rests: map[string]time.Time{}}
}

// rest keeps one model of an account out of use until a time, the account
// serving its other models meanwhile (issue #1147): CLIProxyAPI's state of a
// model on a credential (sdk/cliproxy/auth/types.go:396 ModelState, skipped by
// the selector for that model only, selector.go:727) and OmniRoute's lock of
// the exact model on a connection (open-sse/services/accountFallback/
// exactModelLock.ts lockExactModel). A later rest only lengthens it.
func (a *accountState) rest(account, model string, until time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if key := account + "\x00" + model; until.After(a.rests[key]) {
		a.rests[key] = until
	}
}

// restsUntil is until when a model of an account rests; zero or past when it does not.
func (a *accountState) restsUntil(account, model string, now time.Time) time.Time {
	a.mu.Lock()
	defer a.mu.Unlock()

	key := account + "\x00" + model

	until := a.rests[key]
	if !until.After(now) {
		delete(a.rests, key)

		return time.Time{}
	}

	return until
}

// underAccounts serves a request under the accounts of a runtime in their
// order, those that serve model when it is given (Subscriptions.Token).
// attempt runs it under one account and answers in OpenAI's shape; an
// answer that moves on is kept while the next account is tried, and is what
// the client gets when none serves. usedLast, when given, says an account is
// to be tried only after the others.
func (s *Server) underAccounts(
	ctx context.Context, runtime string, model forModel, session string, usedLast func(AccountToken) bool,
	attempt func(AccountToken) (*http.Response, error),
) (*http.Response, error) {
	resp, err := s.tryAccounts(ctx, runtime, model, session, usedLast, attempt, nil)
	if errors.Is(err, ErrModelNotServed) {
		return modelNotServed(model.name, runtime), nil
	}
	var cooling *CoolingError
	if errors.As(err, &cooling) {
		if now := s.svc.clock.Now(); runtime == codexRuntime && cooling.Until.Sub(now) > codexWaitsUpTo {
			return accountCoolingResponse("/v1/responses", cooling.Until, now), nil
		}
		// 429 with the time the first account is free again, as CLIProxyAPI
		// answers when every credential cools (9router answers 503).
		return openAIError(http.StatusTooManyRequests, "rate_limit_error", "account_unavailable",
			accountsCoolingMessage, cooling.Until.Sub(s.svc.clock.Now())), nil
	}
	return resp, err
}

// underModelAccounts is underAccounts for a request that may move on to the
// model's next target: a CoolingError, or ErrModelNotServed when no account's
// list has the model, is returned as it is, for the caller to try the next
// target or answer for them all (tryChat).
func (s *Server) underModelAccounts(
	ctx context.Context, runtime string, model forModel, session string, usedLast func(AccountToken) bool,
	modelReset func(string) time.Time, attempt func(AccountToken) (*http.Response, error),
) (*http.Response, error) {
	return s.tryAccounts(ctx, runtime, model, session, usedLast, attempt, func() time.Time {
		until, err := s.accounts.FirstFree(ctx, runtime, modelReset)
		if err != nil {
			return time.Time{}
		}
		return until
	})
}

func (s *Server) tryAccounts(
	ctx context.Context, runtime string, model forModel, session string, usedLast func(AccountToken) bool,
	attempt func(AccountToken) (*http.Response, error), firstFree func() time.Time,
) (*http.Response, error) {
	if namesModels[runtime] && model.name == "" {
		return nil, fmt.Errorf("%w: %s", ErrNoModel, runtime)
	}

	var (
		tried    []string
		spared   []AccountToken
		refusal  *http.Response
		stopping error
	)

	try := func(token AccountToken) (*http.Response, bool, error) {
		noteServed(ctx, token.AccountID)

		resp, err := attempt(token)
		if err != nil || !movesOn(resp.StatusCode) {
			return resp, true, err
		}

		if refusal != nil {
			_ = refusal.Body.Close()
		}

		refusal = resp

		return nil, false, nil
	}

	for {
		token, err := s.token(ctx, runtime, model, session, tried...)
		if err != nil {
			stopping = err

			break
		}

		tried = append(tried, token.AccountID)

		if usedLast != nil && usedLast(token) {
			spared = append(spared, token)

			continue
		}

		if resp, done, err := try(token); done {
			return resp, err
		}
	}

	for _, held := range spared {
		if !s.stillServes(ctx, model, held) {
			continue
		}

		if resp, done, err := try(held); done {
			return resp, err
		}
	}

	// The last pick found that no account's list has the model now, the accounts already tried included: an answer
	// one of them gave before it lost the model (a 401 whose refresh found it gone) says nothing more. The request is
	// answered as at the start: the model is not served when every list is known, and to be tried again while one is
	// not (issue #814).
	var cooling *CoolingError
	if errors.Is(stopping, ErrModelNotServed) || errors.As(stopping, &cooling) && cooling.ListUnknown {
		if refusal != nil {
			drain(refusal)
		}

		return nil, stopping
	}

	if firstFree != nil && (refusal != nil && refusal.StatusCode == http.StatusTooManyRequests ||
		refusal == nil && errors.As(stopping, &cooling)) {
		if until := firstFree(); !until.IsZero() {
			if refusal != nil {
				drain(refusal)
			}
			return nil, &CoolingError{Until: until}
		}
	}
	if refusal != nil {
		return refusal, nil
	}

	return nil, stopping
}

// modelOf is the model a request names: the gateway's own (Service.GetModel), or, when the accounts' lists route
// their models (ModelTokens), a Codex or Antigravity model the provider no longer publishes, for those lists to
// decide (Service.unlistedModel, issue #814). Such a model below the provider's version floor is ErrModelNotServed.
func (s *Server) modelOf(ctx context.Context, name string) (Model, error) {
	model, err := s.svc.GetModel(ctx, name)
	if !errors.Is(err, ErrNotFound) {
		return model, err
	}

	if _, ok := s.accounts.(ModelTokens); ok {
		unlisted, ok, err := s.svc.unlistedModel(ctx, name)
		if err != nil {
			return Model{}, err
		}

		if ok {
			return unlisted, nil
		}
	}

	return model, err
}

// modelNotServed is the answer when every account's list is known and none
// has the model (issue #814).
func modelNotServed(model, runtime string) *http.Response {
	return openAIError(http.StatusNotFound, typeInvalidRequest, codeModelNotServed,
		fmt.Sprintf("model %q is not served by any %s account", model, runtime), 0)
}

// token is a token of an account of a runtime, of one whose vendor lists
// model when the accounts can say (ModelTokens) and a model is given.
func (s *Server) token(
	ctx context.Context, runtime string, model forModel, session string, skip ...string,
) (AccountToken, error) {
	if byModel, ok := s.accounts.(ModelTokens); ok && model.listed {
		return byModel.TokenFor(ctx, runtime, model.name, session, skip...)
	}

	return s.accounts.Token(ctx, runtime, session, skip...)
}

// stillServes says a held account may still be sent to, right before it is,
// for a model the accounts' lists name: it may have stopped serving the
// model, or being usable, while others were tried (issue #814). The held token
// is sent as it is, so anything given with it (a retry it claimed) stays.
func (s *Server) stillServes(ctx context.Context, model forModel, held AccountToken) bool {
	byModel, ok := s.accounts.(ModelTokens)
	if !ok || !model.listed {
		return true
	}

	return byModel.Serves(ctx, held.AccountID, model.name)
}

// tokenAfterRefusal refreshes an account whose token the vendor refused. For a
// model the accounts' lists name, it is refreshed only while it is still
// usable and its list still has the model (ModelTokens; issue #814): otherwise
// the error is ErrModelNotServed, and the request goes on to another account.
func (s *Server) tokenAfterRefusal(ctx context.Context, refused AccountToken, model forModel) (AccountToken, error) {
	if byModel, ok := s.accounts.(ModelTokens); ok && model.listed {
		return byModel.TokenAfterRefusalFor(ctx, refused, model.name)
	}

	return s.accounts.TokenAfterRefusal(ctx, refused)
}

// cool keeps an account out of use after a failure, for as long as 9router-go
// does: until the reset the provider names (at most two hours); an hour for
// a quota that is used up; a growing wait after a rate limit or an overload;
// two minutes after a refusal; thirty seconds after another server error.
func (s *Server) cool(
	ctx context.Context, runtime string, token AccountToken, status int, quotaUsedUp bool, reset time.Time, reason string,
) {
	if until, ok := s.coolingUntil(token, status, quotaUsedUp, reset); ok {
		s.park(ctx, runtime, token, until, reason)
	}
}

// coolingUntil is until when a failure keeps an account, or one model of it, out of use (cool); false when the
// failure costs it nothing.
func (s *Server) coolingUntil(
	token AccountToken, status int, quotaUsedUp bool, reset time.Time,
) (time.Time, bool) {
	now := s.svc.clock.Now()

	var wait time.Duration

	switch {
	case reset.After(now) && reset.Sub(now) <= maxProviderReset:
		// The provider's own moment, kept as it was given.
		return reset, true
	case reset.After(now):
		wait = maxProviderReset
	case quotaUsedUp:
		wait = quotaCooldown
	case status == http.StatusTooManyRequests || status == http.StatusBadGateway ||
		status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout:
		wait = s.accountState.nextBackoff(token.AccountID)
	case status == http.StatusUnauthorized || status == http.StatusPaymentRequired || status == http.StatusForbidden:
		wait = refusedCooldown
	case status >= http.StatusInternalServerError:
		wait = transientCooldown
	default:
		return time.Time{}, false
	}

	return now.Add(wait), true
}

func (s *Server) park(ctx context.Context, runtime string, token AccountToken, until time.Time, reason string) {
	err := s.accounts.CoolDown(context.WithoutCancel(ctx), runtime, token.AccountID, until, reason)
	if err != nil {
		s.logger.ErrorContext(ctx, "cooling an account down",
			slog.String("event", "modelgateway.cooldown_failed"), slog.String("account_id", token.AccountID),
			slog.String("error", err.Error()))
	}
}

// nextBackoff counts one more failure in a row and returns the wait for it.
func (a *accountState) nextBackoff(account string) time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()

	level := min(a.levels[account]+1, backoffLevels)
	a.levels[account] = level

	return min(backoffFirst<<(level-1), backoffMax)
}

// served says an account answered: its failures in a row are forgotten.
func (a *accountState) served(account string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	delete(a.levels, account)
}

// An account's whole quota, for a vendor that says it on every answer: the
// gateway reads it there, asking nothing more, and Token keeps an account
// that is nearly used up for last (subscription.Service.Token). What is left
// is one minus the most used of the vendor's windows, as omp measures it
// (packages/ai/src/auth/usage-report.ts remainingUsageFraction).

// claudeQuotaLeft reads Anthropic's unified windows, 5 hours and 7 days, each
// a used part from 0 to 1 (omp packages/ai/src/usage/claude.ts
// parseClaudeRateLimitHeaders). The window of one model family is left out:
// omp trusts it only once it is used up.
func claudeQuotaLeft(h http.Header) (float64, bool) {
	return quotaLeft(1,
		h.Get("Anthropic-Ratelimit-Unified-5h-Utilization"), h.Get("Anthropic-Ratelimit-Unified-7d-Utilization"))
}

// codexQuotaLeft reads the Codex backend's primary and secondary windows, each
// a used percentage (omp packages/ai/src/usage/openai-codex.ts
// parseCodexRateLimitHeaders).
func codexQuotaLeft(h http.Header) (float64, bool) {
	return quotaLeft(100, h.Get("X-Codex-Primary-Used-Percent"), h.Get("X-Codex-Secondary-Used-Percent"))
}

// quotaLeft is what remains under the most used of the windows, whose used
// part is written out of full; false when the vendor named none.
func quotaLeft(full float64, used ...string) (float64, bool) {
	most, known := 0.0, false

	for _, text := range used {
		part, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
		if err != nil || math.IsNaN(part) || math.IsInf(part, 0) {
			continue
		}

		most, known = max(most, part/full), true
	}

	return min(max(1-most, 0), 1), known
}

// noteQuota passes what an answer's headers say of an account's quota on to
// its accounts; read is the vendor's way of saying it.
func (s *Server) noteQuota(token AccountToken, h http.Header, read func(http.Header) (float64, bool)) {
	if left, known := read(h); known {
		s.accounts.NoteQuota(token.AccountID, left)
	}
}

// nearQuota reports whether an account's quota for a model is known, fresh,
// and nearly used up.
func (a *accountState) nearQuota(account, model string, now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	reading := a.quotas[account]
	left, known := reading.left[model]

	return known && now.Sub(reading.at) < quotaTrusted && left <= softQuotaLeft
}

// readQuota asks the vendor for an account's quota when the last reading is
// old enough, without making the caller wait.
func (s *Server) readQuota(ctx context.Context, account string) {
	now := s.svc.clock.Now()
	state := s.accountState

	state.mu.Lock()

	reading := state.quotas[account]
	if reading.asking || (!reading.at.IsZero() && now.Sub(reading.at) < quotaEvery) {
		state.mu.Unlock()

		return
	}

	reading.asking = true
	state.quotas[account] = reading
	state.mu.Unlock()

	go func() {
		left, err := s.accounts.Quota(context.WithoutCancel(ctx), account)

		state.mu.Lock()
		defer state.mu.Unlock()

		if err != nil {
			// The last reading stays until it is too old to trust.
			reading.asking = false
			state.quotas[account] = reading

			return
		}

		state.quotas[account] = quotaReading{at: now, left: left}
	}()
}
