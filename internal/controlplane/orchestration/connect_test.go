package orchestration

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/profilestore"
)

// Scenario: a node restarts holding four Dispatches. The running one at its
// epoch is retained, the one whose lease was lost is reattached, and the one
// never accepted, the settled one, a stale epoch and an unknown id are released.
func TestNodeConnectAnswersEachClaim(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	tasks := w.tasks(specs(4)...)

	lost, _ := w.start(tasks[0].ID)
	w.loseLease()

	running, _ := w.start(tasks[1].ID)
	queued := w.dispatch(tasks[2].ID)
	settled, c := w.start(tasks[3].ID)
	require.NoError(t, w.finish(c, success("done")))

	got, err := w.s.NodeConnect(t.Context(), w.node.ID, Hello{
		WorktreeRoot: "/srv/wt", Runtimes: []RuntimeInfo{{Runtime: "claude"}},
	}, []Claim{
		{DispatchID: running.ID, Epoch: 1},
		{DispatchID: lost.ID, Epoch: 1},
		{DispatchID: queued.ID, Epoch: 0},
		{DispatchID: settled.ID, Epoch: 1},
		{DispatchID: running.ID, Epoch: 2},
		{DispatchID: "dsp_gone", Epoch: 1},
	})
	require.NoError(t, err)

	assert.Equal(t, []Disposition{
		{DispatchID: running.ID, Kind: DispositionRetain},
		{DispatchID: lost.ID, Kind: DispositionReattach},
		{DispatchID: queued.ID, Kind: DispositionRelease},
		{DispatchID: settled.ID, Kind: DispositionRelease},
		{DispatchID: running.ID, Kind: DispositionRelease},
		{DispatchID: "dsp_gone", Kind: DispositionRelease},
	}, got)
}

// Scenario: what a connected node is told. Its queued Dispatch is offered
// with what it needs, a stop asked of a running one is listed, and an answer
// waits until the node acknowledges it.
func TestNodePendingListsOffersStopsAndAnswers(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	tasks := w.tasks(specs(3)...)

	_, waiting := w.start(tasks[0].ID)
	question := w.ask(waiting, libraryChoice(AddressOwner))
	_, err := w.s.AnswerQuestion(t.Context(), question, AddressOwner, Answer{Option: choice(0)})
	require.NoError(t, err)

	stopping, _ := w.start(tasks[1].ID)
	require.NoError(t, w.s.CancelTask(t.Context(), tasks[1].ID))

	queued := w.dispatch(tasks[2].ID)

	p, err := w.s.NodePending(t.Context(), w.node.ID)
	require.NoError(t, err)

	require.Len(t, p.Offers, 1)
	assert.Equal(t, queued.ID, p.Offers[0].DispatchID)
	assert.Equal(t, "c", p.Offers[0].Spec)
	assert.Equal(t, "/srv/wt/a", p.Offers[0].Worktree)
	assert.Equal(t, profilestore.GatewayAccountRef, p.Offers[0].AccountRef)

	assert.Equal(t, []StopRequest{{DispatchID: stopping.ID, Epoch: 1}}, p.Stops)

	require.Len(t, p.Answers, 1)
	assert.Equal(t, question, p.Answers[0].ID)

	require.NoError(t, w.s.AckAnswer(t.Context(), w.again(waiting), question))

	p, err = w.s.NodePending(t.Context(), w.node.ID)
	require.NoError(t, err)
	assert.Empty(t, p.Answers, "an acknowledged answer is not sent again")
}

// connect is the world's node saying hello again, listing every Dispatch it
// holds (a node of this release).
func (w *world) connect(held []string, claims ...Claim) []Disposition {
	w.t.Helper()

	got, err := w.s.NodeConnect(w.t.Context(), w.node.ID, Hello{
		WorktreeRoot: "/srv/wt", Runtimes: []RuntimeInfo{{Runtime: "claude"}}, Held: held, ListsHeld: true,
	}, claims)
	require.NoError(w.t, err)

	return got
}

// slots is how many slots each node has taken.
func (w *world) slots() map[string]int64 {
	w.t.Helper()

	rows, err := store.New(w.db).CountActiveDispatchesPerNode(w.t.Context(), activeDispatchStatuses())
	require.NoError(w.t, err)

	out := map[string]int64{}
	for _, r := range rows {
		out[r.NodeID] = r.N
	}

	return out
}

