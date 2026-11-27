package orchestration

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gatewayRequest records one request the model gateway served on a Dispatch's key, as the gateway records it:
// 1,000 tokens in (400 from the cache), 100 out, and 2,000 micro-dollars, or no price.
func (w *world) gatewayRequest(id, keyID string, priced bool) {
	w.t.Helper()

	var cost any
	if priced {
		cost = 2000
	}

	_, err := w.db.ExecContext(w.t.Context(), `INSERT INTO gateway_usage
		(id, key_id, model, provider, upstream_model, stream, attempts, outcome, status, prompt_tokens,
		 completion_tokens, cached_tokens, reasoning_tokens, cache_write_tokens, cost_micros, duration_ms, created_at)
		VALUES (?, ?, 'claude-opus-5-5', 'claude-subscription', 'claude-opus-5-5', 1, 1, 'ok', 200, 1000, 100, 400,
		        10, 50, ?, 900, ?)`, id, keyID, cost, w.clock.now.UnixMilli())
	require.NoError(w.t, err)
}

// gatewayKey is a key the gateway made for a Dispatch.
func (w *world) gatewayKey(id, dispatchID string) {
	w.t.Helper()

	_, err := w.db.ExecContext(w.t.Context(), `INSERT INTO gateway_keys (id, name, key_hash, models, dispatch_id,
		created_at) VALUES (?, ?, ?, '["*"]', ?, 1)`, id, "dispatch-"+id, "hash-"+id, dispatchID)
	require.NoError(w.t, err)
}

// Scenario (issue #856): what a Task and a Run cost. Task a's worker goes through the model gateway on two keys
// of its Dispatch, two requests priced and one not; task b's runtime does not, and reports its tokens. Each Task
// counts its own, and the Run both. When the a's Dispatch settles its totals are kept: a request recorded after
// it settled still counts, and once the gateway's clean-up deletes the requests, the totals are what is left.
func TestATasksCostIsCountedThroughItsDispatchesKeysAndOutlivesThePurge(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	tasks := w.tasks(
		NewTask{Ref: "a", Profile: "implementer", Spec: "a"},
		NewTask{Ref: "b", Profile: "implementer", Spec: "b"},
	)
	a, b := tasks[0].ID, tasks[1].ID

	d, c := w.start(a)
	w.gatewayKey("gwk_1", d.ID)
	w.gatewayKey("gwk_2", d.ID)
	w.gatewayRequest("gwu_1", "gwk_1", true)
	w.gatewayRequest("gwu_2", "gwk_2", true)
	w.gatewayRequest("gwu_3", "gwk_2", false)

	running, err := w.s.TaskUsage(t.Context(), a)
	require.NoError(t, err)
	assert.Equal(t, int64(3), running.Requests, "a running Dispatch counts from the requests")

	require.NoError(t, w.finish(c, success("done")))

	gateway := WorkUsage{
		Dispatches: 1, Gateway: 1, Requests: 3, Prompt: 3000, Completion: 300, Cached: 1200, Reasoning: 30,
		CacheWrite: 150, CostMicros: 4000, Unpriced: 1,
	}

	got, err := w.s.TaskUsage(t.Context(), a)
	require.NoError(t, err)
	assert.Equal(t, gateway, got, "the runtime's own report is not counted twice")

	w.succeed(b, "done")

	reported := WorkUsage{Dispatches: 1, Reported: Usage{Input: 1200, Output: 300, CacheRead: 9000}}

	got, err = w.s.TaskUsage(t.Context(), b)
	require.NoError(t, err)
	assert.Equal(t, reported, got)

	total, byTask, err := w.s.RunUsage(t.Context(), w.run.ID)
	require.NoError(t, err)
	assert.Equal(t, gateway, byTask[a])
	assert.Equal(t, reported, byTask[b])

	both := gateway
	both.add(reported)
	assert.Equal(t, both, total)

	// A streamed answer that ended after the Dispatch settled is recorded late.
	w.gatewayRequest("gwu_4", "gwk_2", true)

	got, err = w.s.TaskUsage(t.Context(), a)
	require.NoError(t, err)
	assert.Equal(t, int64(4), got.Requests)

	// The gateway's clean-up deletes the requests after 31 days.
	_, err = w.db.ExecContext(t.Context(), `DELETE FROM gateway_usage`)
	require.NoError(t, err)

	got, err = w.s.TaskUsage(t.Context(), a)
	require.NoError(t, err)
	assert.Equal(t, gateway, got, "the totals kept when the Dispatch settled")

	_, err = w.s.TaskUsage(t.Context(), "tsk_nobody")
	require.ErrorIs(t, err, ErrNotFound)
}

// repeatedFailures are the Run's repeated_failure escalations, as their payloads.
func (w *world) repeatedFailures() []map[string]any {
	w.t.Helper()

	page, err := w.s.ListEvents(w.t.Context(), 0, EventFilter{RunID: w.run.ID}, 1000)
	require.NoError(w.t, err)

	var out []map[string]any

	for _, e := range page.Events {
		var p map[string]any
		require.NoError(w.t, json.Unmarshal(e.Payload, &p))

		if e.Kind == EventEscalation && p["kind"] == string(EscalationRepeatedFailure) {
			p["task_id"], p["dispatch_id"] = e.TaskID, e.DispatchID
			out = append(out, p)
		}
	}

	return out
}

// Scenario (issue #856): a Task whose worker fails twice in a row with worker_error raises one repeated_failure
// at the second, naming the class, the count and both Dispatches; its retries go on as before. A Task that fails
// once as worker_error and then lost raises none; lost again with the same unknown cause raises one that names
// the cause. A failure of another Task does not count.
func TestASecondFailureForTheSameReasonIsEscalated(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	tasks := w.tasks(
		NewTask{Ref: "a", Profile: "implementer", Spec: "a"},
		NewTask{Ref: "b", Profile: "implementer", Spec: "b"},
	)
	a, b := tasks[0].ID, tasks[1].ID

	first := w.failOnce(a, FailureWorkerError)
	assert.Empty(t, w.repeatedFailures(), "one failure is not a repetition")

	w.advance(BackoffMax)
	second := w.failOnce(a, FailureWorkerError)

	got := w.repeatedFailures()
	require.Len(t, got, 1)
	assert.Equal(t, "worker_error", got[0]["reason"])
	assert.InDelta(t, 2, got[0]["count"], 0)
	assert.Equal(t, []any{second.ID, first.ID}, got[0]["dispatches"])
	assert.Equal(t, a, got[0]["task_id"])
	assert.Equal(t, second.ID, got[0]["dispatch_id"])
	assert.Equal(t, TaskReady, w.task(a).Status, "the Task is retried as before")

	w.advance(BackoffMax)
	w.failOnce(b, FailureWorkerError)
	require.Len(t, w.repeatedFailures(), 1, "another Task's failure does not count")

	lose := func() Dispatch {
		w.advance(BackoffMax)

		d, _ := w.start(b)
		w.loseLease()
		require.Equal(t, DispatchUnknown, w.dispatchOf(d.ID).Status)
		w.connect(nil)

		return d
	}

	lost := lose()
	require.Len(t, w.repeatedFailures(), 1, "worker_error then lost are different reasons")

	again := lose()

	got = w.repeatedFailures()
	require.Len(t, got, 2)
	assert.Equal(t, "lost:lease_lost", got[1]["reason"], fmt.Sprint(got[1]))
	assert.Equal(t, []any{again.ID, lost.ID}, got[1]["dispatches"])
}
