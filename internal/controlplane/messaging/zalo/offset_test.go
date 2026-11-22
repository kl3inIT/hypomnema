package zalo

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/messaging"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

// Review of #1037, round 2: more messages than a page sent in the same millisecond, two of them with the same id in
// two chats. Paging by offset reads each once; paging by time alone cannot get past the millisecond.
func TestMessagesPageByOffsetPastEqualTimes(t *testing.T) {
	t.Parallel()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: filepath.Join(t.TempDir(), "zalo.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	m, err := openMirror(t.Context(), db)
	require.NoError(t, err)

	for i, row := range [][2]string{{"g1", "m1"}, {"g2", "m1"}, {"g1", "m2"}} {
		_, err := db.ExecContext(t.Context(), `INSERT INTO zl_messages (account, chat_id, msg_id, ts, from_me, text)
			VALUES ('100', ?, ?, 1700000000000, 0, ?)`, row[0], row[1], i)
		require.NoError(t, err)
	}

	seen := map[string]bool{}

	for offset := 0; offset < 3; offset += 2 {
		page, err := m.messages(t.Context(), "100", messaging.Query{Limit: 2, Offset: offset})
		require.NoError(t, err)

		for _, msg := range page {
			seen[msg.Chat+"/"+msg.ID] = true
		}
	}

	assert.Equal(t, map[string]bool{"g1/m1": true, "g2/m1": true, "g1/m2": true}, seen)
}
