package orchestration

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Scenario: the owner gives a node a second token and revokes the first. Each
// token works on its own; a revoked one is unknown, also to a node that is
// still connected with it.
func TestNodeTokensAreAddedAndRevoked(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	n, first, err := w.s.AddNode(t.Context(), "node_new", 0)
	require.NoError(t, err)

	added, second, err := w.s.AddNodeToken(t.Context(), n.ID, "rotation")
	require.NoError(t, err)
	assert.NotEqual(t, first, second)

	_, firstID, err := w.s.AuthenticateNodeToken(t.Context(), first)
	require.NoError(t, err)
	require.NoError(t, w.s.CheckNodeToken(t.Context(), firstID))

	require.NoError(t, w.s.RevokeNodeToken(t.Context(), firstID))

	_, _, err = w.s.AuthenticateNodeToken(t.Context(), first)
	require.ErrorIs(t, err, ErrNotFound)
	require.ErrorIs(t, w.s.CheckNodeToken(t.Context(), firstID), ErrNotFound, "a connected node is refused too")
	require.ErrorIs(t, w.s.RevokeNodeToken(t.Context(), firstID), ErrNotFound, "it is revoked once")

	got, _, err := w.s.AuthenticateNodeToken(t.Context(), second)
	require.NoError(t, err)
	assert.Equal(t, n.ID, got.ID)

	tokens, err := w.s.ListNodeTokens(t.Context(), n.ID)
	require.NoError(t, err)
	require.Len(t, tokens, 2)
	assert.NotNil(t, tokens[0].RevokedAt)
	assert.NotNil(t, tokens[0].LastUsedAt)
	assert.Equal(t, added.ID, tokens[1].ID)
	assert.Nil(t, tokens[1].RevokedAt)

	_, _, err = w.s.AddNodeToken(t.Context(), "nod_missing", "x")
	require.ErrorIs(t, err, ErrNotFound)
}

// Scenario: the owner removes a node. While it runs a Dispatch it is
// refused; afterwards its token is unknown, it is not listed, it takes no
// heartbeat, and its name can be given to a new node.
func TestRemovedNodeIsGoneAndItsNameIsFree(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	task := w.tasks(NewTask{Ref: "x", Profile: "implementer", Spec: "s"})[0]
	w.dispatch(task.ID)

	require.ErrorIs(t, w.s.RemoveNode(t.Context(), w.node.ID), ErrConflict)

	n, token, err := w.s.AddNode(t.Context(), "node_idle", 0)
	require.NoError(t, err)
	require.NoError(t, w.s.RemoveNode(t.Context(), n.ID))

	_, _, err = w.s.AuthenticateNodeToken(t.Context(), token)
	require.ErrorIs(t, err, ErrNotFound)
	require.ErrorIs(t, w.s.NodeHeartbeat(t.Context(), n.ID), ErrNotFound)
	require.ErrorIs(t, w.s.NodeHello(t.Context(), n.ID, Hello{WorktreeRoot: "/srv/wt"}), ErrNotFound)
	require.ErrorIs(t, w.s.ResumeNode(t.Context(), n.ID), ErrNotFound)
	require.ErrorIs(t, w.s.RemoveNode(t.Context(), n.ID), ErrNotFound)

	nodes, err := w.s.ListNodes(t.Context())
	require.NoError(t, err)

	for _, listed := range nodes {
		assert.NotEqual(t, n.ID, listed.ID)
	}

	again, _, err := w.s.AddNode(t.Context(), "node_idle", 0)
	require.NoError(t, err)
	assert.NotEqual(t, n.ID, again.ID)
}

// Scenario: a node says what it is when it connects. The registry shows it;
// what the node could not find stays empty.
func TestHelloRecordsWhatTheNodeIs(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	sys := SystemInfo{DaemonVersion: "v0.9.2", OS: "linux", Arch: "arm64", CPUs: 8, MemoryBytes: 16 << 30}

	require.NoError(t, w.s.NodeHello(t.Context(), w.node.ID, Hello{WorktreeRoot: "/srv/wt", System: sys}))

	got, err := w.s.GetNode(t.Context(), w.node.ID)
	require.NoError(t, err)
	assert.Equal(t, sys, got.System)
}

