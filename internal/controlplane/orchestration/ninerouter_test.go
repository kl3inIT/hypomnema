package orchestration

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

// Scenario: zm keeps the provider ninerouter, 9router that nothing has used
// since 2026-11-10 (issue #413). Migration 00074 removes it with its models;
// one a Bot still names is left.
func TestMigrationRemovesTheUnusedNinerouterProvider(t *testing.T) {
	t.Parallel()

	for _, used := range []bool{false, true} {
		ctx := t.Context()
		db, err := sqlite.Open(ctx, sqlite.Config{Path: filepath.Join(t.TempDir(), "hypomnema.db")})
		require.NoError(t, err)
		t.Cleanup(func() { _ = db.Close() })

		p, err := provider(db)
		require.NoError(t, err)
		_, err = p.UpTo(ctx, 73)
		require.NoError(t, err)

		stmts := []string{
			`INSERT INTO providers (name, kind, base_url, api_key_ref, source, created_at, updated_at)
				VALUES ('ninerouter', 'openai_compatible', 'http://172.22.0.1:20128/v1', 'vault:ninerouter',
				        'name: ninerouter', 100, 100)`,
			`INSERT INTO models (provider, name) VALUES ('ninerouter', 'cx/gpt-6.1-sol')`,
		}
		if used {
			stmts = append(stmts, `UPDATE bots SET provider = 'ninerouter' WHERE name = 'coordinator'`)
		}

		for _, stmt := range stmts {
			_, err = db.ExecContext(ctx, stmt)
			require.NoError(t, err, stmt)
		}

		require.NoError(t, Migrate(ctx, db))

		var providers, models int
		require.NoError(t, db.QueryRowContext(ctx,
			`SELECT count(*) FROM providers WHERE name = 'ninerouter'`).Scan(&providers))
		require.NoError(t, db.QueryRowContext(ctx,
			`SELECT count(*) FROM models WHERE provider = 'ninerouter'`).Scan(&models))

		if used {
			assert.Equal(t, 1, providers, "a provider a Bot names stays")
		} else {
			assert.Equal(t, 0, providers)
			assert.Equal(t, 0, models, "its models go with it")
		}
	}
}
