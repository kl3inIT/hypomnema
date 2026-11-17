package orchestration

import (
	"database/sql"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
)

// The scenarios of capacity by requests and of sharing room between Bots
// (issue #731, second design): a node takes a Dispatch while the requests of
// what it runs, plus this one, fit its allocatable; max_dispatches is an
// optional cap; each Bot has a guaranteed share, borrows free room beyond it,
// and room that frees goes first to the Bot furthest below its guarantee.
// Nothing running is stopped.

// sized is a node that said its CPUs and memory, with no cap, as a node does
// since its hello carries them.
func (w *world) sized(name string, cpus int, memory int64) Node {
	w.t.Helper()

	n := w.addNode(name, 0)
	w.resize(n, cpus, memory)

	return n
}

// resize says the node's CPUs and memory again, as its next hello does.
func (w *world) resize(n Node, cpus int, memory int64) {
	w.t.Helper()

	require.NoError(w.t, w.s.NodeHello(w.t.Context(), n.ID, Hello{
		WorktreeRoot: "/srv/wt", System: SystemInfo{CPUs: cpus, MemoryBytes: memory},
		Runtimes: []RuntimeInfo{{Runtime: "claude", Path: "/usr/local/bin/claude", Version: "1.0.0"}},
	}))
}

// light is a profile that reads and does not build: half a core, 512 MiB, as
// the seeded explorer asks.
func (w *world) light() {
	w.t.Helper()

	_, _, err := w.s.ApplyProfile(w.t.Context(), "", "name: light\ndescription: d\nruntime: claude\n"+
		"model: {provider: model-gateway}\nrequests: {cpu: 500m, memory: 512Mi}\n")
	require.NoError(w.t, err)
}

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

// node is the node as the registry shows it now.
func (w *world) nodeNow(id string) Node {
	w.t.Helper()

	n, err := w.s.GetNode(w.t.Context(), id)
	require.NoError(w.t, err)

	return n
}

// Scenario: the fit rule. A node with 4 CPUs and 8 GiB has 7 GiB allocatable
// (the 1 GiB floor folded in, no reserve). Two building workers fit, 6 GiB; a
// third waits nodes_full, which is not escalated; a light worker still fits
// beside them. The registry shows what is requested against what is
// allocatable.
func TestANodeTakesWhatFitsItsAllocatable(t *testing.T) {
	t.Parallel()

	w := newScoreWorld(t)
	w.light()
	n := w.sized("node_fit", 4, 8*gib)

	tasks := w.tasks(specs(3)...)
	created := w.schedule().Created
	require.Len(t, created, 2)
	assert.Equal(t, ReasonNodesFull, w.task(tasks[2].ID).WaitingReason)

	got := w.nodeNow(n.ID)
	assert.Equal(t, Resources{CPUMillis: 4000, MemoryBytes: 7 * gib}, got.Allocatable)
	assert.Equal(t, Resources{CPUMillis: 2 * DefaultCPURequestMillis, MemoryBytes: 2 * DefaultMemoryRequest},
		got.Requested)
	assert.Equal(t, 0, got.MaxDispatches, "no cap")

	light := w.tasks(NewTask{Ref: "l", Profile: "light", Spec: "l"})[0]
	assert.Equal(t, map[string]string{"l": n.ID}, w.placed(w.schedule().Created), "6.5 GiB of 7 fit")
	assert.Equal(t, int64(2*DefaultMemoryRequest+512<<20), w.nodeNow(n.ID).Requested.MemoryBytes)
	assert.Empty(t, w.task(light.ID).WaitingReason)

	w.advance(UnplaceableAfter)
	w.alive(n)
	assert.Empty(t, w.schedule().Escalations, "a full node frees by itself")
}

