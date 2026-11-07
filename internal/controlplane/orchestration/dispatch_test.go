package orchestration

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
)

// The Dispatch tests are a table of scenarios in the style of Omnigent's
// docs/network-resilience.md: each one names a failure of the node, the
// network or the worker and what must follow. They run on real SQLite with a
// fake clock.

// world is one database, one Run and the calls of one node.
type world struct {
	t     *testing.T
	s     *Service
	db    *sql.DB
	clock *fakeClock
	run   Run
	reqs  int
}

func newWorld(t *testing.T) *world {
	t.Helper()

	s, db, clock := newTestService(t)

	return &world{t: t, s: s, db: db, clock: clock, run: newRun(t, s)}
}

func (w *world) tasks(nts ...NewTask) []Task {
	w.t.Helper()

	tasks, err := w.s.CreateTasks(w.t.Context(), w.run.ID, nts)
	require.NoError(w.t, err)

	return tasks
}

func (w *world) statuses() map[string]string { return statuses(w.t, w.s, w.run.ID) }

func (w *world) task(id string) Task {
	w.t.Helper()

	task, err := w.s.GetTask(w.t.Context(), id)
	require.NoError(w.t, err)

	return task
}

func (w *world) dispatchOf(id string) Dispatch {
	w.t.Helper()

	d, err := w.s.GetDispatch(w.t.Context(), id)
	require.NoError(w.t, err)

	return d
}

func (w *world) conversation(id string) Conversation {
	w.t.Helper()

	c, err := w.s.GetConversation(w.t.Context(), id)
	require.NoError(w.t, err)

	return c
}

func (w *world) spec() DispatchSpec {
	return DispatchSpec{NodeID: "node_a", Worktree: "/srv/wt/a", Runtime: "claude", AccountRef: "acct_1"}
}

func (w *world) advance(d time.Duration) { w.clock.now = w.clock.now.Add(d) }

// call is a node call about d at the given epoch, with a fresh request id.
func (w *world) call(d Dispatch, epoch int) NodeCall {
	w.reqs++

	return NodeCall{NodeID: d.NodeID, DispatchID: d.ID, Epoch: epoch, RequestID: fmt.Sprintf("req_%d", w.reqs)}
}

// again is the same call with a fresh request id.
func (w *world) again(c NodeCall) NodeCall {
	w.reqs++
	c.RequestID = fmt.Sprintf("req_%d", w.reqs)

	return c
}

func (w *world) dispatch(taskID string) Dispatch {
	w.t.Helper()

	d, err := w.s.CreateDispatch(w.t.Context(), taskID, w.spec())
	require.NoError(w.t, err)

	return d
}

func (w *world) accept(d Dispatch) NodeCall {
	w.t.Helper()

	c := w.call(d, 0)
	acc, err := w.s.AcceptDispatch(w.t.Context(), c)
	require.NoError(w.t, err)
	c.Epoch = acc.Epoch

	return c
}

// start creates a Dispatch for the Task and runs it up to running.
func (w *world) start(taskID string) (Dispatch, NodeCall) {
	w.t.Helper()

	d := w.dispatch(taskID)
	c := w.accept(d)
	require.NoError(w.t, w.s.ReportStarted(w.t.Context(), w.again(c), "pid:42@1", "sess_1"))

	return d, c
}

func (w *world) heartbeat(c NodeCall) Beat {
	w.t.Helper()

	b, err := w.s.Heartbeat(w.t.Context(), w.again(c))
	require.NoError(w.t, err)

	return b
}

func (w *world) finish(c NodeCall, o Outcome) error {
	return w.s.ReportFinished(w.t.Context(), w.again(c), o)
}

func (w *world) succeed(taskID, summary string) {
	w.t.Helper()

	_, c := w.start(taskID)
	require.NoError(w.t, w.finish(c, success(summary)))
}

func (w *world) failOnce(taskID string, class FailureClass) Dispatch {
	w.t.Helper()

	d, c := w.start(taskID)
	require.NoError(w.t, w.finish(c, Outcome{Class: class, Result: Result{Summary: "tests failed"}}))

	return d
}

// failForGood fails a Task through the breaker.
func (w *world) failForGood(taskID string) {
	w.t.Helper()

	for range BreakerFailures {
		w.failOnce(taskID, FailureWorkerError)
		w.advance(BackoffMax)
	}
}

// loseLease lets the lease of d run out and runs the reaper.
func (w *world) loseLease() Reaped {
	w.t.Helper()
	w.advance(LeaseDuration)

	return w.reap()
}

