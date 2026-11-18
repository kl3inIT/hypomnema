package orchestration

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/meetingstore"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

// Scenario: migration 00118 on a database with an uploaded meeting (issue #820): the meeting, its voices, lines,
// notes and items are all kept, as an upload, their voices on the track `file`; an action's voice is still named, a
// voice is still renamed, and the meeting is still deleted with all it holds.
func TestTheLiveMeetingsMigrationKeepsUploadedMeetings(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db, err := sqlite.Open(ctx, sqlite.Config{Path: filepath.Join(t.TempDir(), "hypomnema.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	p, err := provider(db)
	require.NoError(t, err)
	_, err = p.UpTo(ctx, 116)
	require.NoError(t, err)

	for _, stmt := range []string{
		`INSERT INTO meetings (id, title, titled, language, people, file_name, media_type, size, duration_ms, state,
			provider, model, created_at, updated_at)
		 VALUES ('mtg_old', 'Weekly', 1, 'vi', '["Lan"]', 'weekly.m4a', 'audio/mp4', 1234, 7000, 'transcribed',
			'soniox', 'stt-async-v5', 100, 200)`,
		`INSERT INTO meeting_speakers (meeting_id, label, position, name) VALUES
			('mtg_old', '1', 1, NULL), ('mtg_old', '2', 2, 'Lan')`,
		`INSERT INTO meeting_lines (meeting_id, n, start_ms, end_ms, speaker, heard, text) VALUES
			('mtg_old', 1, 0, 2000, '1', '1', 'Hôm nay chốt thứ Sáu.'),
			('mtg_old', 2, 2500, 6000, '2', '2', 'Em gửi giá thứ Tư.')`,
		`INSERT INTO meeting_notes (meeting_id, format, state, summary, sections, written_at, updated_at)
		 VALUES ('mtg_old', 'auto', 'ready', 'Launch on Friday.', '[]', 300, 300)`,
		`INSERT INTO meeting_items (id, meeting_id, kind, position, text, owner_speaker, due, line, start_ms) VALUES
			('mti_1', 'mtg_old', 'action', 0, 'Send the prices', '2', 'thứ Tư', 2, 2500)`,
		`INSERT INTO meeting_items (id, meeting_id, kind, position, text, line, start_ms) VALUES
			('mti_2', 'mtg_old', 'decision', 0, 'Launch on Friday', 1, 0)`,
	} {
		_, err := db.ExecContext(ctx, stmt)
		require.NoError(t, err, stmt)
	}

	require.NoError(t, Migrate(ctx, db))

	s := New(db, &fakeClock{now: t0}, func(prefix string) string { return prefix + "_new" })
	m := s.Meetings()

	got, err := m.Get(ctx, "mtg_old")
	require.NoError(t, err)
	assert.Equal(t, meetingstore.SourceUpload, got.Source)
	assert.Empty(t, got.Kind)
	assert.Equal(t, meetingstore.Transcribed, got.State)
	assert.Equal(t, int64(1234), got.Size)
	assert.Equal(t, []string{"Lan"}, got.People)
	assert.Equal(t, meetingstore.NotesReady, got.Notes.State)
	assert.Equal(t, "Launch on Friday.", got.Notes.Summary)
	require.Len(t, got.Speakers, 2)
	assert.Equal(t, meetingstore.TrackFile, got.Speakers[0].Track)
	assert.Equal(t, "Lan", got.Speakers[1].Shown())
	assert.Equal(t, 1, got.Actions)

	lines, err := m.Lines(ctx, "mtg_old")
	require.NoError(t, err)
	require.Len(t, lines, 2)
	assert.Equal(t, meetingstore.TrackFile, lines[1].Track)
	assert.Equal(t, "Em gửi giá thứ Tư.", lines[1].Text)

	items, err := m.Items(ctx, "mtg_old")
	require.NoError(t, err)
	require.Len(t, items, 2)

	actions, err := m.Actions(ctx)
	require.NoError(t, err)
	require.Len(t, actions, 1)
	assert.Equal(t, "Lan", actions[0].Who, "the action's voice is still named")
	assert.Equal(t, meetingstore.TrackFile, actions[0].OwnerTrack)

	require.NoError(t, m.NameSpeaker(ctx, "mtg_old", "", "1", "Minh"))

	actions, err = m.Actions(ctx)
	require.NoError(t, err)
	assert.Equal(t, "Lan", actions[0].Who)

	require.NoError(t, m.Delete(ctx, "mtg_old"))

	for _, table := range []string{"meeting_speakers", "meeting_lines", "meeting_notes", "meeting_items"} {
		var n int
		require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&n))
		assert.Zero(t, n, table)
	}
}
