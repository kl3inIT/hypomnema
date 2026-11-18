package orchestration

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

// Scenario: a control plane with every kind of chat takes the migration that gives each Bot a main chat (issue
// #471). The main Bot has a web chat with live work (a Run not finished, a pending and a standing Approval, a
// queued message, a cut turn, attached files and an artifact), a chat the CLI wrote to last, the owner's Lark DM
// (still mapped), another person's Lark DM whose mapping a /new replaced, a Lark group's chat, and two Routine
// chats, one whose run was pruned; every chat but the CLI's has a later owner line. Its main chat is the CLI's. A Bot
// whose chats tie on the owner's latest line takes the one active last; a chat with no owner line loses to one with
// any; a Bot with no chat, or only Lark and Routine chats, has none. No other row changes.
func TestMigrationGivesEachBotAMainChat(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db, err := sqlite.Open(ctx, sqlite.Config{Path: filepath.Join(t.TempDir(), "hypomnema.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	p, err := provider(db)
	require.NoError(t, err)
	_, err = p.UpTo(ctx, 122)
	require.NoError(t, err)

	main := DefaultBotID
	stmts := []string{
		`INSERT INTO bots (id, name, created_at, updated_at) VALUES ('bot_tie', 'tie', 1, 1), ('bot_none', 'none', 1, 1),
			('bot_side', 'side', 1, 1), ('bot_quiet', 'quiet', 1, 1)`,
		`INSERT INTO channels (name, kind, app_id, secret, bot_id, mode, owner_id, created_at, updated_at)
			VALUES ('lark', 'lark', 'cli_a', 's', '` + main + `', 'owner', 'ou_owner', 1, 1)`,

		// The main Bot's chats.
		chatRow("cht_web", main, 0, 9000),
		`UPDATE chats SET turn_started_at = 8800 WHERE id = 'cht_web'`,
		chatRow("cht_cli", main, 0, 3000),
		chatRow("cht_dm_owner", main, 0, 9500),
		chatRow("cht_dm_other", main, 0, 9600),
		chatRow("cht_group", main, 1, 9700),
		chatRow("cht_routine", main, 0, 9800),
		chatRow("cht_pruned", main, 0, 9900),
		`INSERT INTO channel_chats (external_chat_id, channel, person_id, chat_id, sent_seq, created_at)
			VALUES ('oc_owner', 'lark', 'ou_owner', 'cht_dm_owner', 0, 1)`,
		`INSERT INTO routines (id, bot_id, name, kind, prompt, timeout_seconds, state, created_by, created_at,
			updated_at) VALUES ('rtn_a', '` + main + `', 'digest', 'prompt', 'sum up', 60, 'on', 'owner', 1, 1)`,
		`INSERT INTO routines (id, bot_id, name, kind, script, timeout_seconds, state, created_by, chat_id,
			created_at, updated_at) VALUES ('rtn_b', '` + main + `', 'sync', 'script', 'sync.sh', 60, 'on', 'owner',
			'cht_web', 1, 1)`,
		`INSERT INTO routine_runs (id, routine_id, started_by, state, started_at, chat_id)
			VALUES ('rrn_a', 'rtn_a', 'schedule', 'running', 9000, 'cht_routine')`,
		`INSERT INTO routine_runs (id, routine_id, started_by, state, started_at, ended_at, output)
			VALUES ('rrn_b', 'rtn_b', 'schedule', 'succeeded', 9000, 9001, 'done')`,
		msgRow("cht_web", "owner", "", "the health page", "NULL", 2000) + `, '[{"name":"a.png","path":"attachments/cht_web/a.png","size":1}]')`,
		msgRow("cht_web", "owner", "", "and its test", "NULL", 2500) + `, NULL)`,
		msgRow("cht_cli", "owner", "", "from the terminal", "NULL", 2900) + `, NULL)`,
		msgRow("cht_dm_owner", "owner", "", "from my Lark", "NULL", 9500) + `, NULL)`,
		msgRow("cht_dm_other", "owner", "", "hi bot", "'ou_other'", 9600) + `, NULL)`,
		msgRow("cht_group", "owner", "", "in the group", "NULL", 9700) + `, NULL)`,
		msgRow("cht_routine", "system", "wake", `Routine "digest" (rtn_a) runs by its schedule.`, "NULL", 9000) + `, NULL)`,
		msgRow("cht_routine", "owner", "", "and also this", "NULL", 9800) + `, NULL)`,
		msgRow("cht_pruned", "system", "wake", `Routine "old" (rtn_x) runs by hand, now.`, "NULL", 9000) + `, NULL)`,
		msgRow("cht_pruned", "owner", "", "keep going", "NULL", 9900) + `, NULL)`,
		`UPDATE chat_messages SET to_coordinator = 1 WHERE kind = 'wake'`,
		`INSERT INTO runs (id, objective, status, created_by, chat_id, bot_id, created_at, updated_at)
			VALUES ('run_live', 'tiles', 'open', 'coordinator', 'cht_web', '` + main + `', 2600, 2600)`,
		`INSERT INTO approvals (id, integration, label, action, action_name, chat_id, asked_by, title, payload,
			match_hash, status, created_at) VALUES ('apr_wait', 'lark', 'me', 'send', 'Send', 'cht_web', 'bot',
			'Send it', '{}', 'h1', 'pending', 2700)`,
		`INSERT INTO approvals (id, integration, label, action, action_name, chat_id, asked_by, title, payload,
			match_hash, status, scope, standing, created_at, decided_at) VALUES ('apr_keep', 'lark', 'me', 'read',
			'Read', 'cht_web', 'bot', 'Read it', '{}', 'h2', 'approved', 'session', 1, 2700, 2701)`,
		`INSERT INTO chat_queue (chat_id, text, created_at) VALUES ('cht_web', 'one more thing', 2800)`,
		`INSERT INTO artifacts (id, chat_id, bot_id, message_seq, revision, created_at, updated_at)
			VALUES ('art_page', 'cht_web', '` + main + `', 1, 1, 2550, 2550)`,

		// Ties: the same latest owner line; the one active last wins. A chat with no owner line loses.
		chatRow("cht_tie_old", "bot_tie", 0, 4000),
		chatRow("cht_tie_new", "bot_tie", 0, 4100),
		chatRow("cht_tie_empty", "bot_tie", 0, 9999),
		msgRow("cht_tie_old", "owner", "", "same time", "NULL", 3900) + `, NULL)`,
		msgRow("cht_tie_new", "owner", "", "same time", "NULL", 3900) + `, NULL)`,
		msgRow("cht_tie_empty", "coordinator", "", "hello?", "NULL", 9999) + `, NULL)`,

		// Only chats that are never a main chat.
		chatRow("cht_side_group", "bot_side", 1, 5000),
		chatRow("cht_side_routine", "bot_side", 0, 5000),
		`INSERT INTO routines (id, bot_id, name, kind, prompt, timeout_seconds, state, created_by, created_at,
			updated_at) VALUES ('rtn_side', 'bot_side', 'check', 'prompt', 'check', 60, 'on', 'owner', 1, 1)`,
		`INSERT INTO routine_runs (id, routine_id, started_by, state, started_at, ended_at, chat_id)
			VALUES ('rrn_side', 'rtn_side', 'schedule', 'succeeded', 5000, 5001, 'cht_side_routine')`,

		// A chat with no line at all is still the Bot's only candidate.
		chatRow("cht_quiet", "bot_quiet", 0, 6000),
	}
	for _, stmt := range stmts {
		_, err = db.ExecContext(ctx, stmt)
		require.NoError(t, err, stmt)
	}

	before := snapshot(ctx, t, db)

	require.NoError(t, Migrate(ctx, db))
	require.NoError(t, CheckSchema(ctx, db))

	for bot, want := range map[string]string{
		main: "cht_cli", "bot_tie": "cht_tie_new", "bot_none": "", "bot_side": "", "bot_quiet": "cht_quiet",
	} {
		var got sql.NullString
		require.NoError(t, db.QueryRowContext(ctx, `SELECT main_chat FROM bots WHERE id = ?`, bot).Scan(&got))
		assert.Equal(t, want, got.String, bot)
	}

	assert.Equal(t, before, snapshot(ctx, t, db), "no chat, message, Run, Approval, Routine or Channel changed")

	_, err = p.Down(ctx)
	require.NoError(t, err)
	assert.Equal(t, before, snapshot(ctx, t, db), "taken back, nothing else changed either")
}

// chatRow is an INSERT of a chat.
func chatRow(id, bot string, group int, updated int64) string {
	return fmt.Sprintf(`INSERT INTO chats (id, title, bot_id, channel_group, created_at, updated_at)
		VALUES ('%s', '%s', '%s', %d, 1, %d)`, id, id, bot, group, updated)
}

// msgRow is the start of an INSERT of a chat message; the caller adds its attachments and the closing parenthesis.
func msgRow(chat, role, kind, text, sender string, at int64) string {
	return fmt.Sprintf(`INSERT INTO chat_messages (chat_id, role, kind, text, sender_id, created_at, attachments)
		VALUES ('%s', '%s', '%s', '%s', %s, %d`, chat, role, kind, strings.ReplaceAll(text, "'", "''"), sender, at)
}

// snapshot is every row of the tables a chat's life spans, and the Bots without their main chat, as text.
func snapshot(ctx context.Context, t *testing.T, db *sql.DB) map[string][]string {
	t.Helper()

	out := map[string][]string{}

	for table, query := range map[string]string{
		"bots": `SELECT id, name, position, pinned, persona, memory FROM bots ORDER BY id`,
	} {
		out[table] = rows(ctx, t, db, query)
	}

	for _, table := range []string{
		"chats", "chat_messages", "chat_queue", "runs", "approvals", "routines", "routine_runs", "channels",
		"channel_chats", "artifacts",
	} {
		out[table] = rows(ctx, t, db, `SELECT * FROM `+table+` ORDER BY rowid`)
	}

	return out
}

func rows(ctx context.Context, t *testing.T, db *sql.DB, query string) []string {
	t.Helper()

	r, err := db.QueryContext(ctx, query)
	require.NoError(t, err, query)

	defer func() { _ = r.Close() }()

	cols, err := r.Columns()
	require.NoError(t, err)

	var out []string

	for r.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))

		for i := range vals {
			ptrs[i] = &vals[i]
		}

		require.NoError(t, r.Scan(ptrs...))
		out = append(out, fmt.Sprint(vals...))
	}

	require.NoError(t, r.Err())

	return out
}