// Scenario: zm on 2026-11-17 by requests. elitex (1 CPU, 3.8 GiB) has 2.8 GiB
// allocatable, so a building worker (3 GiB) does not fit there and goes to
// stc-hy-app. With elitex the only node, it waits no_node_fits and is
// escalated after UnplaceableAfter; a light worker still goes to elitex.
func TestARequestLargerThanEveryNodeWaitsAndIsEscalated(t *testing.T) {
	t.Parallel()

	w := newScoreWorld(t)
	w.light()
	elitex := w.sized("elitex", 1, 3891*gib/1024)
	stc := w.sized("stc-hy-app", 24, 31*gib+gib*4/10)

	w.tasks(specs(1)...)
	assert.Equal(t, map[string]string{"a": stc.ID}, w.placed(w.schedule().Created))

	require.NoError(t, w.s.DrainNode(t.Context(), stc.ID))

	b := w.tasks(NewTask{Ref: "b", Profile: "implementer", Spec: "b"})[0]
	out := w.schedule()
	assert.Empty(t, out.Created)
	assert.Equal(t, ReasonNoNodeFits, w.task(b.ID).WaitingReason)
	assert.Empty(t, out.Escalations, "not yet")

	w.advance(UnplaceableAfter)
	w.alive(elitex)

	out = w.schedule()
	require.Len(t, out.Escalations, 1)
	assert.Equal(t, ReasonNoNodeFits, out.Escalations[0].Reason)
	assert.Empty(t, w.schedule().Escalations, "escalated once")

	w.tasks(NewTask{Ref: "l", Profile: "light", Spec: "l"})
	assert.Equal(t, map[string]string{"l": elitex.ID}, w.placed(w.schedule().Created))
}

// Scenario: the optional cap. With none, only the requests limit; set to 1,
// a second Dispatch waits nodes_full even with room; cleared again, it runs. A
// cap out of range is refused.
func TestTheCapIsOptionalAndStillHonoured(t *testing.T) {
	t.Parallel()

	w := newScoreWorld(t)
	ctx := t.Context()
	n := w.sized("node_cap", 16, 32*gib)

	_, err := w.s.SetNodeLimit(ctx, n.ID, 1)
	require.NoError(t, err)

	tasks := w.tasks(specs(3)...)
	require.Len(t, w.schedule().Created, 1)
	assert.Equal(t, []string{ReasonNodesFull, ReasonNodesFull}, waiting(w, w.run.ID, "b", "c"))

	cleared, err := w.s.SetNodeLimit(ctx, n.ID, 0)
	require.NoError(t, err)
	assert.Equal(t, 0, cleared.MaxDispatches)

	assert.Len(t, w.schedule().Created, 2, "the requests alone limit it")
	assert.Empty(t, w.task(tasks[2].ID).WaitingReason)

	_, err = w.s.SetNodeLimit(ctx, n.ID, -1)
	require.ErrorIs(t, err, ErrInvalid)
	_, err = w.s.SetNodeLimit(ctx, n.ID, MaxDispatchesLimit+1)
	require.ErrorIs(t, err, ErrInvalid)
	_, err = w.s.SetNodeLimit(ctx, "nod_missing", 2)
	require.ErrorIs(t, err, ErrNotFound)
}

// Scenario: a node that never said its CPUs and memory has no capacity: it
// takes work only under a cap, as the scheduler's older scenarios use it.
func TestANodeOfUnknownSizeTakesWorkOnlyUnderACap(t *testing.T) {
	t.Parallel()

	w := newScoreWorld(t)
	n := w.addNode("node_unsized", 0, "claude")

	a := w.tasks(specs(1)...)[0]
	assert.Empty(t, w.schedule().Created)
	assert.Equal(t, ReasonNoNodeFits, w.task(a.ID).WaitingReason)

	_, err := w.s.SetNodeLimit(t.Context(), n.ID, 1)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"a": n.ID}, w.placed(w.schedule().Created))
}

// Scenario: the owner keeps some of a node back. reserved_memory_bytes
// shrinks its allocatable, so a second building worker no longer fits; a
// negative reserve is refused.
func TestAReserveShrinksAllocatable(t *testing.T) {
	t.Parallel()

	w := newScoreWorld(t)
	ctx := t.Context()
	n := w.sized("node_reserved", 8, 8*gib)

	got, err := w.s.SetNodeReserved(ctx, n.ID, Resources{CPUMillis: 1000, MemoryBytes: 2 * gib})
	require.NoError(t, err)
	assert.Equal(t, Resources{CPUMillis: 1000, MemoryBytes: 2 * gib}, got.Reserved)
	assert.Equal(t, Resources{CPUMillis: 7000, MemoryBytes: 5 * gib}, got.Allocatable)

	w.tasks(specs(2)...)
	require.Len(t, w.schedule().Created, 1, "3 GiB of 5 fit, 6 do not")
	assert.Equal(t, []string{ReasonNodesFull}, waiting(w, w.run.ID, "b"))

	_, err = w.s.SetNodeReserved(ctx, n.ID, Resources{MemoryBytes: -1})
	require.ErrorIs(t, err, ErrInvalid)

	_, err = w.s.SetNodeReserved(ctx, n.ID, Resources{CPUMillis: 100_000, MemoryBytes: 100 * gib})
	require.NoError(t, err)
	assert.Equal(t, Resources{}, w.nodeNow(n.ID).Allocatable, "never below 0")
}