// Scenario (issue #590): a node comes back no longer holding two of its
// Dispatches, one whose outcome became unknown while it was away and one
// still running. Both end lost, a counted failure, and free their slots; the
// Tasks wait to be tried again. The Dispatch whose accept the node has queued,
// and the one it claims, are left as they are.
func TestNodeConnectSettlesWhatTheNodeNoLongerHolds(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	tasks := w.tasks(specs(4)...)

	unknown, _ := w.start(tasks[0].ID)
	w.loseLease()

	running, _ := w.start(tasks[1].ID)
	accepting := w.dispatch(tasks[2].ID)
	w.accept(accepting)
	kept, _ := w.start(tasks[3].ID)
	require.Equal(t, map[string]int64{w.node.ID: 4}, w.slots())

	got := w.connect([]string{accepting.ID, kept.ID}, Claim{DispatchID: kept.ID, Epoch: 1})

	assert.Equal(t, []Disposition{{DispatchID: kept.ID, Kind: DispositionRetain}}, got)

	for _, id := range []string{unknown.ID, running.ID} {
		d := w.dispatchOf(id)
		assert.Equal(t, DispatchFailed, d.Status, id)
		assert.Equal(t, FailureLost, d.FailureClass, id)
		assert.Equal(t, ConversationClosed, w.conversation(d.ConversationID).Status, "not first-hand: %s", id)
	}

	assert.Equal(t, DispatchStarting, w.dispatchOf(accepting.ID).Status, "its queued accept settles it")
	assert.Equal(t, DispatchRunning, w.dispatchOf(kept.ID).Status)
	assert.Equal(t, map[string]int64{w.node.ID: 2}, w.slots())
	assert.Equal(t, TaskReady, w.task(tasks[0].ID).Status)
	assert.Equal(t, TaskReady, w.task(tasks[1].ID).Status)
}

// Scenario (issue #590, as on zm): a worker never starts, the node loses its
// lease and later forgets the Dispatch; the owner cancels the Task meanwhile.
// At the node's next hello the Dispatch ends lost and the cancel applies.
func TestNodeConnectAppliesAPendingCancelToALostDispatch(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	tasks := w.tasks(specs(1)...)

	d := w.dispatch(tasks[0].ID)
	w.accept(d)
	w.loseLease()
	require.NoError(t, w.s.CancelTask(t.Context(), tasks[0].ID))
	require.Equal(t, TaskBlocked, w.task(tasks[0].ID).Status, "the cancel waits for the unknown Dispatch")

	assert.Empty(t, w.connect([]string{}))

	assert.Equal(t, DispatchFailed, w.dispatchOf(d.ID).Status)
	assert.Equal(t, TaskCanceled, w.task(tasks[0].ID).Status)
	assert.Empty(t, w.slots())
}

// Scenario: a node of an earlier release, which does not list what it holds,
// says hello claiming nothing. Its Dispatches are left as they are: one of
// them may have a final report queued.
func TestNodeConnectWithoutTheHeldListSettlesNothing(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	tasks := w.tasks(specs(1)...)

	d, _ := w.start(tasks[0].ID)
	w.loseLease()

	got, err := w.s.NodeConnect(t.Context(), w.node.ID, Hello{
		WorktreeRoot: "/srv/wt", Runtimes: []RuntimeInfo{{Runtime: "claude"}},
	}, nil)
	require.NoError(t, err)

	assert.Empty(t, got)
	assert.Equal(t, DispatchUnknown, w.dispatchOf(d.ID).Status)
}

// Scenario (issue #590): the node claims a Dispatch whose outcome became
// unknown before its worker started. There is no process to reattach to, so
// it ends start_failed and the node is told to release it; the Task is
// tried again.
func TestNodeConnectEndsAClaimedDispatchThatNeverStarted(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	tasks := w.tasks(specs(1)...)

	d := w.dispatch(tasks[0].ID)
	w.accept(d)
	w.loseLease()

	got := w.connect([]string{d.ID}, Claim{DispatchID: d.ID, Epoch: 1})

	assert.Equal(t, []Disposition{{DispatchID: d.ID, Kind: DispositionRelease}}, got)
	assert.Equal(t, DispatchFailed, w.dispatchOf(d.ID).Status)
	assert.Equal(t, FailureStartFailed, w.dispatchOf(d.ID).FailureClass)
	assert.Equal(t, TaskReady, w.task(tasks[0].ID).Status)
	assert.Empty(t, w.slots())
}

