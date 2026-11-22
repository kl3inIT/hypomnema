package whatsapp

import (
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/messaging"
)

// Review of #1037, round 2: WhatsApp keeps a message's time in seconds, so more messages than a page share one
// second; two of them have the same id in two chats. Paging by offset reads each once.
func TestMessagesPageByOffsetPastEqualSeconds(t *testing.T) {
	t.Parallel()

	svc, err := Open(t.Context(), filepath.Join(t.TempDir(), "whatsapp.db"), slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.Close() })

	for i, row := range [][2]string{{"a@g.us", "m1"}, {"b@g.us", "m1"}, {"a@g.us", "m2"}} {
		_, err := svc.mirror.db.ExecContext(t.Context(), `INSERT INTO wa_messages (account, chat_jid, msg_id, ts,
			from_me, text) VALUES ('84900000001', ?, ?, 1700000000, 0, ?)`, row[0], row[1], i)
		require.NoError(t, err)
	}

	seen := map[string]bool{}

	for offset := 0; offset < 3; offset += 2 {
		page, err := svc.mirror.messages(t.Context(), "84900000001", messaging.Query{Limit: 2, Offset: offset})
		require.NoError(t, err)

		for _, msg := range page {
			seen[msg.Chat+"/"+msg.ID] = true
		}
	}

	assert.Equal(t, map[string]bool{"a@g.us/m1": true, "b@g.us/m1": true, "a@g.us/m2": true}, seen)
}