func (w *world) reap() Reaped {
	w.t.Helper()

	r, err := w.s.Reap(w.t.Context())
	require.NoError(w.t, err)

	return r
}

func success(summary string) Outcome {
	code := 0

	return Outcome{Result: Result{
		Summary:  summary,
		Evidence: []Evidence{{Kind: "pull_request", Ref: "kl3inIT/second-bite#7"}},
		Exit:     Exit{Kind: "exited", Code: &code},
		Usage:    &Usage{Input: 1200, Output: 300, CacheRead: 9000},
	}}
}

// The Dispatch transition table restated pair by pair; every move that goes
// through a guarded UPDATE built from it accepts exactly the allowed pairs.
func TestEveryDispatchTransitionPair(t *testing.T) {
	t.Parallel()

	allowed := map[[2]DispatchStatus]bool{}
	for _, p := range [][2]DispatchStatus{
		{DispatchQueued, DispatchStarting}, {DispatchQueued, DispatchExpired}, {DispatchQueued, DispatchCanceled},
		{DispatchStarting, DispatchRunning}, {DispatchStarting, DispatchFailed},
		{DispatchStarting, DispatchStopping}, {DispatchStarting, DispatchUnknown},
		{DispatchRunning, DispatchSucceeded}, {DispatchRunning, DispatchFailed},
		{DispatchRunning, DispatchStopping}, {DispatchRunning, DispatchUnknown},
		{DispatchStopping, DispatchCanceled}, {DispatchStopping, DispatchStopUnknown},
		{DispatchUnknown, DispatchRunning}, {DispatchUnknown, DispatchSucceeded}, {DispatchUnknown, DispatchFailed},
		{DispatchUnknown, DispatchCanceled}, {DispatchUnknown, DispatchAbandoned},
		{DispatchStopUnknown, DispatchSucceeded}, {DispatchStopUnknown, DispatchFailed},
		{DispatchStopUnknown, DispatchCanceled}, {DispatchStopUnknown, DispatchAbandoned},
	} {
		allowed[p] = true
	}

	w := newWorld(t)
	guarded := map[DispatchStatus]bool{
		DispatchSucceeded: true, DispatchFailed: true, DispatchCanceled: true, DispatchExpired: true,
		DispatchAbandoned: true, DispatchUnknown: true, DispatchStopUnknown: true,
	}

	for _, from := range dispatchOrder {
		for _, to := range dispatchOrder {
			assert.Equal(t, allowed[[2]DispatchStatus{from, to}], canDispatch(from, to), "%s to %s", from, to)

			if !guarded[to] {
				continue
			}

			task := w.tasks(NewTask{Ref: "x", Role: "implement", Spec: fmt.Sprintf("%s-%s", from, to)})[0]
			d := w.dispatch(task.ID)

			unknown := from == DispatchUnknown || from == DispatchStopUnknown
			_, err := w.db.ExecContext(t.Context(),
				"UPDATE dispatches SET status = ?, failure_class = ?, unknown_cause = ? WHERE id = ?", string(from),
				sql.NullString{String: "worker_error", Valid: from == DispatchFailed},
				sql.NullString{String: "lease_lost", Valid: unknown}, d.ID)
			require.NoError(t, err)

			var n int64

			require.NoError(t, w.s.write(t.Context(), func(q *store.Queries, now int64) error {
				if to == DispatchUnknown || to == DispatchStopUnknown {
					n, err = q.MarkUnknown(t.Context(), store.MarkUnknownParams{
						ToStatus: string(to), UnknownCause: nullString("lease_lost"), Now: now, ID: d.ID,
						FromStatuses: dispatchFrom(to),
					})

					return err
				}

				n, err = q.SettleDispatch(t.Context(), store.SettleDispatchParams{
					ToStatus: string(to), FailureClass: nullString(map[bool]string{true: "worker_error"}[to == DispatchFailed]),
					Now: now, ID: d.ID, FromStatuses: dispatchFrom(to),
				})

				return err
			}))
			assert.Equal(t, allowed[[2]DispatchStatus{from, to}], n == 1, "%s to %s", from, to)
		}
	}
}