// Scenario: a database from before node tokens had their own table. After
// the migration the node's token still authenticates it, so no node has to
// be set up again.
func TestMigrationKeepsNodeTokens(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db, _ := databaseAt(t, 15)

	const token = "hyn_from_before"

	_, err := db.ExecContext(ctx, `INSERT INTO nodes (id, name, token_hash, status, max_dispatches, runtimes,
		created_at, updated_at) VALUES ('nod_old', 'zm', ?, 'drained', 3, '[]', 100, 100)`, tokenHash(token))
	require.NoError(t, err)

	require.NoError(t, Migrate(ctx, db))
	require.NoError(t, CheckSchema(ctx, db))

	s := New(db, &fakeClock{now: t0}, func(prefix string) string { return prefix + "_new" })

	n, _, err := s.AuthenticateNodeToken(ctx, token)
	require.NoError(t, err)
	assert.Equal(t, "nod_old", n.ID)
	assert.Equal(t, NodeDrained, n.Status)
	assert.Equal(t, 0, n.MaxDispatches, "00114 cleared every node's cap (issue #731)")
}

// Scenario: a node sends what it measured. The registry shows the latest
// measurement with its time, none before the first, and refuses one that
// cannot be true.
func TestNodeStatsKeepTheLatestMeasurement(t *testing.T) {
	t.Parallel()

	w := newWorld(t)

	before, err := w.s.GetNode(t.Context(), w.node.ID)
	require.NoError(t, err)
	assert.Nil(t, before.Stats)

	require.NoError(t, w.s.NodeStats(t.Context(), w.node.ID, SystemStats{CPUPercent: 10, MemoryUsedBytes: 1 << 30}))
	require.NoError(t, w.s.NodeStats(t.Context(), w.node.ID, SystemStats{
		CPUPercent: 38.5, MemoryUsedBytes: 9 << 30, DiskUsedBytes: 81 << 30, Load1: 3.1,
	}))

	got, err := w.s.GetNode(t.Context(), w.node.ID)
	require.NoError(t, err)
	require.NotNil(t, got.Stats)
	assert.Equal(t, SystemStats{
		CPUPercent: 38.5, MemoryUsedBytes: 9 << 30, DiskUsedBytes: 81 << 30, Load1: 3.1, At: w.clock.Now().UTC(),
	}, *got.Stats)

	require.ErrorIs(t, w.s.NodeStats(t.Context(), w.node.ID, SystemStats{CPUPercent: 140}), ErrInvalid)
	require.ErrorIs(t, w.s.NodeStats(t.Context(), "nod_missing", SystemStats{}), ErrNotFound)
}

// Scenario: the owner renames a machine. It keeps its id, its token and its
// place in the list under the new name; the name of another live machine is
// refused, the name of a removed one is free, and an empty name is refused.
func TestRenamedNodeKeepsEverythingButItsName(t *testing.T) {
	t.Parallel()

	s, _, _ := newTestService(t)
	ctx := t.Context()

	n, token, err := s.AddNode(ctx, "kdlstc-dev", 0)
	require.NoError(t, err)

	other, _, err := s.AddNode(ctx, "builder", 0)
	require.NoError(t, err)

	gone, _, err := s.AddNode(ctx, "old-name", 0)
	require.NoError(t, err)
	require.NoError(t, s.RemoveNode(ctx, gone.ID))

	renamed, err := s.RenameNode(ctx, n.ID, "  stc-dev  ")
	require.NoError(t, err)
	assert.Equal(t, n.ID, renamed.ID)
	assert.Equal(t, "stc-dev", renamed.Name)

	still, _, err := s.AuthenticateNodeToken(ctx, token)
	require.NoError(t, err)
	assert.Equal(t, "stc-dev", still.Name)

	_, err = s.RenameNode(ctx, n.ID, other.Name)
	require.ErrorIs(t, err, ErrConflict)

	_, err = s.RenameNode(ctx, n.ID, "old-name")
	require.NoError(t, err)

	_, err = s.RenameNode(ctx, n.ID, "   ")
	require.ErrorIs(t, err, ErrInvalid)

	_, err = s.RenameNode(ctx, gone.ID, "back")
	require.ErrorIs(t, err, ErrNotFound)
}

