package orchestration

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The scenarios of scoring nodes by free capacity (issue #731): a new
// Conversation goes to the node with the highest mean of its free shares of
// slots, CPU and memory; stale stats rank last; a node low on memory takes
// none.

const gib = int64(1) << 30

// measure says what the node is and what it measured now: its memory, and
// the CPU and memory in use.
func (w *world) measure(n Node, memory int64, cpu float64, used int64) {
	w.t.Helper()

	require.NoError(w.t, w.s.NodeHello(w.t.Context(), n.ID, Hello{
		WorktreeRoot: "/srv/wt", System: SystemInfo{MemoryBytes: memory},
		Runtimes: []RuntimeInfo{{Runtime: "claude", Path: "/usr/local/bin/claude", Version: "1.0.0"}},
	}))
	require.NoError(w.t, w.s.NodeStats(w.t.Context(), n.ID, SystemStats{CPUPercent: cpu, MemoryUsedBytes: used}))
}

// newScoreWorld is a world whose own node is drained, so the nodes a
// scenario adds are the only ones that can run.
func newScoreWorld(t *testing.T) *world {
	t.Helper()

	w := newSchedWorld(t)
	require.NoError(t, w.s.DrainNode(t.Context(), w.node.ID))

	return w
}

// Scenario: zm on 2026-11-17. elitex (limit 1, about one core, 3.8 GiB,
// 99.8% CPU) and stc-hy-app (limit 4, 31.4 GiB, 0.1% CPU). elitex has the
// smaller id, and won the tie under the count of Dispatches; stc-hy-app has
// more free capacity, when both are idle and when it already runs one.
func TestPlacementZmCaseGoesToTheIdleBigNode(t *testing.T) {
	t.Parallel()

	w := newScoreWorld(t)
	elitex := w.addNode("elitex", 1, "claude")
	stc := w.addNode("stc-hy-app", 4, "claude")
	require.Less(t, elitex.ID, stc.ID, "elitex wins a tie by id")

	w.measure(elitex, 3891*gib/1024, 99.8, 1700*gib/1024)
	w.measure(stc, 31*gib+gib*4/10, 0.1, 6*gib)

	w.tasks(specs(1)...)
	assert.Equal(t, map[string]string{"a": stc.ID}, w.placed(w.schedule().Created), "0 of 1 against 0 of 4")

	w.tasks(NewTask{Ref: "b", Profile: "implementer", Spec: "b"})
	assert.Equal(t, map[string]string{"b": stc.ID}, w.placed(w.schedule().Created), "0 of 1 against 1 of 4")
}

// Scenario: nodes that differ only in what they measured or in their slots.
// The idler CPU wins; with equal shares, more free slots by count, then the
// smaller id.
func TestPlacementOrdersByScoreThenFreeSlotsThenID(t *testing.T) {
	t.Parallel()

	t.Run("the idler CPU", func(t *testing.T) {
		t.Parallel()

		w := newScoreWorld(t)
		busy := w.addNode("node_busy", 2, "claude")
		idle := w.addNode("node_idle", 2, "claude")
		w.measure(busy, 16*gib, 80, 4*gib)
		w.measure(idle, 16*gib, 10, 4*gib)

		w.tasks(specs(1)...)
		assert.Equal(t, map[string]string{"a": idle.ID}, w.placed(w.schedule().Created))
	})

	t.Run("more free slots", func(t *testing.T) {
		t.Parallel()

		w := newScoreWorld(t)
		small := w.addNode("node_small", 2, "claude")
		big := w.addNode("node_big", 4, "claude")
		w.measure(small, 16*gib, 10, 4*gib)
		w.measure(big, 16*gib, 10, 4*gib)

		w.tasks(specs(1)...)
		assert.Equal(t, map[string]string{"a": big.ID}, w.placed(w.schedule().Created), "2 free against 4 free")
	})

	t.Run("the smaller id", func(t *testing.T) {
		t.Parallel()

		w := newScoreWorld(t)
		first := w.addNode("node_first", 2, "claude")
		second := w.addNode("node_second", 2, "claude")
		w.measure(second, 16*gib, 10, 4*gib)
		w.measure(first, 16*gib, 10, 4*gib)

		w.tasks(specs(1)...)
		assert.Equal(t, map[string]string{"a": first.ID}, w.placed(w.schedule().Created))
	})
}

// Scenario: several Tasks in one pass on two equal nodes. Each placement
// counts at once, so they spread instead of filling the first node.
func TestPlacementSpreadsTasksInOnePass(t *testing.T) {
	t.Parallel()

	w := newScoreWorld(t)
	one := w.addNode("node_one", 4, "claude")
	two := w.addNode("node_two", 4, "claude")
	w.measure(one, 16*gib, 10, 4*gib)
	w.measure(two, 16*gib, 10, 4*gib)

	w.tasks(specs(4)...)
	assert.Equal(t, map[string]string{"a": one.ID, "b": two.ID, "c": one.ID, "d": two.ID},
		w.placed(w.schedule().Created))
}

// Scenario: stats older than NodeOfflineAfter, or never reported. Such a node
// counts 0 for CPU and memory: it loses to a node that reports room, even a
// busier one, and is still used when it is the only one that can run.
func TestPlacementRanksStaleStatsLast(t *testing.T) {
	t.Parallel()

	w := newScoreWorld(t)
	stale := w.addNode("node_stale", 4, "claude")
	never := w.addNode("node_never", 4, "claude")
	fresh := w.addNode("node_fresh", 1, "claude")
	w.measure(stale, 64*gib, 0, 0)

	w.advance(NodeOfflineAfter)
	w.alive(stale)
	w.alive(never)
	w.measure(fresh, 16*gib, 50, 8*gib)

	w.tasks(specs(1)...)
	assert.Equal(t, map[string]string{"a": fresh.ID}, w.placed(w.schedule().Created),
		"a node with fresh stats wins over an idle one whose stats are stale or missing")

	require.NoError(t, w.s.DrainNode(t.Context(), fresh.ID))
	require.NoError(t, w.s.DrainNode(t.Context(), never.ID))
	w.tasks(NewTask{Ref: "b", Profile: "implementer", Spec: "b"})
	assert.Equal(t, map[string]string{"b": stale.ID}, w.placed(w.schedule().Created),
		"stale stats are not refused")
}

// Scenario: a node with less than NodeMemoryFloor available. It takes no new
// Conversation while another can; when it is the only one, the Task waits
// with nodes_low_memory and is escalated after UnplaceableAfter. A
// Conversation already on it still continues there.
func TestPlacementSkipsANodeLowOnMemory(t *testing.T) {
	t.Parallel()

	w := newScoreWorld(t)
	low := w.addNode("node_low", 4, "claude")
	roomy := w.addNode("node_roomy", 1, "claude")
	w.measure(low, 16*gib, 0, 16*gib-NodeMemoryFloor+1)
	w.measure(roomy, 16*gib, 90, 8*gib)

	w.tasks(specs(1)...)
	assert.Equal(t, map[string]string{"a": roomy.ID}, w.placed(w.schedule().Created),
		"a busier node with memory wins over an idle one without")

	b := w.tasks(NewTask{Ref: "b", Profile: "implementer", Spec: "b"})[0]
	assert.Empty(t, w.schedule().Created, "node_roomy is full, node_low has no memory")
	assert.Equal(t, ReasonNodesFull, w.task(b.ID).WaitingReason, "a full node frees by itself")

	require.NoError(t, w.s.DrainNode(t.Context(), roomy.ID))
	assert.Empty(t, w.schedule().Created)
	assert.Equal(t, ReasonNodesLowMemory, w.task(b.ID).WaitingReason)
	assert.Empty(t, w.schedule().Escalations, "not yet")

	w.advance(UnplaceableAfter)
	w.alive(low)
	w.measure(low, 16*gib, 0, 16*gib-NodeMemoryFloor+1)

	out := w.schedule()
	require.Len(t, out.Escalations, 1)
	assert.Equal(t, ReasonNodesLowMemory, out.Escalations[0].Reason)
	assert.Empty(t, w.schedule().Escalations, "escalated once")

	w.measure(low, 16*gib, 0, 16*gib-NodeMemoryFloor)
	assert.Equal(t, map[string]string{"b": low.ID}, w.placed(w.schedule().Created),
		"exactly the floor available is enough")
}

// Scenario: a Conversation open on a node that has since run low on memory.
// It continues there: its worktree is on that node.
func TestPlacementContinuesOnANodeLowOnMemory(t *testing.T) {
	t.Parallel()

	w := newSchedWorld(t)
	task := w.tasks(specs(1)...)[0]
	first := w.failOnce(task.ID, FailureWorkerError)

	w.advance(BackoffBase)
	w.measure(w.node, 16*gib, 0, 16*gib-NodeMemoryFloor/2)

	created := w.schedule().Created
	require.Len(t, created, 1)
	assert.Equal(t, w.node.ID, w.dispatchOf(created[0]).NodeID)
	assert.Equal(t, first.ConversationID, w.dispatchOf(created[0]).ConversationID)
}
