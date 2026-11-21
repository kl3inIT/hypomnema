package modelgateway

import (
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"sync"
	"time"

	"github.com/kl3inIT/hypomnema/internal/shared/contextwindow"
)

// A prompt that cannot fit its model's context window is refused before it is
// sent, and a run of them ends the client's turn (issue #1052). On 2026-11-19
// four Codex reviewers resent prompts of 1.0M-1.75M estimated tokens to a
// model with a 500,000-token window, 70-176 times each: Codex answers a
// context-window refusal while compacting by dropping its oldest item and
// asking again (codex-rs/core/src/compact.rs:359-368).
//
// The guard follows OmniRoute, whose check is on unless an operator turns it
// off (src/shared/utils/featureFlags.ts:95-104): the prompt's tokens are its
// characters over four, an inline image or file counted as 1,200 tokens
// (open-sse/services/contextManager.ts:79, 112-119, 483-490), and one over the
// window is refused with 400 context_length_exceeded
// (open-sse/handlers/chatCore.ts:2216-2224, its message from
// chatCore/finalOutputBudget.ts:27-37). The gateway counts bytes, as its
// estimate of a request's size does (Tokens.estimate). Codex reads that
// refusal as its ContextWindowExceeded (codex-rs/codex-api/src/api_bridge.rs:
// 144-146, error.rs:141).
//
// The run of refusals ends as LiteLLM's router cools a deployment: more than
// DEFAULT_ALLOWED_FAILS (3) failures (litellm/constants.py:162;
// router_utils/cooldown_handlers.py:800-803), for DEFAULT_COOLDOWN_TIME_SECONDS
// (5; constants.py:164). Counted are the gateway's own refusals and the
// provider's overflow answers on one key and model; an answer that ends well
// clears them. Past the limit the refusal is a 400 whose code Codex does not
// read as an overflow, which it never retries (protocol/src/error.rs:492-521),
// so its turn ends; its message names the context window, for the node to
// tell why (internal/shared/contextwindow).

const (
	// mediaTokens is what an inline image or file counts for (OmniRoute's IMAGE_TOKEN_ESTIMATE).
	mediaTokens = 1200
	// allowedOverflows is LiteLLM's DEFAULT_ALLOWED_FAILS; overflowCooldown its DEFAULT_COOLDOWN_TIME_SECONDS.
	allowedOverflows = 3
	overflowCooldown = 5 * time.Second
	// codeContextOverflow ends the client's turn; OpenClaw names this failure context_overflow
	// (src/agents/failover-error.ts:196).
	codeContextOverflow = "context_overflow"
)

// inlineData is a JSON string that is a base64 data URL: an image, a file or a recording sent in the request.
var inlineData = regexp.MustCompile(`"data:[^";,]+;base64,[^"]*"`)

// promptEstimate is a request's prompt in tokens, roughly.
func promptEstimate(raw []byte) int64 {
	media := inlineData.FindAllIndex(raw, -1)
	size := len(raw)

	for _, m := range media {
		size -= m[1] - m[0]
	}

	return int64(size/4) + int64(len(media))*mediaTokens
}

// overflowRun is a key's run of overflows on one model: how many in a row, and when the run went past the limit.
type overflowRun struct {
	count   int
	tripped time.Time
}

// overflows are the runs of overflows the gateway is following, by key and model; they live while it runs.
type overflows struct {
	mu   sync.Mutex
	runs map[[2]string]*overflowRun
}

func newOverflows() *overflows { return &overflows{runs: map[[2]string]*overflowRun{}} }

// ended reports whether a key's requests to a model are refused at now: its run went past the limit less than
// overflowCooldown ago. A run whose cooldown is over starts again.
func (o *overflows) ended(key, model string, now time.Time) bool {
	o.mu.Lock()
	defer o.mu.Unlock()

	run := o.runs[[2]string{key, model}]
	switch {
	case run == nil || run.tripped.IsZero():
		return false
	case now.Before(run.tripped.Add(overflowCooldown)):
		return true
	default:
		delete(o.runs, [2]string{key, model})

		return false
	}
}

