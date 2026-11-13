package orchestration

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tools sends the worker's tool calls and their results, one call and one
// result for each entry, after the items already sent.
func (w *world) tools(d Dispatch, results ...Item) {
	w.t.Helper()

	page, err := w.s.ListItems(w.t.Context(), d.ID, 0, 0, ItemPageMax)
	require.NoError(w.t, err)

	seq := int64(len(page.Items))
	batch := make([]Item, 0, 2*len(results))

	for _, r := range results {
		seq++
		id := fmt.Sprintf("call_%d", seq)
		batch = append(batch, Item{Seq: seq, Kind: ItemToolCall, CallID: id, Tool: r.Tool, Input: r.Input})
		seq++
		batch = append(batch, Item{Seq: seq, Kind: ItemToolResult, CallID: id, Text: r.Text, Failed: r.Failed})
	}

	_, err = w.s.AddItems(w.t.Context(), w.node.ID, d.ID, batch)
	require.NoError(w.t, err)
}

func failing(command, text string) Item {
	return Item{Tool: "Bash", Input: json.RawMessage(`{"command":"` + command + `"}`), Text: text, Failed: true}
}

func working(command string) Item {
	return Item{Tool: "Bash", Input: json.RawMessage(`{"command":"` + command + `"}`), Text: "ok"}
}

// Scenario: the worker runs the same failing command again and again. At the
// third identical error the control plane stops it; once the node confirms
// the stop the Dispatch ends failed with the reason, and the Task is retried
// like after any failure, not canceled.
func TestAWorkerThatRepeatsTheSameErrorIsStopped(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	task := w.tasks(NewTask{Ref: "a", Profile: "implementer", Spec: "a"})[0]
	d, c := w.start(task.ID)

	w.tools(d, failing("make test", "no rule to make target"), failing("make test", "no rule to make target"))
	assert.Equal(t, DispatchRunning, w.dispatchOf(d.ID).Status, "two are not yet a loop")

	w.tools(d, failing("make test", "no rule to make target"))
	stopped := w.dispatchOf(d.ID)
	assert.Equal(t, DispatchStopping, stopped.Status)
	assert.Equal(t, "Stopped after 3 identical errors: Bash: no rule to make target", stopped.StalledReason)
	assert.True(t, w.heartbeat(c).StopRequested, "the node is told to stop the worker")

	require.NoError(t, w.s.ReportStopped(t.Context(), w.again(c)))

	ended := w.dispatchOf(d.ID)
	assert.Equal(t, DispatchFailed, ended.Status)
	assert.Equal(t, FailureWorkerError, ended.FailureClass)
	require.NotNil(t, ended.Result)
	assert.Equal(t, stopped.StalledReason, ended.Result.Summary)
	assert.Equal(t, TaskReady, w.task(task.ID).Status)
	assert.Equal(t, 1, w.task(task.ID).FailureCount)
}

// Scenario: errors that differ, or a success between them, are a worker
// trying things, not a loop.
func TestDifferentErrorsOrASuccessBetweenAreNotALoop(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	task := w.tasks(NewTask{Ref: "a", Profile: "implementer", Spec: "a"})[0]
	d, _ := w.start(task.ID)

	w.tools(d,
		failing("make test", "no rule to make target"),
		failing("make test", "no rule to make target"),
		working("ls"),
		failing("make test", "no rule to make target"),
		failing("make test", "no rule to make target"),
		failing("go test ./...", "no rule to make target"),
		failing("go test ./...", "FAIL: TestA"),
	)

	assert.Equal(t, DispatchRunning, w.dispatchOf(d.ID).Status)
	assert.Empty(t, w.dispatchOf(d.ID).StalledReason)
}

// Scenario: a worker stays busy with tools that all fail. Being busy is not
// progress: only a tool that succeeds moves the stall timer.
func TestOnlyASuccessfulToolIsProgress(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	task := w.tasks(NewTask{Ref: "a", Profile: "implementer", Spec: "a"})[0]
	d, c := w.start(task.ID)
	start := *w.dispatchOf(d.ID).ProgressAt

	w.advance(time.Minute)
	w.heartbeat(c)
	require.NoError(t, w.s.ReportProgress(t.Context(), w.again(c)))
	w.tools(d, failing("make test", "first"), failing("make lint", "second"))
	assert.Equal(t, start, *w.dispatchOf(d.ID).ProgressAt, "a report and failing tools are not progress")

	w.tools(d, working("make build"))
	assert.Equal(t, w.clock.now, *w.dispatchOf(d.ID).ProgressAt)
}

// Scenario: two attempts in a row ran tools and none of them succeeded. The
// Task fails at once instead of waiting for the breaker's third failure.
func TestTwoAttemptsWithoutASuccessfulToolFailTheTask(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	task := w.tasks(NewTask{Ref: "a", Profile: "implementer", Spec: "a"})[0]

	attempt := func(results ...Item) {
		d, c := w.start(task.ID)
		w.tools(d, results...)
		require.NoError(t, w.finish(c, Outcome{Class: FailureWorkerError, Result: Result{Summary: "gave up"}}))
		w.advance(BackoffMax)
	}

	attempt(failing("make test", "a"))
	assert.Equal(t, TaskReady, w.task(task.ID).Status, "one attempt is not a pattern")

	attempt(failing("make test", "b"), failing("make lint", "c"))
	failed := w.task(task.ID)
	assert.Equal(t, TaskFailed, failed.Status)
	assert.Equal(t, "Stopped after 2 attempts without a successful tool call", failed.Result)
}

// Scenario: an attempt that got something done, or that ran no tool at all,
// breaks the run; the breaker decides as before.
func TestAnAttemptWithASuccessOrNoToolBreaksTheRun(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	task := w.tasks(NewTask{Ref: "a", Profile: "implementer", Spec: "a"})[0]

	attempt := func(results ...Item) {
		d, c := w.start(task.ID)
		w.tools(d, results...)
		require.NoError(t, w.finish(c, Outcome{Class: FailureWorkerError, Result: Result{Summary: "gave up"}}))
		w.advance(BackoffMax)
	}

	attempt(failing("make test", "a"))
	attempt(working("ls"), failing("make test", "a"))
	attempt()
	assert.Equal(t, TaskFailed, w.task(task.ID).Status)
	assert.Equal(t, "3 failed attempts; the last: worker_error", w.task(task.ID).Result)
}
