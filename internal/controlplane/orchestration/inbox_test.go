package orchestration

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/inboxstore"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

// Scenario: the Inbox's migration on a database that already holds data (issue #845): a Bot, a chat and a pending
// Approval made before it stay, and the new tables take a to-do that is given to that Bot and waits for that
// Approval.
func TestInboxMigrationOnADatabaseWithData(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db, err := sqlite.Open(ctx, sqlite.Config{Path: filepath.Join(t.TempDir(), "hypomnema.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	p, err := provider(db)
	require.NoError(t, err)
	_, err = p.UpTo(ctx, 126)
	require.NoError(t, err)

	_, err = db.ExecContext(ctx, `INSERT INTO chats (id, title, bot_id, created_at, updated_at)
		VALUES ('cht_1', 'Chat', ?, 1, 1)`, DefaultBotID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO approvals (id, integration, label, action, action_name, chat_id,
		asked_by, title, payload, match_hash, status, created_at)
		VALUES ('apr_1', 'google', 'work', 'gmail.send', 'Send mail', 'cht_1', 'pi', 'Send mail', '[]', 'h',
		        'pending', ?)`, t0.UnixMilli()) // Asked as the attempt below starts, at the clock's time.
	require.NoError(t, err)

	require.NoError(t, Migrate(ctx, db))
	require.NoError(t, CheckSchema(ctx, db))

	s := New(db, &fakeClock{now: t0}, func(prefix string) string { return prefix + "_new" })
	bot := inboxstore.Actor{Kind: inboxstore.Bot, ID: DefaultBotID}

	td, err := s.Inbox().Create(ctx, bot, inboxstore.NewTodo{
		Title: "Send the contract", State: inboxstore.Open,
		Source:   inboxstore.Source{Kind: inboxstore.FromChat, ID: "cht_1"},
		Assignee: inboxstore.Assignee{Kind: inboxstore.ABot, Name: DefaultBotID},
	})
	require.NoError(t, err)

	td, err = s.Inbox().Start(ctx, inboxstore.Change{Actor: bot, ID: td.ID, Revision: td.Revision},
		inboxstore.Work{Kind: inboxstore.WorkChat, ID: "cht_1"})
	require.NoError(t, err)

	td, err = s.Inbox().LinkApproval(ctx, inboxstore.Change{
		Actor: inboxstore.Actor{Kind: inboxstore.System}, ID: td.ID, Revision: td.Revision}, "apr_1")
	require.NoError(t, err)
	assert.Equal(t, inboxstore.Review, td.State)

	n, err := s.Inbox().NeedsYou(ctx, t0)
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	a, err := s.GetApproval(ctx, "apr_1")
	require.NoError(t, err)
	assert.Equal(t, ApprovalPending, a.Status, "the Approval made before is kept")
}
