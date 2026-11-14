package orchestration

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	assert.Equal(t, GatewayAccountRef, p.Offers[0].AccountRef)

	assert.Equal(t, []StopRequest{{DispatchID: stopping.ID, Epoch: 1}}, p.Stops)

	require.Len(t, p.Answers, 1)
	assert.Equal(t, question, p.Answers[0].ID)

	require.NoError(t, w.s.AckAnswer(t.Context(), w.again(waiting), question))

	p, err = w.s.NodePending(t.Context(), w.node.ID)
	require.NoError(t, err)
	assert.Empty(t, p.Answers, "an acknowledged answer is not sent again")
}