// Scenario (issue #833, as on stc-hy-app): the control plane is down longer
// than a lease while a worker runs. The node says hello to the new process
// before the reaper runs; the Dispatch is retained and its lease renewed, so
// the next heartbeat is taken and the reaper leaves it running.
func TestARestartLongerThanTheLeaseLeavesARetainedDispatchRunning(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	tasks := w.tasks(specs(1)...)

	d, c := w.start(tasks[0].ID)
	w.advance(LeaseDuration + time.Minute)

	_, err := w.s.Heartbeat(t.Context(), w.again(c))
	require.ErrorIs(t, err, ErrConflict, "a lease that ran out is not renewed by a heartbeat")

	got := w.connect([]string{d.ID}, Claim{DispatchID: d.ID, Epoch: 1})
	assert.Equal(t, []Disposition{{DispatchID: d.ID, Kind: DispositionRetain}}, got)

	reaped := w.reap()
	assert.Empty(t, reaped.Unknown)
	assert.Equal(t, DispatchRunning, w.dispatchOf(d.ID).Status)

	w.advance(LeaseDuration / 2)
	b := w.heartbeat(c)
	assert.Equal(t, w.clock.now.Add(LeaseDuration), b.LeaseExpiresAt)

	w.advance(LeaseDuration / 2)
	assert.Empty(t, w.reap().Unknown)
	assert.Equal(t, DispatchRunning, w.dispatchOf(d.ID).Status)
	assert.Equal(t, TaskRunning, w.task(tasks[0].ID).Status)
}

// Scenario (issue #833): the reaper ran before the node came back, so the
// Dispatch is unknown when the node says hello. It is told to reattach, its
// reattach returns it to running, and nothing is left unknown. A Dispatch of
// the node that it no longer holds still ends lost.
func TestARestartLongerThanTheLeaseEndsWithAReattach(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	tasks := w.tasks(specs(2)...)

	d, c := w.start(tasks[0].ID)
	gone, _ := w.start(tasks[1].ID)
	w.advance(time.Minute)

	reaped := w.loseLease()
	assert.ElementsMatch(t, []string{d.ID, gone.ID}, reaped.Unknown)

	got := w.connect([]string{d.ID}, Claim{DispatchID: d.ID, Epoch: 1})
	assert.Equal(t, []Disposition{{DispatchID: d.ID, Kind: DispositionReattach}}, got)

	acc, err := w.s.ReattachDispatch(t.Context(), w.again(c), "pid:42@1")
	require.NoError(t, err)

	c.Epoch = acc.Epoch
	w.heartbeat(c)

	assert.Equal(t, DispatchRunning, w.dispatchOf(d.ID).Status)
	assert.Equal(t, TaskRunning, w.task(tasks[0].ID).Status)
	assert.Equal(t, DispatchFailed, w.dispatchOf(gone.ID).Status)
	assert.Equal(t, FailureLost, w.dispatchOf(gone.ID).FailureClass)
}

// Scenario: the implementer's profile caps each worker at 4 GiB (issue #1313 row 5). Its Dispatch's offer carries the
// limit, which the node makes the worker's scope's MemoryMax; a profile without one offers none.
func TestAProfilesMemoryLimitIsOffered(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	tasks := w.tasks(specs(1)...)
	plain := w.dispatch(tasks[0].ID)

	_, _, err := w.s.Profiles().ApplyProfile(t.Context(), "",
		"name: implementer\ndescription: d\nruntime: claude\nmodel: {provider: model-gateway}\nlimits: {memory: 4Gi}\n")
	require.NoError(t, err)

	more := w.tasks(specs(1)...)
	capped := w.dispatch(more[0].ID)

	p, err := w.s.NodePending(t.Context(), w.node.ID)
	require.NoError(t, err)

	limits := map[string]int64{}
	for _, o := range p.Offers {
		limits[o.DispatchID] = o.MemoryLimit
	}

	assert.Equal(t, map[string]int64{plain.ID: 0, capped.ID: 4 << 30}, limits)
}