// add counts an overflow at now, and reports whether the run is past the limit.
func (o *overflows) add(key, model string, now time.Time) bool {
	o.mu.Lock()
	defer o.mu.Unlock()

	run := o.runs[[2]string{key, model}]
	if run == nil {
		run = &overflowRun{}
		o.runs[[2]string{key, model}] = run
	}

	run.count++
	if run.count > allowedOverflows && run.tripped.IsZero() {
		run.tripped = now
	}

	return !run.tripped.IsZero()
}

// clear ends a run: an answer ended well.
func (o *overflows) clear(key, model string) {
	o.mu.Lock()
	defer o.mu.Unlock()

	delete(o.runs, [2]string{key, model})
}

// refuseOverflow answers a request that must not be sent: its key's run of overflows on the model went past the
// limit, or the prompt does not fit the model's window. It reports whether it answered.
func (s *Server) refuseOverflow(w http.ResponseWriter, r *http.Request, key Key, model Model, raw []byte) bool {
	now := s.svc.clock.Now()
	window := s.contextWindow(r, model)

	if s.overflows.ended(key.ID, model.Name, now) {
		s.endOverflow(w, r, key, model, window)

		return true
	}

	estimate := promptEstimate(raw)
	if window <= 0 || estimate <= window {
		return false
	}

	s.logger.WarnContext(r.Context(), "a prompt does not fit its model's context window",
		slog.String("event", "modelgateway.context_overflow"), slog.String("key_id", key.ID),
		slog.String("model", model.Name), slog.Int64("estimated_tokens", estimate), slog.Int64("window", window))

	if s.overflows.add(key.ID, model.Name, now) {
		s.endOverflow(w, r, key, model, window)

		return true
	}

	writeError(w, http.StatusBadRequest, typeInvalidRequest, "context_length_exceeded", fmt.Sprintf(
		"Input exceeds the context window of %s: estimated %d input tokens, limit %d. "+
			"Reduce the prompt or start a new conversation.", model.Name, estimate, window))

	return true
}

// endOverflow ends the client's turn: it sent more than allowedOverflows prompts in a row that did not fit.
func (s *Server) endOverflow(w http.ResponseWriter, r *http.Request, key Key, model Model, window int64) {
	s.logger.WarnContext(r.Context(), "a client's prompts keep overflowing its model's context window",
		slog.String("event", "modelgateway.context_overflow_ended"), slog.String("key_id", key.ID),
		slog.String("model", model.Name))

	limit := "its context window"
	if window > 0 {
		limit = fmt.Sprintf("its context window of %d tokens", window)
	}

	writeError(w, http.StatusBadRequest, typeInvalidRequest, codeContextOverflow, fmt.Sprintf(
		"More than %d prompts in a row to %s did not fit %s; the gateway sends no more for %s. "+
			"Start a new conversation.", allowedOverflows, model.Name, limit, overflowCooldown))
}

// noteOverflow follows a key's run of overflows once the provider answered: an answer that ended well clears it,
// and one that says the prompt did not fit counts.
func (s *Server) noteOverflow(key Key, model Model, record Usage) {
	switch {
	case record.Outcome == OutcomeOK:
		s.overflows.clear(key.ID, model.Name)
	case (record.Outcome == OutcomeTruncated || record.Outcome == OutcomeUpstreamError) &&
		contextwindow.Exceeded(record.Error):
		s.overflows.add(key.ID, model.Name, s.svc.clock.Now())
	}
}

// contextWindow is the model's window as the gateway gives it to clients, 0 when it knows none.
func (s *Server) contextWindow(r *http.Request, model Model) int64 {
	resolved, err := s.svc.Resolve(r.Context(), model)
	if err != nil || resolved.ContextWindow.Value == nil {
		return 0
	}

	return *resolved.ContextWindow.Value
}