// Scenario: the pointer of a Bot's main chat (issue #471). The first message to a Bot with none makes its main
// chat, and the next finds the same one; the web's New chat moves it to the chat made while the former main chat
// stays with its messages; a chat of Lark's kind never moves it; deleting the main chat clears it, and the next
// message makes another.
func TestABotsMainChatPointer(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	first, made, err := w.s.OpenMainChat(ctx, "")
	require.NoError(t, err)
	assert.True(t, made, "the main Bot had no main chat")
	assert.Equal(t, DefaultBotID, first.Bot)
	assert.Equal(t, first.ID, mainChatOf(t, w.s, DefaultBotID))

	same, made, err := w.s.OpenMainChat(ctx, DefaultBotID)
	require.NoError(t, err)
	assert.False(t, made)
	assert.Equal(t, first.ID, same.ID)

	_, err = w.s.AddChatMessage(ctx, first.ID, ChatOwner, "a long-running topic")
	require.NoError(t, err)

	lark, err := w.s.CreateChat(ctx, "", "a Lark chat")
	require.NoError(t, err)
	assert.Equal(t, first.ID, mainChatOf(t, w.s, DefaultBotID), "a chat of Lark's kind is no main chat")

	fresh, err := w.s.StartMainChat(ctx, "", "", "a new topic", ChatModel{})
	require.NoError(t, err)
	assert.Equal(t, fresh.ID, mainChatOf(t, w.s, DefaultBotID), "New chat moves the main chat")

	kept, err := w.s.ListChatMessages(ctx, first.ID, 0, 0)
	require.NoError(t, err)
	require.Len(t, kept, 1, "the former main chat stays as it was")

	_, err = w.s.DeleteChat(ctx, lark.ID)
	require.NoError(t, err)
	assert.Equal(t, fresh.ID, mainChatOf(t, w.s, DefaultBotID), "deleting another chat leaves it")

	_, err = w.s.DeleteChat(ctx, fresh.ID)
	require.NoError(t, err)
	assert.Empty(t, mainChatOf(t, w.s, DefaultBotID), "deleting the main chat clears it")

	again, made, err := w.s.OpenMainChat(ctx, "")
	require.NoError(t, err)
	assert.True(t, made, "the next message makes another")
	assert.NotEqual(t, first.ID, again.ID, "a hidden chat is not taken back as the main chat")

	_, _, err = w.s.OpenMainChat(ctx, "bot_none")
	require.ErrorIs(t, err, ErrNotFound)

	chats, err := w.s.ListChats(ctx, DefaultBotID, 0)
	require.NoError(t, err)
	require.Len(t, chats, 2)

	lines := map[string]string{}
	for _, c := range chats {
		lines[c.ID] = c.LastLine
	}

	assert.Equal(t, map[string]string{first.ID: "a long-running topic", again.ID: ""}, lines)
}

// Scenario: the web and the CLI write to a Bot with no main chat at the same moment (issue #471): every one of them
// gets the same chat, and the Bot has one chat.
func TestConcurrentFirstMessagesMakeOneMainChat(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	const writers = 8

	ids := make([]string, writers)

	var wg sync.WaitGroup

	for i := range writers {
		wg.Go(func() {
			c, _, err := w.s.OpenMainChat(ctx, "")
			assert.NoError(t, err)

			ids[i] = c.ID
		})
	}

	wg.Wait()

	for _, id := range ids {
		assert.Equal(t, ids[0], id)
	}

	chats, err := w.s.ListChats(ctx, DefaultBotID, 0)
	require.NoError(t, err)
	assert.Len(t, chats, 1)
}

func mainChatOf(t *testing.T, s *Service, bot string) string {
	t.Helper()

	b, err := s.GetBot(t.Context(), bot)
	require.NoError(t, err)

	return b.MainChat
}