// Scenario: everything works. The node accepts, starts the CLI, renews its
// lease and reports success; the Task succeeds and its dependent is ready.
func TestDispatchRunsToSuccess(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	tasks := w.tasks(
		NewTask{Ref: "api", Role: "implement", Spec: "api"},
		NewTask{Ref: "review", Role: "review", Spec: "review", DependsOn: []string{"api"}},
	)

	d := w.dispatch(tasks[0].ID)
	assert.Equal(t, DispatchQueued, d.Status)
	assert.Equal(t, 0, d.Epoch)
	assert.Equal(t, TaskRunning, w.task(tasks[0].ID).Status)

	c := w.accept(d)
	assert.Equal(t, 1, c.Epoch)
	assert.Equal(t, DispatchStarting, w.dispatchOf(d.ID).Status)
	assert.Equal(t, t0.Add(LeaseDuration), *w.dispatchOf(d.ID).LeaseExpiresAt)

	require.NoError(t, w.s.ReportStarted(t.Context(), w.again(c), "pid:42@1", "sess_1"))
	assert.Equal(t, DispatchRunning, w.dispatchOf(d.ID).Status)
	assert.Equal(t, "sess_1", w.conversation(d.ConversationID).CLISessionRef)

	w.advance(30 * time.Second)
	beat := w.heartbeat(c)
	assert.Equal(t, t0.Add(30*time.Second+LeaseDuration), beat.LeaseExpiresAt)
	assert.False(t, beat.StopRequested)
	require.NoError(t, w.s.ReportProgress(t.Context(), w.again(c)))

	require.NoError(t, w.finish(c, success("added the login endpoint")))

	got := w.dispatchOf(d.ID)
	assert.Equal(t, DispatchSucceeded, got.Status)
	require.NotNil(t, got.Result)
	assert.Equal(t, success("added the login endpoint").Result, *got.Result)
	assert.Nil(t, got.LeaseExpiresAt)
	assert.Equal(t, map[string]string{"api": "succeeded:", "review": "ready:"}, w.statuses())
	assert.Equal(t, "added the login endpoint", w.task(tasks[0].ID).Result)
	assert.Equal(t, ConversationOpen, w.conversation(d.ConversationID).Status)
}

// Scenario: no node takes the Dispatch. An accept one millisecond before the
// queue timeout wins; at the timeout the expiry wins. An expiry does not count
// as a failure, and a Conversation that never started is closed.
func TestAcceptLosesToExpiryAtTheSameInstant(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	tasks := w.tasks(NewTask{Ref: "a", Role: "implement", Spec: "a"}, NewTask{Ref: "b", Role: "implement", Spec: "b"})
	late := w.dispatch(tasks[0].ID)
	early := w.dispatch(tasks[1].ID)

	w.advance(QueueTimeout - time.Millisecond)
	w.accept(early)

	w.advance(time.Millisecond)
	_, err := w.s.AcceptDispatch(t.Context(), w.call(late, 0))
	require.ErrorIs(t, err, ErrConflict)

	r := w.reap()
	assert.Equal(t, []string{late.ID}, r.Expired)
	assert.Equal(t, DispatchExpired, w.dispatchOf(late.ID).Status)
	assert.Equal(t, TaskReady, w.task(tasks[0].ID).Status)
	assert.Zero(t, w.task(tasks[0].ID).FailureCount)
	assert.Equal(t, CloseNeverStarted, w.conversation(late.ConversationID).CloseReason)
}

// Scenario: the node goes silent. A heartbeat after the lease ran out renews
// nothing, an old epoch is refused, and the reaper makes the outcome unknown:
// the Task is blocked and no replacement can start.
func TestLostLeaseMakesTheOutcomeUnknown(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	task := w.tasks(NewTask{Ref: "a", Role: "implement", Spec: "a"})[0]
	d, c := w.start(task.ID)

	stale := c
	stale.Epoch = 0
	_, err := w.s.Heartbeat(t.Context(), w.again(stale))
	require.ErrorIs(t, err, ErrConflict)

	w.advance(LeaseDuration)
	_, err = w.s.Heartbeat(t.Context(), w.again(c))
	require.ErrorIs(t, err, ErrConflict)
	assert.Equal(t, t0.Add(LeaseDuration), *w.dispatchOf(d.ID).LeaseExpiresAt)

	r := w.reap()
	assert.Equal(t, []string{d.ID}, r.Unknown)

	got := w.dispatchOf(d.ID)
	assert.Equal(t, DispatchUnknown, got.Status)
	assert.Equal(t, UnknownLeaseLost, got.UnknownCause)
	assert.Equal(t, "blocked:dispatch_unknown", w.statuses()["a"])

	_, err = w.s.CreateDispatch(t.Context(), task.ID, w.spec())
	require.ErrorIs(t, err, ErrConflict)
}

