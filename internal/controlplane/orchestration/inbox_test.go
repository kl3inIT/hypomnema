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
// Approval made before it stay; the Approval, asked by no attempt, is no to-do's, even one started in its chat at
// the same moment; and the new tables take a to-do given to that Bot that waits for an Approval its attempt asks.
func TestInboxMigrationOnADatabaseWithData(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db, err := sqlite.Open(ctx, sqlite.Config{Path: filepath.Join(t.TempDir(), "hypomnema.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	p, err := provider(db)
	require.NoError(t, err)
	_, err = p.UpTo(ctx, 135) // main's last before the Inbox's 00139.
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

	system := inboxstore.Actor{Kind: inboxstore.System}
	_, err = s.Inbox().LinkApproval(ctx, inboxstore.Change{Actor: system, ID: td.ID, Revision: td.Revision}, "apr_1")
	require.ErrorIs(t, err, ErrConflict, "an Approval made before the Inbox is no attempt's")

	asked, granted, err := s.AskApproval(ctx, NewApproval{
		Integration: "google", Label: "work", Action: "gmail.send", ActionName: "Send mail", ChatID: "cht_1",
		AttemptID: td.Work.Attempt, AskedBy: "pi", Title: "Send mail", MatchHash: "h",
	})
	require.NoError(t, err)
	require.False(t, granted)
	assert.NotEqual(t, "apr_1", asked.ID, "the same request in an attempt is its own Approval")
	assert.Equal(t, td.Work.Attempt, asked.AttemptID)

	td, err = s.Inbox().LinkApproval(ctx, inboxstore.Change{Actor: system, ID: td.ID, Revision: td.Revision}, asked.ID)
	require.NoError(t, err)
	assert.Equal(t, inboxstore.Review, td.State)

	n, err := s.Inbox().NeedsYou(ctx, t0)
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	a, err := s.GetApproval(ctx, "apr_1")
	require.NoError(t, err)
	assert.Equal(t, ApprovalPending, a.Status, "the Approval made before is kept")
}
