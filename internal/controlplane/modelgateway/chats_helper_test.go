package modelgateway_test

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// chatFor makes the chat a chat's key is for, with its Bot (the main Bot for ""), as orchestration keeps them: the
// gateway makes a chat's key only while its chat is there with its Bot (issue #996).
func chatFor(t *testing.T, db *sql.DB, chatID, botID string) {
	t.Helper()

	if botID == "" {
		botID = orchestration.DefaultBotID
	}

	_, err := db.ExecContext(t.Context(), `INSERT OR IGNORE INTO bots (id, name, position, created_at, updated_at)
		VALUES (?, ?, (SELECT coalesce(max(position), -1) + 1 FROM bots), 1, 1)`, botID, botID)
	require.NoError(t, err)

	_, err = db.ExecContext(t.Context(), `INSERT OR IGNORE INTO chats (id, title, created_at, updated_at, bot_id)
		VALUES (?, '', 1, 1, ?)`, chatID, botID)
	require.NoError(t, err)
}

func (g chatGateway) chat(t *testing.T, chatID, botID string) {
	t.Helper()

	chatFor(t, g.db, chatID, botID)
}