// Scenario: the owner keeps back more than any node has (PR #759). capacity
// - reserve - NodeMemoryFloor would wrap around int64 and leave a large
// allocatable; it is 0 instead, and no building worker is placed there.
func TestAHugeReserveLeavesNoRoom(t *testing.T) {
	t.Parallel()

	w := newScoreWorld(t)
	n := w.sized("node_huge_reserve", 8, 512<<20)

	got, err := w.s.SetNodeReserved(t.Context(), n.ID, Resources{CPUMillis: math.MaxInt64, MemoryBytes: math.MaxInt64})
	require.NoError(t, err)
	assert.Equal(t, Resources{}, got.Allocatable)

	w.tasks(specs(1)...)
	assert.Empty(t, w.schedule().Created)
}

// Scenario: one part of a node's reserve is changed alone (PR #759). The
// other part keeps what the node has when the change is written, even when
// it changed after the caller read the node; a change with a part that is
// refused writes none of its parts.
func TestAReservePartLeftOutIsKept(t *testing.T) {
	t.Parallel()

	w := newScoreWorld(t)
	ctx := t.Context()
	n := w.sized("node_partial", 8, 8*gib)

	_, err := w.s.SetNodeReserved(ctx, n.ID, Resources{CPUMillis: 500, MemoryBytes: 2 * gib})
	require.NoError(t, err)

	read := w.nodeNow(n.ID)

	cpu := int64(1000)
	_, err = w.s.UpdateNode(ctx, n.ID, NodeChange{ReservedCPUMillis: &cpu})
	require.NoError(t, err, "another change, after the node was read")

	memory := 4 * gib
	got, err := w.s.UpdateNode(ctx, read.ID, NodeChange{ReservedMemoryBytes: &memory})
	require.NoError(t, err)
	assert.Equal(t, Resources{CPUMillis: 1000, MemoryBytes: 4 * gib}, got.Reserved)

	limit, name, negative := 3, "renamed", int64(-1)
	_, err = w.s.UpdateNode(ctx, n.ID, NodeChange{Name: &name, MaxDispatches: &limit, ReservedCPUMillis: &negative})
	require.ErrorIs(t, err, ErrInvalid)

	after := w.nodeNow(n.ID)
	assert.Equal(t, [3]any{read.Name, 0, Resources{CPUMillis: 1000, MemoryBytes: 4 * gib}},
		[3]any{after.Name, after.MaxDispatches, after.Reserved}, "nothing was written")
}

// Scenario: a Dispatch keeps the request its profile had when it was made.
// The profile asks for more while it runs; the node still counts the old
// request, and the next Dispatch asks the new one.
func TestADispatchKeepsItsRequest(t *testing.T) {
	t.Parallel()

	w := newScoreWorld(t)
	n := w.sized("node_snap", 8, 16*gib)

	w.tasks(specs(1)...)
	require.Len(t, w.schedule().Created, 1)

	_, _, err := w.s.ApplyProfile(t.Context(), "", "name: implementer\ndescription: d\nruntime: claude\n"+
		"model: {provider: model-gateway}\nrequests: {cpu: 2, memory: 6Gi}\n")
	require.NoError(t, err)

	assert.Equal(t, Resources{CPUMillis: DefaultCPURequestMillis, MemoryBytes: DefaultMemoryRequest},
		w.nodeNow(n.ID).Requested)

	w.tasks(NewTask{Ref: "b", Profile: "implementer", Spec: "b"})
	require.Len(t, w.schedule().Created, 1)
	assert.Equal(t, Resources{CPUMillis: 3000, MemoryBytes: DefaultMemoryRequest + 6*gib}, w.nodeNow(n.ID).Requested)
}