// Scenario: the network drops but the worker finishes. Its report, from the
// epoch it was given, settles the unknown Dispatch first-hand: the Task
// succeeds and the Conversation stays open.
func TestSameEpochReportSettlesAnUnknownDispatch(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	task := w.tasks(NewTask{Ref: "a", Role: "implement", Spec: "a"})[0]
	d, c := w.start(task.ID)
	w.loseLease()

	stale := c
	stale.Epoch = 0
	require.ErrorIs(t, w.finish(stale, success("done")), ErrConflict)

	require.NoError(t, w.finish(c, success("done")))
	assert.Equal(t, DispatchSucceeded, w.dispatchOf(d.ID).Status)
	assert.Equal(t, TaskSucceeded, w.task(task.ID).Status)
	assert.Equal(t, ConversationOpen, w.conversation(d.ConversationID).Status)
}

// Scenario: a laptop sleeps and wakes. The same node, proving the same
// process and the epoch it held, reattaches and gets a new epoch; the old
// epoch is dead from then on.
func TestReattachRecoversASleepingNode(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	task := w.tasks(NewTask{Ref: "a", Role: "implement", Spec: "a"})[0]
	d, c := w.start(task.ID)
	w.loseLease()

	_, err := w.s.ReattachDispatch(t.Context(), w.again(c), "pid:99@7")
	require.ErrorIs(t, err, ErrConflict, "another process")

	other := w.again(c)
	other.NodeID = "node_b"
	_, err = w.s.ReattachDispatch(t.Context(), other, "pid:42@1")
	require.ErrorIs(t, err, ErrConflict, "another node")

	acc, err := w.s.ReattachDispatch(t.Context(), w.again(c), "pid:42@1")
	require.NoError(t, err)
	assert.Equal(t, 2, acc.Epoch)
	assert.Equal(t, "sess_1", acc.CLISessionRef)
	assert.Equal(t, DispatchRunning, w.dispatchOf(d.ID).Status)
	assert.Equal(t, TaskRunning, w.task(task.ID).Status)

	_, err = w.s.Heartbeat(t.Context(), w.again(c))
	require.ErrorIs(t, err, ErrConflict, "the old epoch")

	c.Epoch = acc.Epoch
	w.heartbeat(c)
}

// Scenario: the coordinator settled the unknown Dispatch as lost before the
// node came back. The reattach is refused, the Conversation is closed, and
// the next Dispatch opens a new one with the recovery note.
func TestReattachIsRefusedOnceTheOutcomeIsSettled(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	task := w.tasks(NewTask{Ref: "a", Role: "implement", Spec: "a"})[0]
	d, c := w.start(task.ID)
	w.loseLease()

	require.NoError(t, w.s.SettleUnknown(t.Context(), d.ID, Settlement{Result: Result{Summary: "no push found"}}))
	got := w.dispatchOf(d.ID)
	assert.Equal(t, DispatchFailed, got.Status)
	assert.Equal(t, FailureLost, got.FailureClass)
	assert.Equal(t, CloseInterrupted, w.conversation(d.ConversationID).CloseReason)

	after := w.task(task.ID)
	assert.Equal(t, TaskReady, after.Status)
	assert.Equal(t, 1, after.FailureCount)
	assert.Equal(t, w.clock.now.Add(BackoffBase), *after.NotBefore)

	_, err := w.s.ReattachDispatch(t.Context(), w.again(c), "pid:42@1")
	require.ErrorIs(t, err, ErrConflict)

	w.advance(BackoffBase)
	_, err = w.s.CreateDispatch(t.Context(), task.ID, DispatchSpec{Continue: true})
	require.ErrorIs(t, err, ErrConflict, "a closed conversation is not continued")

	next := w.dispatch(task.ID)
	assert.True(t, next.Recovery)
	assert.NotEqual(t, d.ConversationID, next.ConversationID)

	acc, err := w.s.AcceptDispatch(t.Context(), w.call(next, 0))
	require.NoError(t, err)
	assert.True(t, acc.Recovery)
	assert.Empty(t, acc.CLISessionRef)
}

// Scenario: the worker process dies without a word. That is unknown, never a
// success, and a process that is gone cannot be reattached.
func TestProcessThatExitsWithoutAReportIsUnknown(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	task := w.tasks(NewTask{Ref: "a", Role: "implement", Spec: "a"})[0]
	d, c := w.start(task.ID)

	require.NoError(t, w.s.ReportExited(t.Context(), w.again(c)))
	got := w.dispatchOf(d.ID)
	assert.Equal(t, DispatchUnknown, got.Status)
	assert.Equal(t, UnknownExitedUnreported, got.UnknownCause)
	assert.Equal(t, "blocked:dispatch_unknown", w.statuses()["a"])

	_, err := w.s.ReattachDispatch(t.Context(), w.again(c), "pid:42@1")
	require.ErrorIs(t, err, ErrConflict)
}

