package orchestration

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
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

// Scenario: the owner logged a Claude subscription in on the control plane.
// No node holds its tokens, so a Task waits rather than start under it, and
// starts once an account a node holds is free. A second login of the same
// vendor account keeps the one account.
func TestAnAccountLoggedInOnTheControlPlaneIsNotGivenToWorkers(t *testing.T) {
	t.Parallel()

	w := newSchedWorld(t)
	require.NoError(t, w.s.DisableAccount(t.Context(), w.account.ID))

	login := AccountLogin{Runtime: "claude", ExternalID: "org-1", Email: "owner@example.com", Plan: "max"}
	ref := func(id string) string { return "vault:account-" + id }

	logged, err := w.s.SaveAccountLogin(t.Context(), login, ref)
	require.NoError(t, err)
	assert.Equal(t, "owner@example.com", logged.Label)
	assert.Equal(t, "vault:account-"+logged.ID, logged.SecretRef)

	w.tasks(specs(1)...)
	assert.Empty(t, w.schedule().Created)

	require.NoError(t, w.s.EnableAccount(t.Context(), w.account.ID))

	created := w.schedule().Created
	require.Len(t, created, 1)
	assert.Equal(t, w.account.ID, w.conversation(w.dispatchOf(created[0]).ConversationID).AccountRef)

	login.Plan = "pro"
	again, err := w.s.SaveAccountLogin(t.Context(), login, ref)
	require.NoError(t, err)
	assert.Equal(t, logged.ID, again.ID)
	assert.Equal(t, "pro", again.Plan)

	users, err := w.s.AccountsUsingSecretRef(t.Context(), logged.SecretRef)
	require.NoError(t, err)
	assert.Equal(t, []string{"account claude/owner@example.com"}, users)
}

// Scenario: two members of one ChatGPT Business workspace sign in. Codex names
// both by the workspace's id, so they are told apart by their e-mail: two
// accounts, each with its own secret. A member signing in again, the e-mail
// written in other case, keeps their account (issue #326).
func TestMembersOfOneWorkspaceAreTwoAccounts(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ref := func(id string) string { return "vault:account-" + id }

	first, err := w.s.SaveAccountLogin(t.Context(), AccountLogin{
		Runtime: "codex", ExternalID: "workspace-1", Email: "first@example.com", Plan: "team",
	}, ref)
	require.NoError(t, err)

	second, err := w.s.SaveAccountLogin(t.Context(), AccountLogin{
		Runtime: "codex", ExternalID: "workspace-1", Email: "second@example.com", Plan: "team",
	}, ref)
	require.NoError(t, err)
	assert.NotEqual(t, first.ID, second.ID)
	assert.NotEqual(t, first.SecretRef, second.SecretRef)

	again, err := w.s.SaveAccountLogin(t.Context(), AccountLogin{
		Runtime: "codex", ExternalID: "workspace-1", Email: "First@Example.com", Plan: "team",
	}, ref)
	require.NoError(t, err)
	assert.Equal(t, first.ID, again.ID)
}

