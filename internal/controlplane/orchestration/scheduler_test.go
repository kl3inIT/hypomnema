package orchestration

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

// The scheduler scenarios: admission at the limit, the order of ready Tasks,
// placement on nodes and accounts, role defaults and escalations. Each runs on
// real SQLite with a fake clock; a node is connected when it was heard from
// within NodeOfflineAfter of that clock.

// newSchedWorld is a world whose implementer profile runs on Claude, as
// the starting set has it.
func newSchedWorld(t *testing.T) *world {
	t.Helper()

	return newWorld(t)
}

func (w *world) schedule() Scheduled {
	w.t.Helper()

	out, err := w.s.Schedule(w.t.Context())
	require.NoError(w.t, err)

	return out
}

// begin takes a queued Dispatch to running.
func (w *world) begin(id string) NodeCall {
	w.t.Helper()

	c := w.accept(w.dispatchOf(id))
	require.NoError(w.t, w.s.ReportStarted(w.t.Context(), w.again(c), "pid:42@1", "sess_1"))

	return c
}

// placed maps each created Dispatch's Task spec to its node.
func (w *world) placed(ids []string) map[string]string {
	w.t.Helper()

	out := map[string]string{}
	for _, id := range ids {
		d := w.dispatchOf(id)
		out[w.task(d.TaskID).Spec] = d.NodeID
	}

	return out
}

func specs(n int) []NewTask {
	out := make([]NewTask, 0, n)
	for i := range n {
		s := string(rune('a' + i))
		out = append(out, NewTask{Ref: s, Profile: "implementer", Spec: s})
	}

	return out
}

// Scenario: more ready Tasks than slots. At most MaxActiveDispatches run; a
// Dispatch waiting on a question keeps its slot, a gated or settled one
// frees it, and the coordinator's own CreateDispatch meets the same limit.
func TestSchedulerAdmitsUpToTheLimit(t *testing.T) {
	t.Parallel()

	w := newSchedWorld(t)
	tasks := w.tasks(specs(6)...)

	first := w.schedule().Created
	require.Len(t, first, MaxActiveDispatches)
	assert.Empty(t, w.schedule().Created, "a second pass with nothing changed creates nothing")

	_, err := w.create(tasks[5].ID, w.spec())
	require.ErrorIs(t, err, ErrUnavailable)

	waiting := w.begin(first[0])
	w.ask(waiting, libraryChoice(AddressOwner))
	assert.Empty(t, w.schedule().Created, "a waiting Dispatch holds its slot")

	gated := w.begin(first[1])
	gate := mergeGate()
	require.NoError(t, w.finish(gated, Outcome{Gate: &gate}))
	assert.Len(t, w.schedule().Created, 1, "a gated Dispatch has settled")

	done := w.begin(first[2])
	require.NoError(t, w.finish(done, success("done")))
	assert.Len(t, w.schedule().Created, 1)
	assert.Empty(t, w.schedule().Created, "every Task has a Dispatch or waits on its Gate")
}

// Scenario: two Runs. The older Run's Tasks go first, by position; a Task no
// node can run is skipped and does not hold back the ones after it.
func TestSchedulerTakesTheOldestRunFirst(t *testing.T) {
	t.Parallel()

	w := newSchedWorld(t)
	w.tasks(
		NewTask{Ref: "x", Profile: "implementer", Spec: "x", Runtime: "codex"},
		NewTask{Ref: "y", Profile: "implementer", Spec: "y"},
		NewTask{Ref: "z", Profile: "implementer", Spec: "z"},
	)

	w.advance(time.Second)
	w.alive(w.node)

	later, err := w.s.CreateRun(t.Context(), NewRun{Objective: "a later objective", CreatedBy: "owner"})
	require.NoError(t, err)
	_, err = w.s.CreateTasks(t.Context(), later.ID, []NewTask{
		{Ref: "p", Profile: "implementer", Spec: "p"},
		{Ref: "q", Profile: "implementer", Spec: "q"},
		{Ref: "r", Profile: "implementer", Spec: "r"},
	})
	require.NoError(t, err)

	got := w.placed(w.schedule().Created)
	assert.Equal(t, map[string]string{"y": w.node.ID, "z": w.node.ID, "p": w.node.ID, "q": w.node.ID}, got)
}

// Scenario: a worker error. The Task waits out its back-off, then continues
// its Conversation on the same node.
func TestSchedulerWaitsForTheBackOffThenContinues(t *testing.T) {
	t.Parallel()

	w := newSchedWorld(t)
	task := w.tasks(specs(1)...)[0]
	first := w.failOnce(task.ID, FailureWorkerError)

	assert.Empty(t, w.schedule().Created)

	w.advance(BackoffBase)
	w.alive(w.node)

	created := w.schedule().Created
	require.Len(t, created, 1)
	assert.Equal(t, first.ConversationID, w.dispatchOf(created[0]).ConversationID)
}