// Scenario: the owner cancels while the worker runs. The Task stays running
// until the node confirms the stop; a final report during the stop is
// refused; the confirmation cancels the Task and blocks its dependents.
func TestCancelWaitsForTheConfirmedStop(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	tasks := w.tasks(
		NewTask{Ref: "a", Role: "implement", Spec: "a"},
		NewTask{Ref: "b", Role: "review", Spec: "b", DependsOn: []string{"a"}},
	)
	d, c := w.start(tasks[0].ID)

	require.NoError(t, w.s.CancelTask(t.Context(), tasks[0].ID))
	assert.Equal(t, TaskRunning, w.task(tasks[0].ID).Status)
	assert.NotNil(t, w.task(tasks[0].ID).CancelRequestedAt)
	assert.Equal(t, DispatchStopping, w.dispatchOf(d.ID).Status)
	_, err := w.s.CreateDispatch(t.Context(), tasks[0].ID, w.spec())
	require.ErrorIs(t, err, ErrConflict, "no second worker while the first may push")

	assert.True(t, w.heartbeat(c).StopRequested)
	require.ErrorIs(t, w.finish(c, success("done anyway")), ErrConflict)

	require.NoError(t, w.s.ReportStopped(t.Context(), w.again(c)))
	assert.Equal(t, DispatchCanceled, w.dispatchOf(d.ID).Status)
	assert.Equal(t, map[string]string{"a": "canceled:", "b": "blocked:dependency_failed"}, w.statuses())
	require.NoError(t, w.s.ReportStopped(t.Context(), w.again(c)), "a repeat is harmless")
}

// Scenario: the worker exits while it is being stopped. The exit is the
// confirmation of the stop.
func TestExitDuringAStopConfirmsIt(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	tasks := w.tasks(
		NewTask{Ref: "a", Role: "implement", Spec: "a"},
		NewTask{Ref: "b", Role: "review", Spec: "b", DependsOn: []string{"a"}},
	)
	_, c := w.start(tasks[0].ID)

	require.NoError(t, w.s.CancelTask(t.Context(), tasks[0].ID))
	require.NoError(t, w.s.ReportExited(t.Context(), w.again(c)), "an exit during a stop confirms it")
	assert.Equal(t, map[string]string{"a": "canceled:", "b": "blocked:dependency_failed"}, w.statuses())
}

// Scenario: the stop is never confirmed, because the worker ignores it or the
// node goes silent. The Dispatch becomes stop_unknown and the Task stays
// blocked; a late confirmation still cancels it.
func TestUnconfirmedStopBecomesStopUnknown(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	tasks := w.tasks(NewTask{Ref: "a", Role: "implement", Spec: "a"}, NewTask{Ref: "b", Role: "implement", Spec: "b"})
	ignored, c := w.start(tasks[0].ID)
	silent, _ := w.start(tasks[1].ID)

	require.NoError(t, w.s.CancelRun(t.Context(), w.run.ID))
	w.heartbeat(c)

	w.advance(StopTimeout)
	r := w.reap()
	assert.Equal(t, []string{ignored.ID}, r.StopUnknown)
	assert.Equal(t, UnknownStopTimeout, w.dispatchOf(ignored.ID).UnknownCause)

	w.advance(LeaseDuration)
	r = w.reap()
	assert.Equal(t, []string{silent.ID}, r.StopUnknown)
	assert.Equal(t, UnknownLeaseLost, w.dispatchOf(silent.ID).UnknownCause)
	assert.Equal(t, map[string]string{"a": "blocked:dispatch_unknown", "b": "blocked:dispatch_unknown"}, w.statuses())

	require.NoError(t, w.s.ReportStopped(t.Context(), w.again(c)))
	assert.Equal(t, TaskCanceled, w.task(tasks[0].ID).Status)
}

// Scenario: a cancel before any node took the Dispatch is immediate.
func TestCancelOfAQueuedDispatchIsImmediate(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	task := w.tasks(NewTask{Ref: "a", Role: "implement", Spec: "a"})[0]
	d := w.dispatch(task.ID)

	require.NoError(t, w.s.CancelTask(t.Context(), task.ID))
	assert.Equal(t, DispatchCanceled, w.dispatchOf(d.ID).Status)
	assert.Equal(t, TaskCanceled, w.task(task.ID).Status)

	_, err := w.s.AcceptDispatch(t.Context(), w.call(d, 0))
	require.ErrorIs(t, err, ErrConflict)
}

