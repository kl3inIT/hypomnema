package orchestration

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The limits of issue #660: no count across every node; each Bot's Runs, and
// the owner's Runs without a Bot, run up to their own limit, each node up to
// its own, and a Task the pass leaves ready says why it waits.

// botRun is a Run a Bot's pi made, with n Tasks of the implementer profile,
// their specs prefix-a, prefix-b and so on.
func botRun(w *world, bot Bot, prefix string, n int) []Task {
	w.t.Helper()

	run, err := w.s.CreateRun(w.t.Context(), NewRun{Objective: prefix, CreatedBy: "pi", Bot: bot.ID})
	require.NoError(w.t, err)

	nts := specs(n)
	for i := range nts {
		nts[i].Spec = prefix + "-" + nts[i].Spec
	}

	tasks, err := w.s.CreateTasks(w.t.Context(), run.ID, nts)
	require.NoError(w.t, err)

	return tasks
}

// waiting is why each of the Run's Tasks with these specs waits, in order.
func waiting(w *world, runID string, specs ...string) []string {
	w.t.Helper()

	tasks, err := w.s.ListTasks(w.t.Context(), runID)
	require.NoError(w.t, err)

	out := make([]string, 0, len(specs))

	for _, spec := range specs {
		for _, t := range tasks {
			if t.Spec == spec {
				out = append(out, t.WaitingReason)
			}
		}
	}

	return out
}

// Scenario: two Bots with the default limit and two nodes with room for 4
// each. Both Bots run 4 at once, 8 in all, while their fifth Tasks wait with
// their own Bot's reason; a Task of the owner's Runs, a group of its own still
// under its limit, waits because every node is full, which is not escalated.
// The owner gives node_a room for one more and the next pass places that Task
// and clears its reason; the Bots' fifth Tasks still wait for their Bot.
func TestEachBotRunsUpToItsOwnLimit(t *testing.T) {
	t.Parallel()

	w := newSchedWorld(t)
	ctx := t.Context()
	nodeB := w.addNode("node_b", 4, "claude")

	vmap, err := w.s.PutBot(ctx, Bot{Name: "vmap"})
	require.NoError(t, err)
	ops, err := w.s.PutBot(ctx, Bot{Name: "ops"})
	require.NoError(t, err)

	vmapTasks := botRun(w, vmap, "vmap", 5)
	opsTasks := botRun(w, ops, "ops", 5)

	created := w.schedule().Created
	require.Len(t, created, 8, "no limit across every node holds them back")

	perBot := map[string]int{}
	for spec := range w.placed(created) {
		perBot[spec[:3]]++
	}

	assert.Equal(t, map[string]int{"vma": 4, "ops": 4}, perBot)
	assert.Equal(t, ReasonBotAtLimit+vmap.ID, w.task(vmapTasks[4].ID).WaitingReason)
	assert.Equal(t, ReasonBotAtLimit+ops.ID, w.task(opsTasks[4].ID).WaitingReason)
	assert.Empty(t, w.task(vmapTasks[0].ID).WaitingReason, "a Task that runs does not wait")

	mine := w.tasks(NewTask{Ref: "m", Profile: "implementer", Spec: "mine"})[0]
	assert.Empty(t, w.schedule().Created)
	assert.Equal(t, ReasonNodesFull, w.task(mine.ID).WaitingReason)

	queued := w.task(mine.ID)

	w.advance(UnplaceableAfter)
	w.alive(w.node)
	w.alive(nodeB)

	out := w.schedule()
	assert.Empty(t, out.Created)
	assert.Empty(t, out.Escalations, "a full fleet and a Bot at its limit resolve by themselves")
	assert.Equal(t, queued.UpdatedAt, w.task(mine.ID).UpdatedAt, "the reason leaves updated_at as it was")

	_, err = w.s.SetNodeLimit(ctx, w.node.ID, 5)
	require.NoError(t, err)

	next := w.schedule().Created
	require.Len(t, next, 1, "the new limit holds from the next pass")
	assert.Equal(t, mine.ID, w.dispatchOf(next[0]).TaskID)
	assert.Equal(t, w.node.ID, w.dispatchOf(next[0]).NodeID)
	assert.Empty(t, w.task(mine.ID).WaitingReason, "cleared when its Dispatch is made")
	assert.Equal(t, ReasonBotAtLimit+vmap.ID, w.task(vmapTasks[4].ID).WaitingReason)
}

// Scenario: a Bot set to run one worker at once. Its second Task waits while
// the node has room, and the coordinator's own CreateDispatch for it is
// refused too; once the first settles, the second runs. A limit out of range
// is refused, and a Bot keeps its number across a change of another setting.
func TestABotsOwnLimitHoldsForTheSchedulerAndCreateDispatch(t *testing.T) {
	t.Parallel()

	w := newSchedWorld(t)
	ctx := t.Context()

	_, err := w.s.PutBot(ctx, Bot{Name: "eager", MaxDispatches: MaxDispatchesLimit + 1})
	require.ErrorIs(t, err, ErrInvalid)

	solo, err := w.s.PutBot(ctx, Bot{Name: "solo", MaxDispatches: 1})
	require.NoError(t, err)
	assert.Equal(t, 1, solo.DispatchLimit())

	solo.Persona = "One at a time."
	solo, err = w.s.PutBot(ctx, solo)
	require.NoError(t, err)
	assert.Equal(t, 1, solo.DispatchLimit(), "kept across another change")

	tasks := botRun(w, solo, "solo", 2)

	first := w.schedule().Created
	require.Len(t, first, 1)
	assert.Equal(t, ReasonBotAtLimit+solo.ID, w.task(tasks[1].ID).WaitingReason)

	_, err = w.create(tasks[1].ID, w.spec())
	require.ErrorIs(t, err, ErrUnavailable, "the node has room; the Bot does not")

	require.NoError(t, w.finish(w.begin(first[0]), success("done")))

	next := w.schedule().Created
	require.Len(t, next, 1)
	assert.Equal(t, tasks[1].ID, w.dispatchOf(next[0]).TaskID)
}

// Scenario: changing a node's limit. The owner sets it within 1 to
// MaxDispatchesLimit; a number out of range or a node that is not there is
// refused.
func TestTheOwnerChangesANodesLimit(t *testing.T) {
	t.Parallel()

	w := newSchedWorld(t)
	ctx := t.Context()

	n, err := w.s.SetNodeLimit(ctx, w.node.ID, 6)
	require.NoError(t, err)
	assert.Equal(t, 6, n.MaxDispatches)

	_, err = w.s.SetNodeLimit(ctx, w.node.ID, 0)
	require.ErrorIs(t, err, ErrInvalid)
	_, err = w.s.SetNodeLimit(ctx, w.node.ID, MaxDispatchesLimit+1)
	require.ErrorIs(t, err, ErrInvalid)
	_, err = w.s.SetNodeLimit(ctx, "nod_missing", 2)
	require.ErrorIs(t, err, ErrNotFound)
}