// Scenario: an open Conversation and its node is away. The Task waits for
// that node, even when another one could take it; once the node is back,
// the Conversation continues there.
func TestContinuingWaitsForItsNode(t *testing.T) {
	t.Parallel()

	w := newSchedWorld(t)
	task := w.tasks(specs(1)...)[0]
	first := w.failOnce(task.ID, FailureWorkerError)

	w.advance(BackoffMax)
	w.addNode("node_b", 2, "claude")
	assert.Empty(t, w.schedule().Created, "node_a is not connected")

	w.alive(w.node)
	require.NoError(t, w.s.DrainNode(t.Context(), w.node.ID))
	assert.Empty(t, w.schedule().Created, "node_a is drained")

	require.NoError(t, w.s.ResumeNode(t.Context(), w.node.ID))

	created := w.schedule().Created
	require.Len(t, created, 1)
	d := w.dispatchOf(created[0])
	assert.Equal(t, w.node.ID, d.NodeID)
	assert.Equal(t, first.ConversationID, d.ConversationID)
}

// Scenario: several nodes for new Conversations. Drained, offline,
// unavailable and full nodes and those without the runtime are skipped; the
// least loaded wins, ties by id; the worktree is under the node's root.
func TestPlacementPicksTheLeastLoadedNodeThatCanRun(t *testing.T) {
	t.Parallel()

	w := newSchedWorld(t)
	require.NoError(t, w.s.DrainNode(t.Context(), w.node.ID))

	offline := w.addNode("node_offline", 2, "claude")
	w.advance(NodeOfflineAfter)

	small := w.addNode("node_small", 1, "claude")
	w.addNode("node_codex", 2, "codex")
	big := w.addNode("node_big", 2, "claude")
	away := w.addNode("node_away", 2, "claude")
	_, err := w.db.ExecContext(t.Context(), "UPDATE nodes SET unavailable_at = 1 WHERE id = ?", away.ID)
	require.NoError(t, err)

	tasks := w.tasks(specs(4)...)
	created := w.schedule().Created

	assert.Equal(t, map[string]string{"a": small.ID, "b": big.ID, "c": big.ID}, w.placed(created))
	assert.NotContains(t, w.placed(created), "d", "every node that can run is full")
	assert.NotEqual(t, offline.ID, w.dispatchOf(created[0]).NodeID)

	conv := w.conversation(w.dispatchOf(created[0]).ConversationID)
	assert.Equal(t, "/srv/wt/"+w.run.ID+"/"+tasks[0].ID, conv.Worktree)

	nodes, err := w.s.ListNodes(t.Context())
	require.NoError(t, err)

	load := map[string]int{}
	for _, n := range nodes {
		load[n.Name] = n.Active
	}

	assert.Equal(t, map[string]int{
		"node_a": 0, "node_offline": 0, "node_small": 1, "node_codex": 0, "node_big": 2, "node_away": 0,
	}, load)
}

// Scenario: accounts and rate limits. The first account by position is used;
// a cooling one is skipped; when every account cools for over an hour the
// owner is told once, and a Task that only waits for a reset is not escalated
// as unplaceable.
func TestAccountsAreTakenInOrderAndCoolingOnesSkipped(t *testing.T) {
	t.Parallel()

	w := newSchedWorld(t)
	second := w.addAccount("claude", "acct_2")
	disabled := w.addAccount("claude", "acct_0")
	require.NoError(t, w.s.DisableAccount(t.Context(), disabled.ID))

	w.tasks(specs(3)...)
	resets := t0.Add(2 * time.Hour)

	created := w.schedule().Created
	require.Len(t, created, 3)

	for _, id := range created {
		assert.Equal(t, w.account.ID, w.conversation(w.dispatchOf(id).ConversationID).AccountRef)
	}

	before := w.lastSeq()
	require.NoError(t, w.finish(w.begin(created[0]), Outcome{Class: FailureRateLimited, ResetsAt: &resets}))
	assert.NotContains(t, w.events(before, EventFilter{}), "escalation a account_cooling", "acct_2 is still free")

	accounts, err := w.s.ListAccounts(t.Context())
	require.NoError(t, err)
	require.Len(t, accounts, 3)
	assert.Equal(t, w.account.ID, accounts[0].ID)
	require.NotNil(t, accounts[0].CoolingUntil)
	assert.Equal(t, resets, *accounts[0].CoolingUntil)

	// The other two Tasks are still queued on acct_1; a new Task goes to acct_2.
	more := w.tasks(NewTask{Ref: "n", Profile: "implementer", Spec: "n"})[0]
	next := w.schedule().Created
	require.Len(t, next, 1)
	d := w.dispatchOf(next[0])
	assert.Equal(t, more.ID, d.TaskID)
	assert.Equal(t, second.ID, w.conversation(d.ConversationID).AccountRef)

	before = w.lastSeq()
	require.NoError(t, w.finish(w.begin(next[0]), Outcome{Class: FailureRateLimited, ResetsAt: &resets}))
	assert.Contains(t, w.events(before, EventFilter{}), "escalation n account_cooling")

	w.tasks(NewTask{Ref: "w", Profile: "implementer", Spec: "w"})
	assert.Empty(t, w.schedule().Created, "every account cools")

	w.advance(UnplaceableAfter)
	w.alive(w.node)

	out := w.schedule()
	assert.Empty(t, out.Created)
	assert.Empty(t, out.Escalations, "waiting for a reset resolves by itself")
}