// Scenario: a node with two GPUs sends what they measured, then a machine
// whose GPU went away sends none. The registry shows the GPUs of the latest
// measurement, none once the node reports none, and refuses a GPU that
// cannot be true.
func TestNodeStatsKeepTheLatestGPUs(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	temp, power := 74.0, 312.5

	gpus := []GPUStats{
		{
			Index: 0, Name: "GeForce RTX 4090", UsagePercent: 87, MemoryUsedBytes: 22 << 30, MemoryTotalBytes: 24 << 30,
			TemperatureCelsius: &temp, PowerWatts: &power,
		},
		{Index: 1, Name: "RTX A6000", UsagePercent: 3, MemoryUsedBytes: 1 << 29, MemoryTotalBytes: 48 << 30},
	}
	require.NoError(t, w.s.NodeStats(t.Context(), w.node.ID, SystemStats{CPUPercent: 10, GPUs: gpus}))

	got, err := w.s.GetNode(t.Context(), w.node.ID)
	require.NoError(t, err)
	require.NotNil(t, got.Stats)
	assert.Equal(t, gpus, got.Stats.GPUs)

	require.NoError(t, w.s.NodeStats(t.Context(), w.node.ID, SystemStats{CPUPercent: 10}))

	got, err = w.s.GetNode(t.Context(), w.node.ID)
	require.NoError(t, err)
	assert.Empty(t, got.Stats.GPUs)

	for _, bad := range []GPUStats{
		{Index: 0, Name: "x", UsagePercent: 140},
		{Index: 0, Name: "", UsagePercent: 10},
		{Index: -1, Name: "x", UsagePercent: 10},
		{Index: 0, Name: "x", UsagePercent: 10, MemoryUsedBytes: -1},
	} {
		require.ErrorIs(t, w.s.NodeStats(t.Context(), w.node.ID, SystemStats{GPUs: []GPUStats{bad}}), ErrInvalid)
	}
}

// Scenario: migration 00114 on a database with a capped node, a Bot with its
// own limit and the seeded profiles (issue #731). The node's cap is cleared, a
// cap can be set on it again, and the Bot keeps its number as its guarantee;
// the seeded explorer asks for half a core and 512 MiB, and a reserve starts
// at 0.
func TestMigrationClearsCapsAndKeepsBotsNumbers(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db, _ := databaseAt(t, 113)

	_, err := db.ExecContext(ctx, `INSERT INTO nodes (id, name, status, max_dispatches, runtimes, created_at, updated_at)
		VALUES ('nod_elitex', 'elitex', 'active', 1, '[]', 100, 100)`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE bots SET max_dispatches = 8 WHERE id = ?`, DefaultBotID)
	require.NoError(t, err)

	require.NoError(t, Migrate(ctx, db))

	s := New(db, &fakeClock{now: t0}, func(prefix string) string { return prefix + "_new" })

	n, err := s.GetNode(ctx, "nod_elitex")
	require.NoError(t, err)
	assert.Equal(t, 0, n.MaxDispatches, "no cap")
	assert.Equal(t, Resources{}, n.Reserved)

	n, err = s.SetNodeLimit(ctx, "nod_elitex", 1)
	require.NoError(t, err)
	assert.Equal(t, 1, n.MaxDispatches, "a cap can be set again")

	bot, err := s.GetBot(ctx, DefaultBotID)
	require.NoError(t, err)
	assert.Equal(t, 8, bot.Guarantee())

	explorer, err := s.GetProfile(ctx, "explorer")
	require.NoError(t, err)
	assert.Equal(t, [2]int64{500, 512 << 20}, [2]int64{explorer.CPURequestMillis, explorer.MemoryRequestBytes})
}