// Scenario: canceling a Run asks every running worker to stop, and cancels
// at once what has no worker.
func TestCancelRunStopsEveryRunningDispatch(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	tasks := w.tasks(
		NewTask{Ref: "a", Role: "implement", Spec: "a"},
		NewTask{Ref: "b", Role: "implement", Spec: "b"},
		NewTask{Ref: "c", Role: "review", Spec: "c", DependsOn: []string{"a"}},
	)
	da, ca := w.start(tasks[0].ID)
	db, cb := w.start(tasks[1].ID)

	require.NoError(t, w.s.CancelRun(t.Context(), w.run.ID))
	assert.Equal(t, map[string]string{"a": "running:", "b": "running:", "c": "canceled:"}, w.statuses())
	assert.Equal(t, DispatchStopping, w.dispatchOf(da.ID).Status)
	assert.Equal(t, DispatchStopping, w.dispatchOf(db.ID).Status)

	require.NoError(t, w.s.ReportStopped(t.Context(), w.again(ca)))
	require.NoError(t, w.s.ReportStopped(t.Context(), w.again(cb)))
	assert.Equal(t, map[string]string{"a": "canceled:", "b": "canceled:", "c": "canceled:"}, w.statuses())
}

// Scenario: the work keeps failing. Each counted failure waits longer before
// the retry; the third fails the Task and blocks its dependents; the owner
// reopens it and the count starts again. A launch that failed never ran.
func TestThirdCountedFailureTripsTheBreaker(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	tasks := w.tasks(
		NewTask{Ref: "a", Role: "implement", Spec: "a"},
		NewTask{Ref: "b", Role: "review", Spec: "b", DependsOn: []string{"a"}},
	)
	a := tasks[0].ID

	w.failOnce(a, FailureWorkerError)
	assert.Equal(t, 1, w.task(a).FailureCount)
	assert.Equal(t, w.clock.now.Add(BackoffBase), *w.task(a).NotBefore)
	_, err := w.s.CreateDispatch(t.Context(), a, w.spec())
	require.ErrorIs(t, err, ErrConflict, "too early")

	w.advance(BackoffBase)
	d := w.dispatch(a)
	c := w.accept(d)
	require.ErrorIs(t, w.finish(c, success("never ran")), ErrConflict, "nothing ran yet")
	require.NoError(t, w.finish(c, Outcome{Class: FailureStartFailed, Result: Result{Summary: "claude not found"}}))
	assert.Equal(t, 2, w.task(a).FailureCount)
	assert.Equal(t, w.clock.now.Add(2*BackoffBase), *w.task(a).NotBefore)

	w.advance(2 * BackoffBase)
	w.failOnce(a, FailureWorkerError)
	assert.Equal(t, map[string]string{"a": "failed:", "b": "blocked:dependency_failed"}, w.statuses())
	assert.Equal(t, "3 failed attempts; the last: worker_error", w.task(a).Result)

	require.NoError(t, w.s.ReopenTask(t.Context(), a))
	assert.Equal(t, map[string]string{"a": "ready:", "b": "pending:"}, w.statuses())
	assert.Zero(t, w.task(a).FailureCount)
	assert.Nil(t, w.task(a).NotBefore)
	assert.Nil(t, w.task(a).FinishedAt)
}

// Scenario: the account hits a rate limit. It is not counted; the Task waits
// for the reset, and the slot takes no other Dispatch until then while another
// account still can.
func TestRateLimitIsNotCountedAndCoolsTheSlot(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	tasks := w.tasks(
		NewTask{Ref: "a", Role: "implement", Spec: "a"},
		NewTask{Ref: "b", Role: "implement", Spec: "b"},
		NewTask{Ref: "c", Role: "implement", Spec: "c"},
	)
	resets := t0.Add(10 * time.Minute)

	d, c := w.start(tasks[0].ID)
	require.NoError(t, w.finish(c, Outcome{Class: FailureRateLimited, ResetsAt: &resets}))
	assert.Equal(t, FailureRateLimited, w.dispatchOf(d.ID).FailureClass)
	assert.Zero(t, w.task(tasks[0].ID).FailureCount)
	assert.Equal(t, resets, *w.task(tasks[0].ID).NotBefore)

	_, err := w.s.CreateDispatch(t.Context(), tasks[1].ID, w.spec())
	require.ErrorIs(t, err, ErrCoolingDown)

	other := w.spec()
	other.AccountRef = "acct_2"
	_, err = w.s.CreateDispatch(t.Context(), tasks[2].ID, other)
	require.NoError(t, err)

	w.clock.now = resets
	w.dispatch(tasks[1].ID)
	w.dispatch(tasks[0].ID)
}