// Scenario: the database of a running control plane is migrated past the
// rebuild of the providers table. The providers the owner declared, their
// models, the profile that names one and the gateway model served from one
// are all still there, and a provider of the new kind can be applied.
func TestMigrationKeepsProvidersAndWhatNamesThem(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db, err := sqlite.Open(ctx, sqlite.Config{Path: filepath.Join(t.TempDir(), "hypomnema.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	p, err := provider(db)
	require.NoError(t, err)
	_, err = p.UpTo(ctx, 26)
	require.NoError(t, err)

	_, err = db.ExecContext(ctx, `
		INSERT INTO providers (name, kind, base_url, api_key_ref, source, created_at, updated_at)
		VALUES ('router', 'openai_compatible', 'http://router/v1', 'vault:router', 'name: router', 5, 6);
		INSERT INTO models (provider, name) VALUES ('router', 'cx/gpt');
		INSERT INTO profile_versions (id, profile, version, source, digest, runtime, provider, model, created_at)
		VALUES ('pfv_1', 'coordinator', 9, 'name: coordinator', 'd', 'pi', 'router', 'cx/gpt', 7);
		INSERT INTO gateway_models (name, capability, source, created_at, updated_at)
		VALUES ('gpt', 'chat', 'name: gpt', 1, 1);
		INSERT INTO gateway_model_targets (model, position, provider, upstream_model) VALUES ('gpt', 1, 'router', 'cx/gpt');`)
	require.NoError(t, err)

	require.NoError(t, Migrate(ctx, db))
	require.NoError(t, CheckSchema(ctx, db))

	var broken int
	require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*) FROM pragma_foreign_key_check").Scan(&broken))
	assert.Zero(t, broken)

	s := New(db, &fakeClock{now: t0}, func(prefix string) string { return prefix + "_1" })

	router, err := s.GetProvider(ctx, "router")
	require.NoError(t, err)
	assert.Equal(t, "http://router/v1", router.BaseURL)
	assert.Equal(t, "vault:router", router.APIKeyRef)
	assert.Equal(t, []string{"cx/gpt"}, router.Models)

	claude, err := s.GetProvider(ctx, "claude-subscription")
	require.NoError(t, err)
	assert.Contains(t, claude.Models, "opus")

	antigravity, err := s.GetProvider(ctx, "antigravity-subscription")
	require.NoError(t, err)
	assert.Equal(t, ProviderAntigravitySubscription, antigravity.Kind)
	assert.Contains(t, antigravity.Models, "gemini-3.8-flash-high")

	var named int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM profile_versions WHERE provider = 'router') +
		(SELECT count(*) FROM gateway_model_targets WHERE provider = 'router')`).Scan(&named))
	assert.Equal(t, 1, named, "the versions of pi's profile left with it when it became a Bot (00045)")

	_, err = s.ApplyProvider(ctx, "", "name: agy2\nkind: antigravity_subscription\nmodels: [m]\n")
	require.NoError(t, err)

	_, err = s.ApplyProvider(ctx, "", "name: odd\nkind: telepathy\nmodels: [m]\n")
	require.ErrorIs(t, err, ErrInvalid)
}

// Scenario: a profile whose model the model gateway serves. Its Task runs on
// a node that has the CLI although no account of that runtime exists
// anywhere; the Conversation names the gateway where an account would be. A
// rate limit cools no account down, since the gateway looks after its own.
func TestAModelTheGatewayServesNeedsNoAccountOnANode(t *testing.T) {
	t.Parallel()

	w := newSchedWorld(t)
	w.addNode("node_codex", 2, "codex")

	_, err := w.s.ApplyProvider(t.Context(), "", "name: gateway\nkind: model_gateway\nmodels: [gpt-sol]\n")
	require.NoError(t, err)
	_, _, err = w.s.ApplyProfile(t.Context(), "",
		"name: codex-worker\ndescription: d\nruntime: codex\nmodel: {provider: gateway, name: gpt-sol}\n")
	require.NoError(t, err)
	_, _, err = w.s.ApplyProfile(t.Context(), "", "name: codex-own\ndescription: d\nruntime: codex\n")
	require.NoError(t, err)

	w.tasks(
		NewTask{Ref: "g", Profile: "codex-worker", Spec: "g"},
		NewTask{Ref: "o", Profile: "codex-own", Spec: "o"},
	)

	created := w.schedule().Created
	require.Len(t, created, 1, "a codex worker that logs in itself still needs an account on a node")

	d := w.dispatchOf(created[0])
	assert.Equal(t, "g", w.task(d.TaskID).Spec)
	assert.Equal(t, "gpt-sol", d.Model)
	assert.Equal(t, GatewayAccountRef, w.conversation(d.ConversationID).AccountRef)

	before := w.lastSeq()
	resets := t0.Add(2 * time.Hour)
	require.NoError(t, w.finish(w.begin(created[0]), Outcome{Class: FailureRateLimited, ResetsAt: &resets}))
	assert.NotContains(t, w.events(before, EventFilter{}), "escalation g account_cooling")

	accounts, err := w.s.ListAccounts(t.Context())
	require.NoError(t, err)

	for _, a := range accounts {
		assert.Nil(t, a.CoolingUntil, a.Label)
	}

	// pi runs on the control plane with a key of its own, not under a Dispatch.
	_, _, err = w.s.ApplyProfile(t.Context(), "",
		"name: pi-worker\ndescription: d\nruntime: pi\nmodel: {provider: gateway, name: gpt-sol}\n")
	require.ErrorIs(t, err, ErrInvalid)
}

// A profile says whether a worker can start under it now, by the scheduler's
// own view: a node that has its CLI, then an account that is not cooling
// down, or none for a model the gateway serves. The coordinator's profile
// says nothing, since no node runs it.
func TestProfilesSayWhetherAWorkerCanStartNow(t *testing.T) {
	t.Parallel()

	w := newSchedWorld(t)
	ctx := t.Context()

	_, err := w.s.ApplyProvider(ctx, "", "name: gateway\nkind: model_gateway\nmodels: [gpt-sol]\n")
	require.NoError(t, err)
	_, _, err = w.s.ApplyProfile(ctx, "",
		"name: codex-worker\ndescription: d\nruntime: codex\nmodel: {provider: gateway, name: gpt-sol}\n")
	require.NoError(t, err)
	_, _, err = w.s.ApplyProfile(ctx, "", "name: codex-own\ndescription: d\nruntime: codex\n")
	require.NoError(t, err)

	availability := func() map[string]*Availability {
		t.Helper()

		profiles, err := w.s.ListProfiles(ctx)
		require.NoError(t, err)

		out := map[string]*Availability{}
		for _, p := range profiles {
			out[p.Name] = p.Availability
		}

		return out
	}

	got := availability()
	assert.Equal(t, &Availability{Available: true}, got["implementer"], "node_a has claude and acct_1 is free")
	assert.Equal(t, &Availability{Reason: ReasonNoNode}, got["codex-worker"], "no node has codex")
	assert.Nil(t, got["coordinator"], "pi is not a worker's runtime")

	w.addNode("node_codex", 1, "codex")

	got = availability()
	assert.Equal(t, &Availability{Available: true}, got["codex-worker"], "the gateway serves it: no account needed")
	assert.Equal(t, &Availability{Reason: ReasonNoAccount}, got["codex-own"], "codex has no account on a node")

	one, err := w.s.GetProfile(ctx, "codex-own")
	require.NoError(t, err)
	assert.Equal(t, &Availability{Reason: ReasonNoAccount}, one.Availability)

	// A node at its limit still counts: the Task waits its turn.
	w.tasks(NewTask{Ref: "g", Profile: "codex-worker", Spec: "g"}, NewTask{Ref: "c", Profile: "implementer", Spec: "c"})

	created := w.schedule().Created
	require.Len(t, created, 2)
	assert.Equal(t, &Availability{Available: true}, availability()["codex-worker"], "node_codex runs 1 of 1")

	// The only account of the runtime cools down until its limit resets.
	var claude string

	for _, id := range created {
		if w.dispatchOf(id).NodeID == w.node.ID {
			claude = id
		}
	}

	resets := t0.Add(2 * time.Hour)
	require.NoError(t, w.finish(w.begin(claude), Outcome{Class: FailureRateLimited, ResetsAt: &resets}))
	assert.Equal(t, &Availability{Reason: ReasonAccountsCooling, At: &resets}, availability()["implementer"])

	second := w.addAccount("claude", "acct_2")
	assert.Equal(t, &Availability{Available: true}, availability()["implementer"], "acct_2 is free")
	require.NoError(t, w.s.DisableAccount(ctx, second.ID))

	// A drained node runs no new worker.
	require.NoError(t, w.s.DrainNode(ctx, w.node.ID))
	assert.Equal(t, &Availability{Reason: ReasonNoNode}, availability()["implementer"])
}

// Scenario: the owner gives the control plane a Claude token (issue #150).
// No node holds an account, yet a Claude Code Task is placed on a node that
// has Claude Code, under the account whose token the control plane keeps; an
// account before it that cools down is passed over as any other. A Codex
// account logged in on the control plane is for the gateway: it is not one a
// worker runs under.
func TestAnAccountWhoseTokenTheControlPlaneKeepsIsPlaced(t *testing.T) {
	t.Parallel()

	w := newSchedWorld(t)
	require.NoError(t, w.s.DisableAccount(t.Context(), w.account.ID))

	kept, err := w.s.AddTokenAccount(t.Context(), "claude", "personal", func(id string) string { return "vault:" + id })
	require.NoError(t, err)
	assert.Equal(t, "vault:"+kept.ID, kept.SecretRef)

	_, err = w.s.AddTokenAccount(t.Context(), "codex", "work", func(id string) string { return "vault:" + id })
	require.ErrorIs(t, err, ErrInvalid, "only Claude Code's token is one to send")

	gateway, err := w.s.SaveAccountLogin(t.Context(), AccountLogin{
		Runtime: "codex", ExternalID: "chatgpt-1", Email: "owner@example.com",
	}, func(id string) string { return "vault:" + id })
	require.NoError(t, err)

	w.addNode("node_codex", 2, "codex")
	w.tasks(
		NewTask{Ref: "c", Profile: "implementer", Spec: "c"},
		NewTask{Ref: "x", Profile: "implementer", Spec: "x", Runtime: "codex"},
	)

	created := w.schedule().Created
	require.Len(t, created, 1, "the Codex account is the gateway's, not a worker's")

	d := w.dispatchOf(created[0])
	assert.Equal(t, "c", w.task(d.TaskID).Spec)
	assert.Equal(t, kept.ID, w.conversation(d.ConversationID).AccountRef)

	ref, sent, err := w.s.AccountTokenRef(t.Context(), kept.ID)
	require.NoError(t, err)
	assert.True(t, sent)
	assert.Equal(t, kept.SecretRef, ref)

	for _, id := range []string{w.account.ID, gateway.ID, "acc_unknown"} {
		_, sent, err := w.s.AccountTokenRef(t.Context(), id)
		require.NoError(t, err)
		assert.False(t, sent, id)
	}

	resets := t0.Add(2 * time.Hour)
	require.NoError(t, w.finish(w.begin(created[0]), Outcome{Class: FailureRateLimited, ResetsAt: &resets}))

	w.tasks(NewTask{Ref: "n", Profile: "implementer", Spec: "n"})
	assert.Empty(t, w.schedule().Created, "the account cools down as one on a node does")
}

// Scenario: out of quota (issue #171). While every account of a profile's
// runtime waits for a limit to reset, a Task that has not started runs under
// the first backup the owner listed that a worker can start under; the Run
// is told once for that stretch, and work returns when the limit has reset.
func TestTaskMovesToABackupProfileWhenItsOwnIsOutOfQuota(t *testing.T) {
	t.Parallel()

	w := newSchedWorld(t)
	ctx := t.Context()

	_, err := w.s.ApplyProvider(ctx, "", "name: gateway\nkind: model_gateway\nmodels: [gpt-sol]\n")
	require.NoError(t, err)
	_, _, err = w.s.ApplyProfile(ctx, "", "name: codex-own\ndescription: d\nruntime: codex\n")
	require.NoError(t, err)
	backup, _, err := w.s.ApplyProfile(ctx, "",
		"name: codex-worker\ndescription: d\nruntime: codex\nmodel: {provider: gateway, name: gpt-sol}\n")
	require.NoError(t, err)
	writer, _, err := w.s.ApplyProfile(ctx, "",
		"name: writer\ndescription: d\nruntime: claude\nfallbacks: [codex-own, codex-worker]\n")
	require.NoError(t, err)
	assert.Equal(t, []string{"codex-own", "codex-worker"}, writer.Fallbacks)

	codex := w.addNode("node_codex", 4, "codex")

	// The Task that meets the limit has a Conversation: it waits for the reset.
	first := w.tasks(NewTask{Ref: "a", Profile: "writer", Spec: "a"})[0]
	created := w.schedule().Created
	require.Len(t, created, 1)
	assert.Equal(t, w.node.ID, w.dispatchOf(created[0]).NodeID, "the profile's own runtime while it has quota")
	assert.Empty(t, w.dispatchOf(created[0]).FallbackFor)

	resets := t0.Add(2 * time.Hour)
	require.NoError(t, w.finish(w.begin(created[0]), Outcome{Class: FailureRateLimited, ResetsAt: &resets}))

	w.tasks(
		NewTask{Ref: "b", Profile: "writer", Spec: "b"},
		NewTask{Ref: "c", Profile: "writer", Spec: "c"},
		NewTask{Ref: "strict", Profile: "writer", Spec: "strict", Runtime: "claude"},
		NewTask{Ref: "plain", Profile: "implementer", Spec: "plain"},
	)

	out := w.schedule()
	require.Len(t, out.Created, 2, "b and c; the Task that names its runtime and the profile with no list wait")

	for _, id := range out.Created {
		d := w.dispatchOf(id)
		assert.Contains(t, []string{"b", "c"}, w.task(d.TaskID).Spec)
		assert.Equal(t, codex.ID, d.NodeID)
		assert.Equal(t, backup.VersionID, d.ProfileVersionID, "codex-own has no account, so the second backup runs")
		assert.Equal(t, "gpt-sol", d.Model)
		assert.Equal(t, "writer", d.FallbackFor)
		require.NotNil(t, d.FallbackUntil)
		assert.Equal(t, resets, *d.FallbackUntil)
		assert.Equal(t, GatewayAccountRef, w.conversation(d.ConversationID).AccountRef)
	}

	require.Len(t, out.Escalations, 1, "one notice for the stretch, not one per Task")
	assert.Equal(t, Escalation{
		Kind: EscalationFallback, DispatchID: out.Created[0], Profile: "writer", Fallback: "codex-worker",
		Until: resets.UnixMilli(),
	}, out.Escalations[0])
	assert.Equal(t, TaskReady, w.task(first.ID).Status, "the Task that met the limit still waits")

	// The limit has reset: a new Task runs under its own profile again.
	w.advance(2*time.Hour + time.Minute)
	w.alive(w.node)
	w.alive(codex)
	w.tasks(NewTask{Ref: "later", Profile: "writer", Spec: "later"})

	back := w.schedule().Created
	require.NotEmpty(t, back)

	for _, id := range back {
		d := w.dispatchOf(id)
		assert.Equal(t, w.node.ID, d.NodeID, w.task(d.TaskID).Spec)
		assert.Empty(t, d.FallbackFor)
	}
}

// Scenario: the limit comes under way (issue #187). The Task's worker had begun; a backup takes the Task over on
// the same node, in the same worktree, with a session of its own, and is told so with the end of the previous
// worker's transcript. A Task is handed off once, never when it names its own runtime, and never after a request
// to a service was approved for it.
func TestTaskUnderWayIsHandedToABackupInItsWorktree(t *testing.T) {
	t.Parallel()

	w := newSchedWorld(t)
	ctx := t.Context()

	_, err := w.s.ApplyProvider(ctx, "", "name: gateway\nkind: model_gateway\nmodels: [gpt-sol]\n")
	require.NoError(t, err)
	backup, _, err := w.s.ApplyProfile(ctx, "",
		"name: codex-worker\ndescription: d\nruntime: codex\nmodel: {provider: gateway, name: gpt-sol}\n")
	require.NoError(t, err)
	_, _, err = w.s.ApplyProfile(ctx, "",
		"name: writer\ndescription: d\nruntime: claude\nfallbacks: [codex-worker]\n")
	require.NoError(t, err)

	// Both CLIs on one node: the worktree is there.
	both := w.addNode("node_both", 4, "claude", "codex")
	require.NoError(t, w.s.DrainNode(ctx, w.node.ID))

	tasks := w.tasks(
		NewTask{Ref: "a", Profile: "writer", Spec: "do a"},
		NewTask{Ref: "strict", Profile: "writer", Spec: "strict", Runtime: "claude"},
	)

	created := w.schedule().Created
	require.Len(t, created, 2)

	byTask := map[string]Dispatch{}
	for _, id := range created {
		d := w.dispatchOf(id)
		byTask[d.TaskID] = d
	}

	first := byTask[tasks[0].ID]
	old := w.conversation(first.ConversationID)
	call := w.begin(first.ID)
	_, err = w.s.AddItems(ctx, both.ID, first.ID, []Item{
		{Seq: 1, Kind: ItemMessage, Text: "I will write the\nhandler first."},
		{Seq: 2, Kind: ItemReasoning, Text: "private thoughts"},
		{Seq: 3, Kind: ItemToolCall, CallID: "c1", Tool: "Bash", Input: []byte(`{"command":"go test ./..."}`)},
		{Seq: 4, Kind: ItemToolResult, CallID: "c1", Text: "ok"},
	})
	require.NoError(t, err)

	resets := t0.Add(48 * time.Hour)
	require.NoError(t, w.finish(call, Outcome{Class: FailureRateLimited, ResetsAt: &resets}))
	require.NoError(t, w.finish(w.begin(byTask[tasks[1].ID].ID), Outcome{Class: FailureRateLimited, ResetsAt: &resets}))
	assert.Nil(t, w.task(tasks[0].ID).NotBefore, "a Task a backup may take over is not held until the reset")
	assert.NotNil(t, w.task(tasks[1].ID).NotBefore, "a Task that names its runtime waits for the reset")

	out := w.schedule()
	require.Len(t, out.Created, 1, "the Task that names its runtime waits")

	d := w.dispatchOf(out.Created[0])
	conv := w.conversation(d.ConversationID)
	assert.Equal(t, tasks[0].ID, d.TaskID)
	assert.True(t, d.Handoff)
	assert.Equal(t, "writer", d.FallbackFor)
	assert.Equal(t, backup.VersionID, d.ProfileVersionID)
	assert.Equal(t, both.ID, d.NodeID)
	assert.Equal(t, old.Worktree, conv.Worktree, "the same worktree")
	assert.Equal(t, "codex", conv.Runtime)
	assert.NotEqual(t, old.ID, conv.ID, "a session of its own")
	assert.Equal(t, ConversationClosed, w.conversation(old.ID).Status)
	require.Len(t, out.Escalations, 1)
	assert.Equal(t, EscalationFallback, out.Escalations[0].Kind)

	// The worker that takes over gets the Task again, the note, and what the first one said and did.
	row, err := w.s.read().GetDispatch(ctx, d.ID)
	require.NoError(t, err)
	offer, err := offerOf(ctx, w.s.read(), row)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(offer.Spec, "do a\n\nAnother worker began this task"), offer.Spec)
	assert.Contains(t, offer.Spec, "The files are what is true")
	assert.Contains(t, offer.Spec, "Do not redo or rewrite a finished step")
	assert.Contains(t, offer.Spec, "worker: I will write the handler first.\n"+
		"tool Bash: {\"command\":\"go test ./...\"}\nresult: ok\n---\n")
	assert.NotContains(t, offer.Spec, "private thoughts")
	assert.Empty(t, offer.CLISessionRef, "no session of the first CLI is resumed")

	// A later Dispatch of the Task continues the backup's session with the backup's profile; it is not a second
	// hand-off, and the Run is not told again.
	require.NoError(t, w.finish(w.begin(d.ID), Outcome{Class: FailureWorkerError}))
	w.advance(time.Hour)
	w.alive(both)

	next := w.schedule()
	require.Len(t, next.Created, 1)
	assert.Empty(t, next.Escalations)

	again := w.dispatchOf(next.Created[0])
	assert.Equal(t, conv.ID, again.ConversationID)
	assert.Equal(t, backup.VersionID, again.ProfileVersionID)
	assert.Equal(t, "gpt-sol", again.Model)
	assert.Equal(t, "writer", again.FallbackFor)
	assert.False(t, again.Handoff)

	row, err = w.s.read().GetDispatch(ctx, again.ID)
	require.NoError(t, err)
	offer, err = offerOf(ctx, w.s.read(), row)
	require.NoError(t, err)
	assert.Equal(t, "do a", offer.Spec, "the note is given once, to the worker that takes over")
}

// A Task for which a request to a service was approved is not handed off: its worker acted outside the worktree.
func TestTaskThatWroteThroughAnAccountIsNotHandedOff(t *testing.T) {
	t.Parallel()

	w := newSchedWorld(t)
	ctx := t.Context()

	_, _, err := w.s.ApplyProfile(ctx, "", "name: codex-own\ndescription: d\nruntime: codex\n")
	require.NoError(t, err)
	_, _, err = w.s.ApplyProfile(ctx, "", "name: writer\ndescription: d\nruntime: claude\nfallbacks: [codex-own]\n")
	require.NoError(t, err)
	w.addNode("node_both", 4, "claude", "codex")
	w.addAccount("codex", "codex_1")
	require.NoError(t, w.s.DrainNode(ctx, w.node.ID))

	task := w.tasks(NewTask{Ref: "a", Profile: "writer", Spec: "a"})[0]
	created := w.schedule().Created
	require.Len(t, created, 1)

	call := w.begin(created[0])
	require.NoError(t, w.s.write(ctx, func(q *store.Queries, now int64) error {
		if err := q.InsertApproval(ctx, store.InsertApprovalParams{
			ID: "apr_1", Integration: "google", Label: "work", Action: "gmail.send", ActionName: "Send a mail",
			DispatchID: nullString(created[0]), TaskID: nullString(task.ID), RunID: nullString(task.RunID),
			AskedBy: "worker", Title: "Send a mail", Payload: "{}", MatchHash: "h", Now: now,
		}); err != nil {
			return err
		}

		_, err := q.DecideApproval(ctx, store.DecideApprovalParams{
			ID: "apr_1", Status: "approved", Scope: nullString("once"), Now: sql.NullInt64{Int64: now, Valid: true},
		})

		return err
	}))

	resets := t0.Add(48 * time.Hour)
	require.NoError(t, w.finish(call, Outcome{Class: FailureRateLimited, ResetsAt: &resets}))
	assert.NotNil(t, w.task(task.ID).NotBefore, "it waits for the reset")
	assert.Empty(t, w.schedule().Created)
}

// A profile's backups are checked when it is applied.
func TestFallbacksNameOtherWorkerProfiles(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	apply := func(source string) error {
		_, _, err := w.s.ApplyProfile(t.Context(), "", source)

		return err
	}

	require.ErrorIs(t, apply("name: a\ndescription: d\nruntime: claude\nfallbacks: [a]\n"), ErrInvalid)
	require.ErrorIs(t, apply("name: a\ndescription: d\nruntime: claude\nfallbacks: [missing]\n"), ErrNotFound)
	require.ErrorIs(t, apply("name: a\ndescription: d\nruntime: claude\nfallbacks: [reviewer, reviewer]\n"),
		ErrInvalid)
	require.ErrorIs(t, apply("name: lead\ndescription: d\nruntime: pi\nfallbacks: [reviewer]\n"), ErrInvalid)
	require.NoError(t, apply("name: a\ndescription: d\nruntime: claude\nfallbacks: [reviewer, explorer]\n"))
}

// Scenario: a Bot may give work to one profile only (issue #169). Its Task
// meets the limit of that profile's runtime: it does not move to a backup the
// Bot may not give work to, and waits; the owner's Task of the same profile
// moves to the backup.
func TestABotsTaskFallsBackOnlyToItsWorkers(t *testing.T) {
	t.Parallel()

	w := newSchedWorld(t)
	ctx := t.Context()

	_, err := w.s.ApplyProvider(ctx, "", "name: gateway\nkind: model_gateway\nmodels: [gpt-sol]\n")
	require.NoError(t, err)
	_, _, err = w.s.ApplyProfile(ctx, "",
		"name: codex-worker\ndescription: d\nruntime: codex\nmodel: {provider: gateway, name: gpt-sol}\n")
	require.NoError(t, err)
	_, _, err = w.s.ApplyProfile(ctx, "", "name: writer\ndescription: d\nruntime: claude\nfallbacks: [codex-worker]\n")
	require.NoError(t, err)

	_, err = w.s.PutBot(ctx, Bot{Name: "vmap", Workers: AllowList{Limited: true, Names: []string{"writer"}}})
	require.NoError(t, err)

	w.addNode("node_codex", 4, "codex")

	w.tasks(NewTask{Ref: "a", Profile: "writer", Spec: "a"})
	first := w.schedule().Created
	require.Len(t, first, 1)

	resets := t0.Add(2 * time.Hour)
	require.NoError(t, w.finish(w.begin(first[0]), Outcome{Class: FailureRateLimited, ResetsAt: &resets}))

	botRun, err := w.s.CreateRun(ctx, NewRun{Objective: "maps", CreatedBy: "pi", Bot: "vmap"})
	require.NoError(t, err)

	_, err = w.s.CreateTasks(ctx, botRun.ID, []NewTask{{Ref: "b", Profile: "writer", Spec: "the bot's"}})
	require.NoError(t, err)
	w.tasks(NewTask{Ref: "c", Profile: "writer", Spec: "the owner's"})

	// A Run a Bot's pi created in its chat before Runs recorded their Bot is that chat's Bot's.
	tok, _, err := w.s.AddToken(ctx, "pi-coordinator", bearertoken.Coordinator)
	require.NoError(t, err)

	chat, err := w.s.CreateChat(ctx, "vmap", "")
	require.NoError(t, err)

	older, err := w.s.CreateRun(ctx, NewRun{
		Objective: "older", CreatedBy: "pi", CoordinatorTokenID: tok.ID, ChatID: chat.ID,
	})
	require.NoError(t, err)

	_, err = w.s.CreateTasks(ctx, older.ID, []NewTask{{Ref: "d", Profile: "writer", Spec: "the older run's"}})
	require.NoError(t, err)

	out := w.schedule()
	require.Len(t, out.Created, 1, "the Bot's Tasks wait")
	d := w.dispatchOf(out.Created[0])
	assert.Equal(t, "the owner's", w.task(d.TaskID).Spec)
	assert.Equal(t, "writer", d.FallbackFor)
}
