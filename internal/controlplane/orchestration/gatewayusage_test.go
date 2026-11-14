package orchestration

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

// Scenario: a database from before the one-hour cache writes (migration
// 00078, issue #448). A request recorded before keeps its counts and its cost,
// with no one-hour part; the way back drops the column and keeps the row.
func TestMigrationAddsTheCacheWrittenForAnHour(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db, err := sqlite.Open(ctx, sqlite.Config{Path: filepath.Join(t.TempDir(), "hypomnema.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	p, err := provider(db)
	require.NoError(t, err)
	_, err = p.UpTo(ctx, 77)
	require.NoError(t, err)

	for _, stmt := range []string{
		`INSERT INTO gateway_keys (id, name, key_hash, models, created_at) VALUES ('gwk_1', 'pi', 'h', '["*"]', 1)`,
		`INSERT INTO gateway_usage (id, key_id, model, provider, upstream_model, stream, attempts, outcome, status,
			prompt_tokens, completion_tokens, cache_write_tokens, cost_micros, duration_ms, created_at)
			VALUES ('gwu_1', 'gwk_1', 'sol', 'claude-subscription', 'claude-sonnet-5-5', 1, 1, 'ok', 200,
			        100, 10, 50, 420, 5, 1)`,
	} {
		_, err = db.ExecContext(ctx, stmt)
		require.NoError(t, err, stmt)
	}

	require.NoError(t, Migrate(ctx, db))
	require.NoError(t, CheckSchema(ctx, db))

	var written, hour, cost int64
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT cache_write_tokens, cache_write_1h_tokens, cost_micros FROM gateway_usage WHERE id = 'gwu_1'`).
		Scan(&written, &hour, &cost))
	assert.Equal(t, []int64{50, 0, 420}, []int64{written, hour, cost}, "nothing is recomputed")

	_, err = p.DownTo(ctx, 77)
	require.NoError(t, err)

	var n int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM gateway_usage`).Scan(&n))
	assert.Equal(t, 1, n)
}