// Scenario: a node is off. Expiries are not counted, but three in a row on
// one node escalate it as unavailable.
func TestExpiriesInARowMakeTheNodeUnavailable(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	task := w.tasks(NewTask{Ref: "a", Role: "implement", Spec: "a"})[0]

	for i := range ExpiriesBeforeUnavailable {
		w.dispatch(task.ID)
		w.advance(QueueTimeout)

		r := w.reap()
		if i < ExpiriesBeforeUnavailable-1 {
			assert.Empty(t, r.Escalations)

			continue
		}

		assert.Equal(t, []Escalation{{Kind: EscalationNodeUnavailable, NodeID: "node_a"}}, r.Escalations)
	}

	assert.Zero(t, w.task(task.ID).FailureCount)
}

// Scenario: the account needs a login. The Task is blocked and never retried
// on its own; the owner logs in and reopens it.
func TestNeedsAuthIsNeverRetried(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	task := w.tasks(NewTask{Ref: "a", Role: "implement", Spec: "a"})[0]

	w.failOnce(task.ID, FailureNeedsAuth)
	assert.Equal(t, "blocked:needs_auth", w.statuses()["a"])

	w.advance(24 * time.Hour)
	_, err := w.s.CreateDispatch(t.Context(), task.ID, w.spec())
	require.ErrorIs(t, err, ErrConflict)

	require.NoError(t, w.s.ReopenTask(t.Context(), task.ID))
	w.dispatch(task.ID)
}

// Scenario: the network repeats a call. The same request id returns the
// stored response; the same id with another call is refused; a settled
// Dispatch accepts only an identical final report.
func TestRepeatedCallsReturnTheSameReceipt(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	task := w.tasks(NewTask{Ref: "a", Role: "implement", Spec: "a"})[0]
	d := w.dispatch(task.ID)

	first := w.call(d, 0)
	acc1, err := w.s.AcceptDispatch(t.Context(), first)
	require.NoError(t, err)
	acc2, err := w.s.AcceptDispatch(t.Context(), first)
	require.NoError(t, err)
	assert.Equal(t, acc1, acc2)
	assert.Equal(t, 1, w.dispatchOf(d.ID).Epoch, "accepted once")

	_, err = w.s.Heartbeat(t.Context(), first)
	require.ErrorIs(t, err, ErrConflict, "the id was used for another call")

	c := first
	c.Epoch = acc1.Epoch
	require.NoError(t, w.s.ReportStarted(t.Context(), w.again(c), "pid:42@1", "sess_1"))

	report := w.again(c)
	require.NoError(t, w.s.ReportFinished(t.Context(), report, success("done")))
	require.NoError(t, w.s.ReportFinished(t.Context(), report, success("done")))
	require.ErrorIs(t, w.s.ReportFinished(t.Context(), report, success("other")), ErrConflict)
	require.NoError(t, w.finish(c, success("done")), "an identical report under a new id")
	require.ErrorIs(t, w.finish(c, Outcome{Class: FailureWorkerError}), ErrConflict)
}

// Scenario: a retry continues the worker's CLI session. Only an open
// Conversation whose last Dispatch ended first-hand is continued, on its own
// node; opening a new one supersedes it.
func TestContinuingAConversation(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	tasks := w.tasks(NewTask{Ref: "a", Role: "implement", Spec: "a"}, NewTask{Ref: "b", Role: "implement", Spec: "b"})
	first := w.failOnce(tasks[0].ID, FailureWorkerError)
	w.advance(BackoffBase)

	_, err := w.s.CreateDispatch(t.Context(), tasks[0].ID, DispatchSpec{Continue: true, NodeID: "node_b"})
	require.ErrorIs(t, err, ErrInvalid, "a conversation stays on its node")

	next, err := w.s.CreateDispatch(t.Context(), tasks[0].ID, DispatchSpec{Continue: true})
	require.NoError(t, err)
	assert.Equal(t, first.ConversationID, next.ConversationID)
	assert.False(t, next.Recovery)

	acc, err := w.s.AcceptDispatch(t.Context(), w.call(next, 0))
	require.NoError(t, err)
	assert.Equal(t, "sess_1", acc.CLISessionRef)

	old := w.failOnce(tasks[1].ID, FailureWorkerError)
	w.advance(BackoffBase)
	fresh := w.dispatch(tasks[1].ID)
	assert.NotEqual(t, old.ConversationID, fresh.ConversationID)
	assert.Equal(t, CloseSuperseded, w.conversation(old.ConversationID).CloseReason)
	assert.False(t, fresh.Recovery)
}

