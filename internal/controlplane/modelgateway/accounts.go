package modelgateway

import (
	"context"
	"errors"
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

// accountState is what the gateway remembers of accounts while it runs: how
// many times in a row each was limited, and the last reading of its quota.
// It is in memory, as the references keep theirs.
type accountState struct {
	mu     sync.Mutex
	levels map[string]int
	quotas map[string]quotaReading
}

type quotaReading struct {
	at time.Time
	// left is the part of each model's quota that remains, from 0 to 1.
	left map[string]float64
	// asking says a reading is on its way.
	asking bool
}

func newAccountState() *accountState {
	return &accountState{levels: map[string]int{}, quotas: map[string]quotaReading{}}
}

// underAccounts serves a request under the accounts of a runtime in their
// order. attempt runs it under one account and answers in OpenAI's shape; an
// answer that moves on is kept while the next account is tried, and is what
// the client gets when none serves. usedLast, when given, says an account is
// to be tried only after the others.
func (s *Server) underAccounts(
	ctx context.Context, runtime, session string, usedLast func(AccountToken) bool,
	attempt func(AccountToken) (*http.Response, error),
) (*http.Response, error) {
	var (
		tried    []string
		spared   []AccountToken
		refusal  *http.Response
		stopping error
	)

	try := func(token AccountToken) (*http.Response, bool, error) {
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
		token, err := s.accounts.Token(ctx, runtime, session, tried...)
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

	for _, token := range spared {
		if resp, done, err := try(token); done {
			return resp, err
		}
	}

	if refusal != nil {
		return refusal, nil
	}

	// No account was tried at all.
	var cooling *CoolingError
	if errors.As(stopping, &cooling) {
		// 429 with the time the first account is free again, as CLIProxyAPI
		// answers when every credential cools (9router answers 503).
		return openAIError(http.StatusTooManyRequests, "rate_limit_error", "account_unavailable",
			"no account of this provider can be used for now", cooling.Until.Sub(s.svc.clock.Now())), nil
	}

	return nil, stopping
}

// cool keeps an account out of use after a failure, for as long as 9router-go
// does: until the reset the provider names (at most two hours); an hour for
// a quota that is used up; a growing wait after a rate limit or an overload;
// two minutes after a refusal; thirty seconds after another server error.
func (s *Server) cool(
	ctx context.Context, runtime string, token AccountToken, status int, quotaUsedUp bool, reset time.Time, reason string,
) {
	now := s.svc.clock.Now()

	var wait time.Duration

	switch {
	case reset.After(now) && reset.Sub(now) <= maxProviderReset:
		// The provider's own moment, kept as it was given.
		s.park(ctx, runtime, token, reset, reason)

		return
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
		return
	}

	s.park(ctx, runtime, token, now.Add(wait), reason)
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
