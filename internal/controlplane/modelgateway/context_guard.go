package modelgateway

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"sync"
	"time"

	"github.com/kl3inIT/hypomnema/internal/shared/contextwindow"
)

// A run of answers that did not end ends the client's turn (issue #1052). On
// 2026-11-19 four Codex reviewers resent prompts that did not fit the window,
// 70-176 times each, every answer truncated: Codex answers a context-window
// refusal while compacting by dropping its oldest item and asking again
// (codex-rs/core/src/compact.rs:359-368).
//
// The gateway does not count a prompt's tokens before sending it (the
// owner's choice B, 2026-11-22). LiteLLM checks a prompt against the window
// only when asked to (litellm/router.py:95, enable_pre_call_checks False).
// What counts, whatever the prompt's size, is an answer that did not end: a
// stream cut short, silently or on an error the provider sent (one Codex
// retries on its own up to five times, codex-rs/codex-api/src/sse/
// responses_error.rs:73-91, model-provider-info/src/lib.rs:74), a client gone
// before anything came, or the provider saying the prompt did not fit
// (internal/shared/contextwindow). A stream the provider ended because a
// parameter was invalid, such as a tool's input too long, does not count: the
// request is at fault, not its size or the provider, as OmniRoute sets
// invalid_value and invalid_type apart (open-sse/services/accountFallback.ts:
// 379); counting it would end the turn as an overflow (review of PR #1057).
// An answer that ends well clears the run.
//
// The run ends as LiteLLM's router cools a deployment: more than
// DEFAULT_ALLOWED_FAILS (3) failures (litellm/constants.py:162;
// router_utils/cooldown_handlers.py:800-803), and for
// DEFAULT_COOLDOWN_TIME_SECONDS (5; constants.py:164) a request for it is
// refused without being sent (router_utils/handle_error.py:23-55); then the
// run starts again. LiteLLM's count lives for the cooldown after a failure
// (cooldown_handlers.py:795-802). Here the cooldown is counted from an
// answer to the key's next request, and a run with a request in flight is
// kept: one of the 2026-11-19 answers took 7-19 s, longer than the window,
// and was followed at once by the next. A run with nothing in flight 5 s
// after its last answer is forgotten, so a revoked Dispatch key leaves
// nothing behind.
//
// The refusal is a 400 whose code Codex does not read as an overflow, which
// it never retries (protocol/src/error.rs:492-521;
// codex-api/src/api_bridge.rs:192-225), so its turn ends instead of trimming
// again; its message names the context window, so the node reports the
// failure as an overflow and the next Dispatch starts a new Conversation.

const (
	// allowedOverflows is LiteLLM's DEFAULT_ALLOWED_FAILS; overflowCooldown its DEFAULT_COOLDOWN_TIME_SECONDS.
	allowedOverflows = 3
	overflowCooldown = 5 * time.Second
	// codeContextOverflow ends the client's turn; OpenClaw names this failure context_overflow
	// (src/agents/failover-error.ts:196).
	codeContextOverflow = "context_overflow"
)

// parameterError is a provider's error that a parameter of the request was invalid.
var parameterError = regexp.MustCompile(`\b(?:invalid_value|invalid_type)\b`)

// answer is how an answer counts toward a run.
type answer int

const (
	// answerOther neither counts nor clears: a refusal the provider explained, a client gone mid-answer.
	answerOther answer = iota
	// answerOK ended well and clears the run.
	answerOK
	// answerFailed did not end: cut short, empty, or the prompt did not fit.
	answerFailed
)

// overflowRun is a key's run of failed answers on one model: how many in a row, the last one, when the run went
// past the limit, and the requests in flight.
type overflowRun struct {
	count         int
	last, tripped time.Time
	inFlight      int
}

// overflows are the runs the gateway is following, by key and model; they live while it runs.
type overflows struct {
	mu   sync.Mutex
	runs map[[2]string]*overflowRun
}

func newOverflows() *overflows { return &overflows{runs: map[[2]string]*overflowRun{}} }

// forget drops the runs that are over at now: past the limit for the cooldown, or with nothing in flight for the
// cooldown after their last answer. The caller holds mu.
func (o *overflows) forget(now time.Time) {
	for k, run := range o.runs {
		cooled := !run.tripped.IsZero() && !now.Before(run.tripped.Add(overflowCooldown))
		idle := run.inFlight == 0 && !now.Before(run.last.Add(overflowCooldown))

		if cooled || idle {
			delete(o.runs, k)
		}
	}
}

// begin is a key's request to a model arriving at now. refused says its run is past the limit; else run, when not
// nil, is the run the request is in flight in, to hand to end.
func (o *overflows) begin(key, model string, now time.Time) (refused bool, run *overflowRun) {
	o.mu.Lock()
	defer o.mu.Unlock()

	o.forget(now)

	run = o.runs[[2]string{key, model}]
	switch {
	case run == nil:
		return false, nil
	case !run.tripped.IsZero():
		return true, nil
	default:
		run.inFlight++

		return false, run
	}
}

// end is the answer to a request begin let through, at now.
func (o *overflows) end(key, model string, now time.Time, began *overflowRun, kind answer) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if began != nil && began.inFlight > 0 {
		began.inFlight--
	}

	k := [2]string{key, model}
	run := o.runs[k]

	switch kind {
	case answerOK:
		delete(o.runs, k)
	case answerFailed:
		if run == nil {
			run = &overflowRun{}
			o.runs[k] = run
		}

		run.count++
		run.last = now

		if run.count > allowedOverflows && run.tripped.IsZero() {
			run.tripped = now
		}
	case answerOther:
		if run != nil {
			run.last = now
		}
	}
}

// refuseOverflow answers a request of a key whose run on the model went past the limit, and reports whether it did;
// else it returns the run the request is in flight in, for noteOverflow.
func (s *Server) refuseOverflow(w http.ResponseWriter, r *http.Request, key Key, model Model) (bool, *overflowRun) {
	refused, run := s.overflows.begin(key.ID, model.Name, s.svc.clock.Now())
	if !refused {
		return false, run
	}

	s.logger.WarnContext(r.Context(), "a client's answers keep ending early",
		slog.String("event", "modelgateway.context_overflow_ended"), slog.String("key_id", key.ID),
		slog.String("model", model.Name))

	writeError(w, http.StatusBadRequest, typeInvalidRequest, codeContextOverflow, fmt.Sprintf(
		"The last %d answers of %s to this key did not end: cut short, empty, or the prompt exceeds the context "+
			"window; for %s the gateway sends no more of this key's. Start a new conversation.",
		allowedOverflows+1, model.Name, overflowCooldown))

	return true, nil
}

// noteOverflow follows a key's run once the provider answered.
func (s *Server) noteOverflow(ctx context.Context, key Key, model Model, run *overflowRun, record Usage) {
	kind := answerOther

	switch {
	case record.Outcome == OutcomeOK:
		kind = answerOK
	case record.Outcome == OutcomeTruncated && !parameterError.MatchString(record.Error),
		record.Outcome == OutcomeClientGone && record.TTFT == nil,
		(record.Outcome == OutcomeTruncated || record.Outcome == OutcomeUpstreamError) &&
			contextwindow.Exceeded(record.Error):
		kind = answerFailed

		s.logger.WarnContext(ctx, "an answer did not end",
			slog.String("event", "modelgateway.answer_failed"), slog.String("key_id", key.ID),
			slog.String("model", model.Name), slog.String("outcome", string(record.Outcome)),
			slog.String("error", record.Error))
	}

	s.overflows.end(key.ID, model.Name, s.svc.clock.Now(), run, kind)
}