// Scenario: the worker is alive but does nothing, or runs very long. Both are
// escalated once, and nothing is killed.
func TestStallAndOverrunAreEscalatedNotKilled(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	task := w.tasks(NewTask{Ref: "a", Role: "implement", Spec: "a"})[0]
	d, c := w.start(task.ID)

	keepAlive := func(total time.Duration) {
		for step := time.Minute; total > 0; total -= step {
			w.advance(step)
			w.heartbeat(c)
		}
	}

	keepAlive(StallAfter - time.Minute)
	require.NoError(t, w.s.ReportProgress(t.Context(), w.again(c)))
	keepAlive(StallAfter - time.Minute)
	assert.Empty(t, w.reap().Escalations)

	keepAlive(time.Minute)
	assert.Equal(t, []Escalation{{Kind: EscalationStalled, DispatchID: d.ID, NodeID: "node_a"}}, w.reap().Escalations)
	assert.Empty(t, w.reap().Escalations, "escalated once")
	assert.NotNil(t, w.dispatchOf(d.ID).StalledAt)

	require.NoError(t, w.s.ReportProgress(t.Context(), w.again(c)))
	assert.Nil(t, w.dispatchOf(d.ID).StalledAt)

	keepAlive(MaxDuration - 2*StallAfter + time.Minute)
	r := w.reap()
	assert.Contains(t, r.Escalations, Escalation{Kind: EscalationOverrun, DispatchID: d.ID, NodeID: "node_a"})
	assert.Equal(t, DispatchRunning, w.dispatchOf(d.ID).Status)
}

// Scenario: nobody can tell what an unknown Dispatch did. Its effects show
// the push that was intended but not confirmed; abandoning it counts as lost
// and lists what may still be held on the node.
func TestAbandonListsWhatMayRemain(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	task := w.tasks(NewTask{Ref: "a", Role: "implement", Spec: "a"})[0]
	d, c := w.start(task.ID)
	ctx := t.Context()

	require.NoError(t, w.s.RecordResource(ctx, w.again(c), Resource{Kind: "worktree", Ref: "/srv/wt/a", Custody: "owned"}))
	require.NoError(t, w.s.RecordResource(ctx, w.again(c), Resource{Kind: "process", Ref: "pid:42@1", Custody: "owned"}))
	require.NoError(t, w.s.ReleaseResource(ctx, w.again(c), "process", "pid:42@1"))
	require.NoError(t, w.s.RecordEffect(ctx, w.again(c),
		Effect{Seq: 1, Kind: "push", Ref: "feat/login", Phase: EffectIntent}))
	require.NoError(t, w.s.RecordEffect(ctx, w.again(c), Effect{Seq: 2, Kind: "comment", Ref: "#7", Phase: EffectDone}))
	require.ErrorIs(t, w.s.RecordEffect(ctx, w.again(c), Effect{Seq: 2, Kind: "comment", Ref: "#7", Phase: EffectDone}),
		ErrConflict)

	w.loseLease()

	effects, err := w.s.ListEffects(ctx, d.ID)
	require.NoError(t, err)
	assert.Equal(t, []Effect{
		{Seq: 1, Kind: "push", Ref: "feat/login", Phase: EffectIntent},
		{Seq: 2, Kind: "comment", Ref: "#7", Phase: EffectDone},
	}, effects)

	held, err := w.s.AbandonDispatch(ctx, d.ID)
	require.NoError(t, err)
	assert.Equal(t, []Resource{{Kind: "worktree", Ref: "/srv/wt/a", Custody: "owned"}}, held)
	assert.Equal(t, DispatchAbandoned, w.dispatchOf(d.ID).Status)
	assert.Equal(t, 1, w.task(task.ID).FailureCount)
	assert.Equal(t, CloseInterrupted, w.conversation(d.ConversationID).CloseReason)

	_, err = w.s.AbandonDispatch(ctx, d.ID)
	require.ErrorIs(t, err, ErrConflict)
}