// Scenario: a Conversation that continues runs on its own node, where its
// worktree is, and counts its request there: while that node is full it
// waits nodes_full rather than move, even with another node free.
func TestAContinuingConversationMustFitItsOwnNode(t *testing.T) {
	t.Parallel()

	w := newSchedWorld(t)
	own := w.node
	w.resize(own, 4, 4*gib) // room for one building worker

	task := w.tasks(specs(1)...)[0]
	first := w.failOnce(task.ID, FailureWorkerError)
	require.Equal(t, own.ID, first.NodeID)

	w.tasks(NewTask{Ref: "b", Profile: "implementer", Spec: "b"})
	assert.Equal(t, map[string]string{"b": own.ID}, w.placed(w.schedule().Created), "it fits while a backs off")

	w.sized("node_other", 16, 32*gib)
	w.advance(BackoffMax)
	w.alive(own)

	assert.Empty(t, w.schedule().Created)
	assert.Equal(t, ReasonNodesFull, w.task(task.ID).WaitingReason)
}

// Scenario: the score's requests term. With equal measured stats, the node
// with more free room by requests wins; equal scores go to the node with more
// allocatable memory left, then to the smaller id.
func TestPlacementScoresFreeRoomByRequests(t *testing.T) {
	t.Parallel()

	t.Run("more free room", func(t *testing.T) {
		t.Parallel()

		w := newScoreWorld(t)
		small := w.sized("node_small", 8, 9*gib)
		big := w.sized("node_big", 8, 17*gib)
		w.measure(small, 9*gib, 10, 0)
		w.measure(big, 17*gib, 10, 0)

		w.tasks(specs(1)...)
		assert.Equal(t, map[string]string{"a": big.ID}, w.placed(w.schedule().Created))
	})

	t.Run("more memory left", func(t *testing.T) {
		t.Parallel()

		// node_half (4 CPUs, 8 GiB allocatable, empty) and node_double (8 CPUs, 16 GiB, one building worker)
		// have the same free share with the next one: 1/4 of the CPU and 3/8 of the memory requested. node_double
		// has 13 GiB left against 8.
		w := newScoreWorld(t)
		double := w.sized("node_double", 8, 17*gib)
		w.tasks(specs(1)...)
		require.Equal(t, map[string]string{"a": double.ID}, w.placed(w.schedule().Created))

		half := w.sized("node_half", 4, 9*gib)
		w.measure(half, 9*gib, 10, 0)
		w.measure(double, 17*gib, 10, 0)
		w.resize(half, 4, 9*gib)
		w.resize(double, 8, 17*gib)

		w.tasks(NewTask{Ref: "b", Profile: "implementer", Spec: "b"})
		assert.Equal(t, map[string]string{"b": double.ID}, w.placed(w.schedule().Created))
	})

	t.Run("the smaller id", func(t *testing.T) {
		t.Parallel()

		w := newScoreWorld(t)
		first := w.sized("node_first", 8, 16*gib)
		second := w.sized("node_second", 8, 16*gib)
		require.Less(t, first.ID, second.ID)

		w.tasks(specs(1)...)
		assert.Equal(t, map[string]string{"a": first.ID}, w.placed(w.schedule().Created))
	})
}

// Scenario (the review of PR #752, P2): a node whose hello did not say its
// memory, with fresh stats at 0 % CPU, counts 0 for its measured CPU too, so
// it does not outrank a node with 16 GiB, 12 GiB used and 80 % CPU.
func TestUnknownMemoryCountsNothingMeasured(t *testing.T) {
	t.Parallel()

	now := int64(1_000_000)
	fresh := sql.NullInt64{Int64: now, Valid: true}
	p := &placement{now: now}

	unknown := &nodeLoad{row: store.Node{
		ID: "nod_a", StatsAt: fresh, CpuPercent: sql.NullFloat64{Float64: 0, Valid: true},
		MemoryUsedBytes: sql.NullInt64{Int64: gib, Valid: true},
	}}
	known := &nodeLoad{row: store.Node{
		ID: "nod_b", StatsAt: fresh, CpuPercent: sql.NullFloat64{Float64: 80, Valid: true},
		MemoryBytes:     sql.NullInt64{Int64: 16 * gib, Valid: true},
		MemoryUsedBytes: sql.NullInt64{Int64: 12 * gib, Valid: true},
	}}

	assert.Zero(t, p.measured(unknown), "neither its CPU nor its memory counts")
	assert.InDelta(t, 0.2+0.25, p.measured(known), 1e-9)
	assert.True(t, p.better(known, unknown, request{}), "nod_b wins although nod_a has the smaller id")
}