// Scenario: profile defaults. A Task's own runtime, model and effort win
// over its profile's; the Dispatch records what it ran with and the profile
// version.
func TestTaskValuesWinOverProfileDefaults(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	implementer, _, err := w.s.ApplyProfile(t.Context(), "",
		"name: implementer\ndescription: d\nruntime: claude\nmodel: {name: opus, effort: high}\n")
	require.NoError(t, err)
	_, _, err = w.s.ApplyProfile(t.Context(), "", "name: reviewer\ndescription: d\nruntime: codex\n")
	require.NoError(t, err)

	w.tasks(
		NewTask{Ref: "a", Profile: "implementer", Spec: "a"},
		NewTask{Ref: "b", Profile: "implementer", Spec: "b", Model: "sonnet"},
		NewTask{Ref: "c", Profile: "reviewer", Spec: "c"},
	)

	created := w.schedule().Created
	require.Len(t, created, 2, "the reviewer runs on codex, which no node has")

	got := map[string][3]string{}
	for _, id := range created {
		d := w.dispatchOf(id)
		got[w.task(d.TaskID).Spec] = [3]string{d.Model, d.Effort, d.ProfileVersionID}
	}

	v := implementer.VersionID
	assert.Equal(t, map[string][3]string{"a": {"opus", "high", v}, "b": {"sonnet", "high", v}}, got)
}

// Scenario: a ready Task no node can run. After UnplaceableAfter the owner is
// told once, with the reason; a Task with a runtime but no usable account
// gets the other reason.
func TestUnplaceableTaskIsEscalatedOnce(t *testing.T) {
	t.Parallel()

	w := newSchedWorld(t)
	w.tasks(NewTask{Ref: "a", Profile: "implementer", Spec: "a", Runtime: "codex"})
	w.addAccount("pi", "acct_g")
	w.tasks(NewTask{Ref: "g", Profile: "implementer", Spec: "g", Runtime: "pi"})
	w.addNode("node_g", 1, "pi")

	require.NoError(t, w.s.DisableAccount(t.Context(), mustAccount(t, w, "pi")))

	assert.Empty(t, w.schedule().Escalations, "not yet")

	w.advance(UnplaceableAfter)
	w.alive(w.node)
	require.NoError(t, w.s.NodeHeartbeat(t.Context(), mustNode(t, w, "node_g")))

	before := w.lastSeq()
	out := w.schedule()
	require.Len(t, out.Escalations, 2)
	assert.Equal(t, ReasonNoNode, out.Escalations[0].Reason)
	assert.Equal(t, ReasonNoAccount, out.Escalations[1].Reason)
	assert.Equal(t, []string{"escalation a unplaceable", "escalation g unplaceable"}, w.events(before, EventFilter{}))

	assert.Empty(t, w.schedule().Escalations, "escalated once")
}

func mustAccount(t *testing.T, w *world, runtime string) string {
	t.Helper()

	accounts, err := w.s.ListAccounts(t.Context())
	require.NoError(t, err)

	for _, a := range accounts {
		if a.Runtime == runtime {
			return a.ID
		}
	}

	t.Fatalf("no %s account", runtime)

	return ""
}

func mustNode(t *testing.T, w *world, name string) string {
	t.Helper()

	nodes, err := w.s.ListNodes(t.Context())
	require.NoError(t, err)

	for _, n := range nodes {
		if n.Name == name {
			return n.ID
		}
	}

	t.Fatalf("no node %s", name)

	return ""
}

