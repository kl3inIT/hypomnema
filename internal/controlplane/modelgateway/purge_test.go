package modelgateway_test

import (
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

// Scenario (issue #628): the daily clean-up. A request older than the Usage page's 30 days goes, a recent one
// stays. A revoked chat key that sent nothing goes; a revoked key whose only request is old goes with it; a
// revoked key with a recent request stays, so its request is still counted; a live key that sent nothing stays.
func TestTheCleanUpDeletesOldRequestsAndTheRevokedKeysNoneNames(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	db, err := sqlite.Open(ctx, sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	g := modelgateway.New(db, fixedClock{}, platform.NewID, nil, nil).WithKeyVault(testKeyVault(t, db))

	for _, chat := range []string{"cht_idle", "cht_old", "cht_recent"} {
		chatFor(t, db, chat, "bot_a")
	}

	idle, _, err := g.AddChatKey(ctx, "cht_idle", "bot_a", "")
	require.NoError(t, err)
	oldOnly, _, err := g.AddChatKey(ctx, "cht_old", "bot_a", "")
	require.NoError(t, err)
	recent, _, err := g.AddChatKey(ctx, "cht_recent", "bot_a", "")
	require.NoError(t, err)
	live, _, err := g.AddKey(ctx, "dongvl", nil, nil)
	require.NoError(t, err)

	for _, k := range []modelgateway.Key{idle, oldOnly, recent} {
		require.NoError(t, g.RevokeKey(ctx, k.ID))
	}

	now := fixedClock{}.Now()
	request(t, db, "old", oldOnly.ID, now.Add(-40*24*time.Hour))
	request(t, db, "new", recent.ID, now.Add(-2*24*time.Hour))

	purged, err := g.Purge(ctx)
	require.NoError(t, err)
	assert.Equal(t, modelgateway.Purged{Requests: 1, Keys: 2}, purged)

	keys, err := g.ListKeys(ctx)
	require.NoError(t, err)

	var left []string
	for _, k := range keys {
		left = append(left, k.ID)
	}

	assert.ElementsMatch(t, []string{recent.ID, live.ID}, left)

	again, err := g.Purge(ctx)
	require.NoError(t, err)
	assert.Equal(t, modelgateway.Purged{}, again, "a second clean-up has nothing to do")
}

// request records one answered request of a key at a time.
func request(t *testing.T, db *sql.DB, id, key string, at time.Time) {
	t.Helper()

	_, err := db.ExecContext(t.Context(), `INSERT INTO gateway_usage
		(id, key_id, model, provider, upstream_model, stream, attempts, outcome, status, duration_ms, created_at)
		VALUES (?, ?, 'sol', 'p', 'm', 0, 1, 'ok', 200, 5, ?)`, id, key, at.UnixMilli())
	require.NoError(t, err)
}