// Scenario: the default request of each profile. A profile that asks for
// nothing gets the building default; the seeded explorer asks for 0.5 core
// and 512 MiB from the migration, the implementer and the reviewer nothing;
// a profile's YAML may ask for its own, and a request that cannot be read is
// refused.
func TestEachProfileHasARequest(t *testing.T) {
	t.Parallel()

	w := newSeedWorld(t)
	q := store.New(w.db)
	ctx := t.Context()

	for profile, want := range map[string]request{
		"implementer": {cpuMillis: 1000, memoryBytes: 3 << 30},
		"reviewer":    {cpuMillis: 1000, memoryBytes: 3 << 30},
		"explorer":    {cpuMillis: 500, memoryBytes: 512 << 20},
	} {
		t.Run(profile, func(t *testing.T) {
			t.Parallel()

			v, err := q.CurrentProfileVersion(t.Context(), profile)
			require.NoError(t, err)
			assert.Equal(t, want, requestOf(v))
		})
	}

	explorer, err := w.s.GetProfile(ctx, "explorer")
	require.NoError(t, err)
	assert.Equal(t, [2]int64{500, 512 << 20}, [2]int64{explorer.CPURequestMillis, explorer.MemoryRequestBytes})

	p, _, err := w.s.ApplyProfile(ctx, "", "name: heavy\ndescription: d\nruntime: claude\n"+
		"model: {provider: claude-subscription}\nrequests: {cpu: 1.5, memory: 6Gi}\n")
	require.NoError(t, err)
	assert.Equal(t, [2]int64{1500, 6 << 30}, [2]int64{p.CPURequestMillis, p.MemoryRequestBytes})

	p, _, err = w.s.ApplyProfile(ctx, "", "name: half\ndescription: d\nruntime: claude\n"+
		"model: {provider: claude-subscription}\nrequests: {memory: 1G}\n")
	require.NoError(t, err)
	assert.Equal(t, [2]int64{0, 1_000_000_000}, [2]int64{p.CPURequestMillis, p.MemoryRequestBytes},
		"the CPU left out is the default")

	for _, bad := range []string{"{cpu: 0}", "{cpu: -1}", "{cpu: lots}", "{memory: 0}", "{memory: 3Qi}", "{cpu: 0m}"} {
		t.Run(bad, func(t *testing.T) {
			t.Parallel()

			_, _, err := w.s.ApplyProfile(t.Context(), "", "name: bad\ndescription: d\nruntime: claude\n"+
				"model: {provider: claude-subscription}\nrequests: "+bad+"\n")
			require.ErrorIs(t, err, ErrInvalid)
		})
	}
}

