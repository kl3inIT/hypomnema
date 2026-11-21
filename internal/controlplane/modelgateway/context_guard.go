package modelgateway

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/kl3inIT/hypomnema/internal/shared/contextwindow"
)

// A run of answers saying the prompt did not fit its model's context window
// ends the client's turn (issue #1052). On 2026-11-19 four Codex reviewers
// resent prompts that did not fit the window, 70-176 times each: Codex
// answers a context-window refusal while compacting by dropping its oldest
// item and asking again (codex-rs/core/src/compact.rs:359-368).
//
// The gateway does not count a prompt's tokens before sending it. LiteLLM
// checks a prompt against the window only when asked to
// (litellm/router.py:95, enable_pre_call_checks False), and then with the
// model's tokenizer (router_rust/steps/precall.py:490-492); an estimate from
// the request's bytes refused prompts that fit (review of PR #1057). What
// counts is the provider's own answer, its error read as an overflow by
// internal/shared/contextwindow.
//
// The run ends as LiteLLM's router cools a deployment: more than
// DEFAULT_ALLOWED_FAILS (3) failures (litellm/constants.py:162;
// router_utils/cooldown_handlers.py:800-803), and for
// DEFAULT_COOLDOWN_TIME_SECONDS (5; constants.py:164) a request for it is
// refused without being sent (router_utils/handle_error.py:23-55). An answer
// that ends well clears the run. The refusal is a 400 whose code Codex does
// not read as an overflow, which it never retries (protocol/src/error.rs:
// 492-521; codex-api/src/api_bridge.rs:192-225), so its turn ends instead of
// trimming again; its message names the context window, for the node to tell
// why.

const (
	// allowedOverflows is LiteLLM's DEFAULT_ALLOWED_FAILS; overflowCooldown its DEFAULT_COOLDOWN_TIME_SECONDS.
	allowedOverflows = 3
	overflowCooldown = 5 * time.Second
	// codeContextOverflow ends the client's turn; OpenClaw names this failure context_overflow
	// (src/agents/failover-error.ts:196).
	codeContextOverflow = "context_overflow"
)

// overflowRun is a key's run of overflows on one model: how many in a row, the last one, and when the run went past
// the limit.
type overflowRun struct {
	count         int
	last, tripped time.Time
}

// overflows are the runs of overflows the gateway is following, by key and model; they live while it runs.
type overflows struct {
	mu   sync.Mutex
	runs map[[2]string]*overflowRun
}

func newOverflows() *overflows { return &overflows{runs: map[[2]string]*overflowRun{}} }

// forget drops the runs that are over at now: overflowCooldown after the last overflow, as LiteLLM's count of
// failures lives for the cooldown (router_utils/cooldown_handlers.py:800-802), and overflowCooldown after the run
// went past the limit, as LiteLLM's cooldown ends. A Dispatch's key is revoked when it ends and never comes back, so
// only recent runs are kept. The caller holds mu.
func (o *overflows) forget(now time.Time) {
	for k, run := range o.runs {
		if !now.Before(run.last.Add(overflowCooldown)) ||
			(!run.tripped.IsZero() && !now.Before(run.tripped.Add(overflowCooldown))) {
			delete(o.runs, k)
		}
	}
}

// ended reports whether a key's requests to a model are refused at now: its run went past the limit.
func (o *overflows) ended(key, model string, now time.Time) bool {
	o.mu.Lock()
	defer o.mu.Unlock()

	o.forget(now)
	run := o.runs[[2]string{key, model}]

	return run != nil && !run.tripped.IsZero()
}

// add counts an overflow at now.
func (o *overflows) add(key, model string, now time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()

	o.forget(now)

	run := o.runs[[2]string{key, model}]
	if run == nil {
		run = &overflowRun{}
		o.runs[[2]string{key, model}] = run
	}

	run.count++
	run.last = now

	if run.count > allowedOverflows && run.tripped.IsZero() {
		run.tripped = now
	}
}

// clear ends a run: an answer ended well.
func (o *overflows) clear(key, model string) {
	o.mu.Lock()
	defer o.mu.Unlock()

	delete(o.runs, [2]string{key, model})
}

// refuseOverflow answers a request of a key whose run of overflows on the model went past the limit, and reports
// whether it did.
func (s *Server) refuseOverflow(w http.ResponseWriter, r *http.Request, key Key, model Model) bool {
	if !s.overflows.ended(key.ID, model.Name, s.svc.clock.Now()) {
		return false
	}

	s.logger.WarnContext(r.Context(), "a client's prompts keep overflowing its model's context window",
		slog.String("event", "modelgateway.context_overflow_ended"), slog.String("key_id", key.ID),
		slog.String("model", model.Name))

	writeError(w, http.StatusBadRequest, typeInvalidRequest, codeContextOverflow, fmt.Sprintf(
		"Each of the last %d prompts to %s exceeds the context window; for %s the gateway sends no more of "+
			"this key's. Start a new conversation.", allowedOverflows+1, model.Name, overflowCooldown))

	return true
}

// noteOverflow follows a key's run of overflows once the provider answered: an answer that ended well clears it,
// and one that says the prompt did not fit counts.
func (s *Server) noteOverflow(ctx context.Context, key Key, model Model, record Usage) {
	switch {
	case record.Outcome == OutcomeOK:
		s.overflows.clear(key.ID, model.Name)
	case (record.Outcome == OutcomeTruncated || record.Outcome == OutcomeUpstreamError) &&
		contextwindow.Exceeded(record.Error):
		s.logger.WarnContext(ctx, "the provider says a prompt did not fit its model's context window",
			slog.String("event", "modelgateway.context_overflow"), slog.String("key_id", key.ID),
			slog.String("model", model.Name))
		s.overflows.add(key.ID, model.Name, s.svc.clock.Now())
	}
}
