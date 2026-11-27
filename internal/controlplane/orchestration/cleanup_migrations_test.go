package orchestration

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Scenario (issue #1220): the migrations that drop what finished migrations left (the model-name conversion's
// tables, the plaintext share token and its scrub mark, dispatch_resources) go back and come again. A link made
// before them still opens afterwards, and keeps its hash and sealed copy on the way back.
func TestTheCleanupMigrationsGoBackAndComeAgain(t *testing.T) {
	t.Parallel()

	w, _, _ := credentialWorld(t)
	ctx := t.Context()

	chat, err := w.s.CreateChat(ctx, "", "")
	require.NoError(t, err)

	sh := w.shareOf(chat.ID, chat.Bot)

	p, err := provider(w.db)
	require.NoError(t, err)

	before := migrationVersion(t, "_drop_model_name_conversion.sql") - 1
	_, err = p.DownTo(ctx, before)
	require.NoError(t, err)

	var hash, version string
	require.NoError(t, w.db.QueryRowContext(ctx,
		"SELECT token_hash, secret_version_id FROM artifact_shares WHERE id = ? AND token IS NULL", sh.ID).
		Scan(&hash, &version), "the link is back with its hash and sealed copy, and no plaintext")
	assert.NotEmpty(t, hash)
	assert.NotEmpty(t, version)

	for _, table := range []string{"dispatch_resources", "share_token_scrub", "model_name_conversion",
		"model_name_changes", "model_provider_changes"} {
		_, err := w.db.ExecContext(ctx, "SELECT count(*) FROM "+table) //nolint:gosec // the names above
		require.NoError(t, err, "%s is back", table)
	}

	_, err = p.Up(ctx)
	require.NoError(t, err)
	require.NoError(t, CheckSchema(ctx, w.db))

	shown, err := w.s.SharedArtifact(ctx, sh.Token)
	require.NoError(t, err)
	assert.Equal(t, sh.ID, shown.ShareID)

	var violations int
	require.NoError(t, w.db.QueryRowContext(ctx, "SELECT count(*) FROM pragma_foreign_key_check").Scan(&violations))
	assert.Zero(t, violations)
}