// Scenario: the owner's two cases of sharing (issue #731). The coordinator
// is guaranteed 8 and another Bot 3, on room for 11 building workers. With
// the other Bot idle, the coordinator borrows and runs 11. When the other
// Bot's work arrives nothing is stopped; each freed place goes to it (0/3 is
// below 10/8) until it has 3, and after that to the coordinator (7/8 below
// 3/3).
func TestBotsShareTheRoomByTheirGuarantees(t *testing.T) {
	t.Parallel()

	w := newScoreWorld(t)
	ctx := t.Context()
	// 11 building workers: 11 × 3 GiB in 34 - 1 GiB, 11 cores of 24.
	w.sized("node_room", 24, 34*gib)

	coord, err := w.s.PutBot(ctx, Bot{Name: "coord", GuaranteedDispatches: 8})
	require.NoError(t, err)
	other, err := w.s.PutBot(ctx, Bot{Name: "other", GuaranteedDispatches: 3})
	require.NoError(t, err)

	coordTasks := botRun(w, coord, "coord", 12)
	running := w.schedule().Created
	require.Len(t, running, 11, "the coordinator borrows the room the other Bot does not use")
	assert.Equal(t, ReasonNodesFull, w.task(coordTasks[11].ID).WaitingReason, "no Bot limit any more")

	otherTasks := botRun(w, other, "other", 4)
	assert.Empty(t, w.schedule().Created, "nothing is stopped to make room")
	assert.Equal(t, ReasonNodesFull, w.task(otherTasks[0].ID).WaitingReason)

	for _, id := range running {
		d := w.dispatchOf(id)
		assert.False(t, d.Settled(), "a running worker is never stopped")
		assert.Nil(t, d.StopRequestedAt)
	}

	settle := func(i int) string {
		t.Helper()

		require.NoError(t, w.finish(w.begin(running[i]), success("done")))

		next := w.schedule().Created
		require.Len(t, next, 1)

		return w.task(w.dispatchOf(next[0]).TaskID).Spec
	}

	assert.Equal(t, "other-a", settle(0), "0/3 goes before 10/8")
	assert.Equal(t, "other-b", settle(1), "1/3 goes before 9/8")
	assert.Equal(t, "other-c", settle(2), "2/3 goes before 8/8")
	assert.Equal(t, "coord-l", settle(3), "7/8 goes before 3/3")
	assert.Equal(t, ReasonNodesFull, w.task(otherTasks[3].ID).WaitingReason)
}

// Scenario: best effort. One Bot's oldest Task asks for more than any node
// has: it waits no_node_fits, and in the same pass another Bot's Task and the
// first Bot's next Task are placed, as Kueue's BestEffortFIFO.
func TestATaskThatFitsNowhereDoesNotBlockOthers(t *testing.T) {
	t.Parallel()

	w := newScoreWorld(t)
	ctx := t.Context()
	w.sized("node_room", 8, 16*gib)

	_, _, err := w.s.ApplyProfile(ctx, "", "name: huge\ndescription: d\nruntime: claude\n"+
		"model: {provider: model-gateway}\nrequests: {memory: 64Gi}\n")
	require.NoError(t, err)

	first, err := w.s.PutBot(ctx, Bot{Name: "first", GuaranteedDispatches: 1})
	require.NoError(t, err)
	second, err := w.s.PutBot(ctx, Bot{Name: "second", GuaranteedDispatches: 1})
	require.NoError(t, err)

	run, err := w.s.CreateRun(ctx, NewRun{Objective: "first", CreatedBy: "pi", Bot: first.ID})
	require.NoError(t, err)
	firstTasks, err := w.s.CreateTasks(ctx, run.ID, []NewTask{
		{Ref: "huge", Profile: "huge", Spec: "first-huge"}, {Ref: "next", Profile: "implementer", Spec: "first-next"},
	})
	require.NoError(t, err)

	botRun(w, second, "second", 1)

	created := w.schedule().Created
	specs := map[string]bool{}

	for _, id := range created {
		specs[w.task(w.dispatchOf(id).TaskID).Spec] = true
	}

	assert.Equal(t, map[string]bool{"second-a": true, "first-next": true}, specs)
	assert.Equal(t, ReasonNoNodeFits, w.task(firstTasks[0].ID).WaitingReason)
}

// Scenario: the order of the groups in a pass. The lowest used / guarantee
// goes first (2 of 4 before 1 of 1), compared exactly; equal ratios go to the
// older Run, then the smaller group id; a group with no Task left is skipped,
// and nil comes when every Task was tried.
func TestGroupsAreOrderedByUsedOverGuarantee(t *testing.T) {
	t.Parallel()

	a := &queue{group: "bot_a", guarantee: 4, tasks: []queued{{id: "a1", runCreated: 5}}}
	b := &queue{group: "bot_b", guarantee: 1, tasks: []queued{{id: "b1", runCreated: 0}}}
	assert.Same(t, a, nextQueue([]*queue{b, a}, map[string]int{"bot_a": 2, "bot_b": 1}), "1/2 before 1/1")

	assert.Same(t, b, nextQueue([]*queue{a, b}, map[string]int{"bot_a": 4, "bot_b": 1}), "equal: the older Run")

	c := &queue{group: "bot_c", guarantee: 1, tasks: []queued{{id: "c1", runCreated: 0}}}
	assert.Same(t, b, nextQueue([]*queue{c, b}, map[string]int{}), "equal and as old: the smaller id")

	empty := &queue{group: "", guarantee: 4}
	assert.Same(t, c, nextQueue([]*queue{empty, c}, map[string]int{"bot_c": 9}), "nothing ready is skipped")
	assert.Nil(t, nextQueue([]*queue{empty}, nil))
}