// Scenario: a node goes quiet. It is connected until NodeOfflineAfter has
// passed since it was last heard from, and any node call counts.
func TestNodeIsConnectedUntilItGoesQuiet(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	connected := func() bool {
		n, err := w.s.GetNode(t.Context(), w.node.ID)
		require.NoError(t, err)

		return n.Connected
	}

	w.advance(NodeOfflineAfter - time.Millisecond)
	assert.True(t, connected())

	w.advance(time.Millisecond)
	assert.False(t, connected())

	task := w.tasks(specs(1)...)[0]
	_, err := w.s.CreateDispatch(t.Context(), task.ID, w.spec())
	require.ErrorIs(t, err, ErrUnavailable, "no Dispatch for a node that is not connected")

	d := w.dispatch(task.ID)
	w.advance(NodeOfflineAfter)
	w.accept(d)
	assert.True(t, connected(), "accepting a Dispatch counts as being heard from")
}

// Scenario: the node let its Dispatches expire. It takes no new work until it
// connects again (a hello) or the owner resumes it.
func TestUnavailableNodeTakesNothingUntilItConnectsAgain(t *testing.T) {
	t.Parallel()

	w := newSchedWorld(t)
	task := w.tasks(specs(1)...)[0]

	for range ExpiriesBeforeUnavailable {
		w.dispatch(task.ID)
		w.advance(QueueTimeout)
		w.reap()
	}

	n, err := w.s.GetNode(t.Context(), w.node.ID)
	require.NoError(t, err)
	require.NotNil(t, n.UnavailableAt)

	w.alive(w.node)
	assert.Empty(t, w.schedule().Created)

	require.NoError(t, w.s.NodeHello(t.Context(), w.node.ID, Hello{
		WorktreeRoot: "/srv/wt", Runtimes: []RuntimeInfo{{Runtime: "claude"}},
	}))
	assert.Len(t, w.schedule().Created, 1)
}

// Scenario: registering a node. The token authenticates it, another does
// not, and only the token's hash is stored.
func TestNodeTokenAuthenticatesAndIsNotStored(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	n, token, err := w.s.AddNode(t.Context(), "node_new", 0)
	require.NoError(t, err)
	assert.Equal(t, NodeDispatchesDefault, n.MaxDispatches)
	assert.True(t, strings.HasPrefix(token, "hyn_"))

	got, err := w.s.AuthenticateNode(t.Context(), token)
	require.NoError(t, err)
	assert.Equal(t, n.ID, got.ID)

	_, err = w.s.AuthenticateNode(t.Context(), token+"x")
	require.ErrorIs(t, err, ErrNotFound)

	var stored int
	require.NoError(t, w.db.QueryRowContext(t.Context(),
		"SELECT count(*) FROM node_tokens WHERE token_hash = ? OR name = ?", token, token).Scan(&stored))
	assert.Zero(t, stored)

	_, _, err = w.s.AddNode(t.Context(), "node_new", 0)
	require.ErrorIs(t, err, ErrConflict, "names are unique")
}

// Scenario: the loop. A commit wakes it, and it places the new Task without
// waiting for the next tick; it stops with its context.
func TestLoopPlacesANewTaskWhenWoken(t *testing.T) {
	t.Parallel()

	w := newSchedWorld(t)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)

	go func() { done <- w.s.Loop(ctx) }()

	task := w.tasks(specs(1)...)[0]

	require.Eventually(t, func() bool {
		return w.task(task.ID).Status == TaskRunning
	}, time.Second, 5*time.Millisecond)

	cancel()
	require.NoError(t, <-done)
}

func TestMigrationMovesCooldownsToAccounts(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db, err := sqlite.Open(ctx, sqlite.Config{Path: filepath.Join(t.TempDir(), "hypomnema.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	p, err := provider(db)
	require.NoError(t, err)
	_, err = p.UpTo(ctx, 4)
	require.NoError(t, err)

	_, err = db.ExecContext(ctx, `INSERT INTO slot_cooldowns (node_id, runtime, account_ref, until, reason) VALUES
		('node_a', 'claude', 'acct_1', 100, 'rate_limited'),
		('node_b', 'claude', 'acct_1', 300, 'rate_limited'),
		('node_a', 'codex', 'acct_2', 200, 'rate_limited')`)
	require.NoError(t, err)

	require.NoError(t, Migrate(ctx, db))
	require.NoError(t, CheckSchema(ctx, db))

	rows, err := db.QueryContext(ctx, "SELECT runtime, account_ref, until FROM account_cooldowns ORDER BY runtime")
	require.NoError(t, err)

	defer func() { _ = rows.Close() }()

	var got []string

	for rows.Next() {
		var runtime, account string

		var until int64

		require.NoError(t, rows.Scan(&runtime, &account, &until))
		got = append(got, runtime+"/"+account+"@"+time.UnixMilli(until).UTC().Format("05.000"))
	}

	require.NoError(t, rows.Err())
	assert.Equal(t, []string{"claude/acct_1@00.300", "codex/acct_2@00.200"}, got)
}
