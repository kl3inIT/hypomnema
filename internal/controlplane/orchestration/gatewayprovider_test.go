package orchestration

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

// beforeGatewayKind is a database from before migration 00072 with the old
// gateway provider, as zm had it: an OpenAI-compatible provider at the
// control plane's own /v1, which the Bot "coordinator" and a chat use.
func beforeGatewayKind(t *testing.T, withModelGateway bool) *sql.DB {
	t.Helper()

	ctx := t.Context()
	db, err := sqlite.Open(ctx, sqlite.Config{Path: filepath.Join(t.TempDir(), "hypomnema.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	p, err := provider(db)
	require.NoError(t, err)
	_, err = p.UpTo(ctx, 71)
	require.NoError(t, err)

	stmts := []string{
		`INSERT INTO providers (name, kind, base_url, api_key_ref, source, created_at, updated_at)
			VALUES ('gateway', 'openai_compatible', 'http://127.0.0.1:18990/v1', 'vault:gateway',
			        'name: gateway', 100, 100)`,
		`INSERT INTO models (provider, name) VALUES ('gateway', 'gpt-6.1-sol')`,
		`UPDATE bots SET provider = 'gateway', model = 'gpt-6.1-sol' WHERE name = 'coordinator'`,
		`INSERT INTO chats (id, title, bot, provider, model, created_at, updated_at)
			VALUES ('cht_1', 'hi', 'coordinator', 'gateway', 'gpt-6.1-sol', 100, 100)`,
	}
	if withModelGateway {
		stmts = append(stmts,
			`INSERT INTO providers (name, kind, source, created_at, updated_at)
				VALUES ('model-gateway', 'model_gateway', 'name: model-gateway', 100, 100)`,
			`INSERT INTO models (provider, name) VALUES ('model-gateway', 'gpt-6.1-sol')`)
	}

	for _, stmt := range stmts {
		_, err = db.ExecContext(ctx, stmt)
		require.NoError(t, err, stmt)
	}

	require.NoError(t, Migrate(ctx, db))

	return db
}

func providerOf(ctx context.Context, t *testing.T, db *sql.DB, query string) string {
	t.Helper()

	var name sql.NullString
	require.NoError(t, db.QueryRowContext(ctx, query).Scan(&name))

	return name.String
}

// Scenario: the Bot and the chat on the old gateway provider move to the
// provider of kind model_gateway, which offers their model, and the old
// provider goes. Without a model_gateway provider nothing is touched.
func TestMigrationMovesTheOldGatewayProviderToTheModelGateway(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	db := beforeGatewayKind(t, true)
	assert.Equal(t, "model-gateway", providerOf(ctx, t, db, `SELECT provider FROM bots WHERE name = 'coordinator'`))
	assert.Equal(t, "model-gateway", providerOf(ctx, t, db, `SELECT provider FROM chats WHERE id = 'cht_1'`))

	var left int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM providers WHERE name = 'gateway'`).Scan(&left))
	assert.Zero(t, left)

	untouched := beforeGatewayKind(t, false)
	assert.Equal(t, "gateway", providerOf(ctx, t, untouched, `SELECT provider FROM bots WHERE name = 'coordinator'`))
	assert.Equal(t, "gateway", providerOf(ctx, t, untouched, `SELECT name FROM providers WHERE name = 'gateway'`))
}
