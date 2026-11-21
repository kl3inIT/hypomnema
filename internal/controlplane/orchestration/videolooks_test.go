package orchestration

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

// Scenario: a database from before the video looks (issue #937). Every revision, the queued one included, gets the
// captions it had and no frame, and pins no voice; the default look starts as Clean, android, midnight. The way
// back drops the columns and the table and keeps the rows.
func TestMigrationGivesEveryRevisionItsLegacyLook(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db, err := sqlite.Open(ctx, sqlite.Config{Path: filepath.Join(t.TempDir(), "hypomnema.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	p, err := provider(db)
	require.NoError(t, err)
	_, err = p.UpTo(ctx, 147)
	require.NoError(t, err)

	for _, stmt := range []string{
		`INSERT INTO videos (id, take_id, title, made_by, created_at) VALUES ('vid_1', 'tk_1', 'Một', 'owner', 1)`,
		`INSERT INTO video_revisions (video_id, rev, state, lines, captions, aspect, requested_by, created_at,
			updated_at) VALUES ('vid_1', 1, 'review', '["a"]', 1, '9:16', 'owner', 1, 1)`,
		`INSERT INTO video_revisions (video_id, rev, parent, state, lines, captions, aspect, requested_by, created_at,
			updated_at) VALUES ('vid_1', 2, 1, 'queued', '["a"]', 1, '16:9', 'owner', 2, 2)`,
	} {
		_, err = db.ExecContext(ctx, stmt)
		require.NoError(t, err, stmt)
	}

	require.NoError(t, Migrate(ctx, db))
	require.NoError(t, CheckSchema(ctx, db))

	looks := func() []string {
		rows, err := db.QueryContext(ctx, `SELECT look, pin_provider, pin_voice FROM video_revisions ORDER BY rev`)
		require.NoError(t, err)

		defer func() { _ = rows.Close() }()

		var out []string

		for rows.Next() {
			var look, provider, voice string
			require.NoError(t, rows.Scan(&look, &provider, &voice))
			assert.Empty(t, provider+voice, "no voice is pinned")

			out = append(out, look)
		}

		require.NoError(t, rows.Err())

		return out
	}

	legacy := `{"captions":{"preset":"legacy"},"frame":{"chrome":"full"}}`
	assert.Equal(t, []string{legacy, legacy}, looks(), "the queued revision too")

	var def string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT look FROM video_settings WHERE id = 1`).Scan(&def))
	assert.JSONEq(t, `{"captions":{"preset":"clean","position":"preset"},`+
		`"frame":{"chrome":"android","padding":48,"radius":40,"shadow":true,"safe_top":false},`+
		`"background":{"preset":"midnight"}}`, def)

	_, err = db.ExecContext(ctx, `INSERT INTO video_settings (id, look, updated_at) VALUES (2, '{}', 1)`)
	require.Error(t, err, "one row")

	_, err = p.DownTo(ctx, 147)
	require.NoError(t, err)

	var n int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM video_revisions`).Scan(&n))
	assert.Equal(t, 2, n)
}