// Scenario: two Bots, each 0 of 1, have Runs created at the same moment, and
// there is room for one building worker (PR #759). The pass's list is ordered
// by when the Run was created and then by the Run's id, so the Run's place in
// it must not decide: the Bot with the smaller id goes first, whatever the
// Runs' ids.
func TestRunsOfTheSameAgeGoToTheSmallerGroupID(t *testing.T) {
	t.Parallel()

	w := newScoreWorld(t)
	ctx := t.Context()
	w.sized("node_one", 8, 4*gib)

	x, err := w.s.PutBot(ctx, Bot{Name: "x", GuaranteedDispatches: 1})
	require.NoError(t, err)
	y, err := w.s.PutBot(ctx, Bot{Name: "y", GuaranteedDispatches: 1})
	require.NoError(t, err)

	first, second := botRun(w, x, "x", 1)[0], botRun(w, y, "y", 1)[0]
	runOf := func(id string) Run {
		r, err := w.s.GetRun(ctx, id)
		require.NoError(t, err)

		return r
	}
	require.Equal(t, runOf(first.RunID).CreatedAt, runOf(second.RunID).CreatedAt)

	// The Run with the smaller id, first in the list, is the Bot with the larger id.
	early, late := first, second
	if early.RunID > late.RunID {
		early, late = late, early
	}

	small, large := min(x.ID, y.ID), max(x.ID, y.ID)
	_, err = w.db.ExecContext(ctx, "UPDATE runs SET bot_id = CASE id WHEN ? THEN ? ELSE ? END WHERE id IN (?, ?)",
		early.RunID, large, small, early.RunID, late.RunID)
	require.NoError(t, err)

	created := w.schedule().Created
	require.Len(t, created, 1)
	assert.Equal(t, late.ID, w.dispatchOf(created[0]).TaskID, "the smaller Bot id, not the smaller Run id")
}

// Scenario: a Bot's guarantee is a setting of 1 to MaxDispatchesLimit, kept
// across a change of another setting; 0 is the default.
func TestABotsGuaranteeIsASetting(t *testing.T) {
	t.Parallel()

	w := newSchedWorld(t)
	ctx := t.Context()

	_, err := w.s.PutBot(ctx, Bot{Name: "eager", GuaranteedDispatches: MaxDispatchesLimit + 1})
	require.ErrorIs(t, err, ErrInvalid)

	plain, err := w.s.PutBot(ctx, Bot{Name: "plain"})
	require.NoError(t, err)
	assert.Equal(t, BotGuaranteeDefault, plain.Guarantee())

	solo, err := w.s.PutBot(ctx, Bot{Name: "solo", GuaranteedDispatches: 1})
	require.NoError(t, err)

	solo.Persona = "One at a time."
	solo, err = w.s.PutBot(ctx, solo)
	require.NoError(t, err)
	assert.Equal(t, 1, solo.Guarantee(), "kept across another change")
}

// Scenario: requests in the profile's notation, as a Kubernetes quantity.
func TestRequestsAreReadAsQuantities(t *testing.T) {
	t.Parallel()

	for in, want := range map[string]int64{"500m": 500, "0.5": 500, "1": 1000, "2.25": 2250, "24": 24000} {
		t.Run("cpu "+in, func(t *testing.T) {
			t.Parallel()

			got, err := parseCPU(in)
			require.NoError(t, err)
			assert.Equal(t, want, got)
		})
	}

	for in, want := range map[string]int64{
		"512Mi": 512 << 20, "3Gi": 3 << 30, "1.5Gi": 3 << 29, "1G": 1e9, "100k": 1e5, "4096": 4096,
	} {
		t.Run("memory "+in, func(t *testing.T) {
			t.Parallel()

			got, err := parseMemory(in)
			require.NoError(t, err)
			assert.Equal(t, want, got)
		})
	}

	r, err := ParseReserve("0", "")
	require.NoError(t, err)
	assert.Equal(t, Resources{}, r, "0 and nothing are no reserve")
}
