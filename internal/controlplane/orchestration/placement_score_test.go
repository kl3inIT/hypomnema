package orchestration

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The scenarios of scoring nodes by free capacity (issue #731): a new
// Conversation goes to the node with the highest mean of its free share by
// requests and its measured free CPU and memory; stale stats rank last; a
// node low on memory takes none. The requests term has its own scenarios
// (capacity_test.go).

const gib = int64(1) << 30

// measure says what the node is and what it measured now: its memory, with 8
// CPUs, and the CPU and memory in use.
func (w *world) measure(n Node, memory int64, cpu float64, used int64) {
	w.t.Helper()

	require.NoError(w.t, w.s.NodeHello(w.t.Context(), n.ID, Hello{
		WorktreeRoot: "/srv/wt", System: SystemInfo{CPUs: 8, MemoryBytes: memory},
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

func TestPlacementRanksDownNodesWithTaskExpiries(t *testing.T) {
	t.Parallel()

	for _, scenario := range []struct {
		name  string
		bFits bool
		after time.Duration
		wantB bool
	}{
		{name: "B fits", bFits: true, wantB: true},
		{name: "only A fits"},
		{name: "expiry is outside the window", bFits: true, after: TaskExpiryPenaltyWindow + time.Millisecond},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()

			w := newSchedWorld(t)
			nodeA := w.node
			nodeB := w.addNode("node_b", 2, "claude")
			w.measure(nodeA, 16*gib, 10, 4*gib)
			w.measure(nodeB, 16*gib, 80, 4*gib)
			task := w.tasks(specs(1)...)[0]

			first := w.schedule().Created
			require.Equal(t, map[string]string{task.Spec: nodeA.ID}, w.placed(first))
			w.advance(QueueTimeout)
			require.Equal(t, first, w.reap().Expired)

			w.advance(scenario.after)
			w.measure(nodeA, 16*gib, 10, 4*gib)
			w.measure(nodeB, 16*gib, 80, 4*gib)
			if !scenario.bFits {
				require.NoError(t, w.s.DrainNode(t.Context(), nodeB.ID))
			}

			wantNode := nodeA.ID
			if scenario.wantB {
				wantNode = nodeB.ID
			}
			assert.Equal(t, map[string]string{task.Spec: wantNode}, w.placed(w.schedule().Created))
		})
	}

	// Each expired Task ranks its own node down for the next pass, not only
	// the last: A and B each take their turn before C, which never expired.
	t.Run("every node expired once", func(t *testing.T) {
		t.Parallel()

		w := newSchedWorld(t)
		nodeA := w.node
		nodeB := w.addNode("node_b", 2, "claude")
		nodeC := w.addNode("node_c", 2, "claude")
		task := w.tasks(specs(1)...)[0]

		for _, wantNode := range []Node{nodeA, nodeB} {
			w.measure(nodeA, 16*gib, 10, 4*gib)
			w.measure(nodeB, 16*gib, 40, 4*gib)
			w.measure(nodeC, 16*gib, 80, 4*gib)
			created := w.schedule().Created
			require.Equal(t, map[string]string{task.Spec: wantNode.ID}, w.placed(created))
			w.advance(QueueTimeout)
			require.Equal(t, created, w.reap().Expired)
		}

		w.measure(nodeA, 16*gib, 10, 4*gib)
		w.measure(nodeB, 16*gib, 40, 4*gib)
		w.measure(nodeC, 16*gib, 80, 4*gib)
		assert.Equal(t, map[string]string{task.Spec: nodeC.ID}, w.placed(w.schedule().Created))
	})
}

// Scenario: zm on 2026-11-17. elitex (cap 1, 3.8 GiB, 99.8% CPU) and
// stc-hy-app (cap 4, 31.4 GiB, 0.1% CPU). elitex has the smaller id, and won
// the tie under the count of Dispatches; stc-hy-app has more free capacity,
// when both are idle and when it already runs one (and elitex no longer fits
// a building worker at all: capacity_test.go).
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

// Scenario: nodes that differ only in what they measured. The idler CPU wins;
// with equal shares, the smaller id.
func TestPlacementOrdersByScoreThenID(t *testing.T) {
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
