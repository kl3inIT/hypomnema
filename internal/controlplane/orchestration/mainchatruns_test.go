package orchestration

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
)

// Scenario: issue #1169. A Run is open on the main Bot's main chat, with a wake-up its pi was not given yet, and
// another on a side chat. The owner starts a new main chat: the main chat's Run moves to it with its coordinator
// token, the new chat is told, the former one says where it went and its pi is not given the old wake-up; the Run's
// later events wake the new main chat only, and the side chat's Run stays and wakes its own chat. Setting the former
// chat as the main chat again takes the Run back.
func TestANewMainChatTakesTheFormerMainChatsOpenRuns(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	_, _, err := w.s.AddToken(ctx, "pi", bearertoken.Coordinator)
	require.NoError(t, err)

	old, _, err := w.s.OpenMainChat(ctx, "", "", "", ChatModel{})
	require.NoError(t, err)
	side, err := w.s.CreateChat(ctx, "", "a side chat")
	require.NoError(t, err)

	moving, err := w.s.CreateRun(ctx, NewRun{Objective: "work of the main chat", CreatedBy: "pi", ChatID: old.ID})
	require.NoError(t, err)
	staying, err := w.s.CreateRun(ctx, NewRun{Objective: "work of the side chat", CreatedBy: "pi", ChatID: side.ID})
	require.NoError(t, err)

	movingTasks, err := w.s.CreateTasks(ctx, moving.ID, []NewTask{{Ref: "a", Spec: "a"}, {Ref: "b", Spec: "b"}})
	require.NoError(t, err)
	stayingTasks, err := w.s.CreateTasks(ctx, staying.ID, []NewTask{{Ref: "c", Spec: "c"}})
	require.NoError(t, err)

	require.NoError(t, w.s.CancelTask(ctx, movingTasks[0].ID))
	_, err = w.s.WakeChats(ctx)
	require.NoError(t, err)
	require.Len(t, w.undelivered(old.ID), 1, "the former main chat's pi has a wake-up waiting")

	fresh, err := w.s.StartMainChat(ctx, "", "", "a new topic", ChatModel{})
	require.NoError(t, err)

	moved, err := w.s.GetRun(ctx, moving.ID)
	require.NoError(t, err)
	assert.Equal(t, fresh.ID, moved.ChatID)
	assert.Equal(t, moving.CoordinatorTokenID, moved.CoordinatorTokenID)

	stayed, err := w.s.GetRun(ctx, staying.ID)
	require.NoError(t, err)
	assert.Equal(t, side.ID, stayed.ChatID, "a Run of a side chat is not moved")

	assert.Empty(t, w.undelivered(old.ID), "the former main chat's pi is not woken for a Run it no longer has")
	assert.Contains(t, w.lastLine(old.ID), "Run "+moving.ID+" moved to chat "+fresh.ID)

	notice := w.undelivered(fresh.ID)
	require.Len(t, notice, 1)
	assert.Contains(t, notice[0].Text, "Run "+moving.ID+" moved here from chat "+old.ID)
	assert.Contains(t, notice[0].Text, "hypomnema runs show")

	require.NoError(t, w.s.CancelTask(ctx, movingTasks[1].ID))
	require.NoError(t, w.s.CancelTask(ctx, stayingTasks[0].ID))
	_, err = w.s.WakeChats(ctx)
	require.NoError(t, err)

	assert.Empty(t, w.undelivered(old.ID), "the Run's later events do not wake the former main chat")

	woken := w.undelivered(fresh.ID)
	require.Len(t, woken, 2)
	assert.Contains(t, woken[1].Text, movingTasks[1].ID, "the Run's later event wakes the new main chat")
	assert.NotContains(t, woken[1].Text, movingTasks[0].ID, "an event from before the move is not told again")

	sideWakes := w.undelivered(side.ID)
	require.Len(t, sideWakes, 1)
	assert.Contains(t, sideWakes[0].Text, stayingTasks[0].ID)

	_, err = w.s.SetMainChat(ctx, DefaultBotID, old.ID)
	require.NoError(t, err)

	back, err := w.s.GetRun(ctx, moving.ID)
	require.NoError(t, err)
	assert.Equal(t, old.ID, back.ChatID, "setting the main chat moves the Runs too")
	left := w.undelivered(fresh.ID)
	require.Len(t, left, 1, "the chat left is not given the Run's wake-ups")
	assert.Zero(t, left[0].EventSeq, "only the line that told it of the Run is left")
	assert.Contains(t, w.lastLine(old.ID), "Run "+moving.ID+" moved here from chat "+fresh.ID)
}

func (w *world) undelivered(chatID string) []ChatMessage {
	w.t.Helper()

	msgs, err := w.s.Undelivered(w.t.Context(), chatID)
	require.NoError(w.t, err)

	return msgs
}

func (w *world) lastLine(chatID string) string {
	w.t.Helper()

	msgs, err := w.s.ListChatMessages(w.t.Context(), chatID, 0, 0)
	require.NoError(w.t, err)
	require.NotEmpty(w.t, msgs)

	return msgs[len(msgs)-1].Text
}

// Scenario: issue #1169, the Runs left open on a Bot's former main chat before the fix. The migration moves them to
// the main chat, lets go of the former chat's wake-ups of theirs not given yet and tells both chats; a finished Run,
// a Run of the main chat, of an Inbox item's chat, of a Routine's chat, of a Lark group's chat and of a Bot with no
// main chat stay where they are, as does every other wake-up.
func TestMigrationMovesTheOpenRunsOfFormerMainChats(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	own := migrationVersion(t, "_former_main_chat_runs.sql")
	db, p := databaseAt(t, own-1)

	main := DefaultBotID
	stmts := []string{
		`INSERT INTO bots (id, name, created_at, updated_at) VALUES ('bot_none', 'none', 1, 1)`,
		chatRow("cht_main", main, 0, 10),
		chatRow("cht_old", main, 0, 10),
		chatRow("cht_inbox", main, 0, 10),
		chatRow("cht_routine", main, 0, 10),
		chatRow("cht_group", main, 1, 10),
		chatRow("cht_none", "bot_none", 0, 10),
		`UPDATE bots SET main_chat = 'cht_main' WHERE id = '` + main + `'`,
		`INSERT INTO todos (id, title, state, source_kind, created_by_kind, chat_id, created_at, updated_at)
			VALUES ('tdo_a', 'a', 'open', 'owner', 'owner', 'cht_inbox', 1, 1)`,
		`INSERT INTO chat_messages (chat_id, role, kind, text, to_coordinator, created_at)
			VALUES ('cht_routine', 'system', 'wake', 'Routine daily: the prompt', 1, 5)`,
	}

	runs := map[string]string{
		"run_old": "cht_old", "run_old2": "cht_old", "run_done": "cht_old", "run_main": "cht_main",
		"run_inbox": "cht_inbox", "run_routine": "cht_routine", "run_group": "cht_group", "run_none": "cht_none",
	}
	for id, chat := range runs {
		status := "open"
		if id == "run_done" {
			status = "finished"
		}

		stmts = append(stmts,
			`INSERT INTO runs (id, objective, status, created_by, chat_id, created_at, updated_at)
				VALUES ('`+id+`', 'o', '`+status+`', 'pi', '`+chat+`', 1, 1)`,
			`INSERT INTO events (kind, run_id, payload, created_at) VALUES ('escalation', '`+id+`', '{}', 2)`,
			`INSERT INTO chat_messages (chat_id, role, kind, text, event_seq, created_at)
				VALUES ('`+chat+`', 'system', 'wake', 'woken for `+id+`',
					(SELECT max(seq) FROM events WHERE run_id = '`+id+`'), 3)`,
		)
	}

	for _, stmt := range stmts {
		_, err := db.ExecContext(ctx, stmt)
		require.NoError(t, err, stmt)
	}

	_, err := p.UpTo(ctx, own)
	require.NoError(t, err)

	assert.Equal(t, []string{
		"run_done cht_old", "run_group cht_group", "run_inbox cht_inbox", "run_main cht_main", "run_none cht_none",
		"run_old cht_main", "run_old2 cht_main", "run_routine cht_routine",
	}, rows(ctx, t, db, `SELECT id || ' ' || chat_id FROM runs ORDER BY id`))

	assert.Equal(t, []string{"1", "1"}, rows(ctx, t, db,
		`SELECT wake_after = (SELECT max(seq) FROM events WHERE run_id = runs.id) FROM runs
		WHERE id IN ('run_old', 'run_old2')`), "a moved Run's events from before the move do not wake again")

	assert.Equal(t, []string{"woken for run_done", "woken for run_group", "woken for run_inbox",
		"woken for run_main", "woken for run_none", "woken for run_routine"}, rows(ctx, t, db,
		`SELECT text FROM chat_messages WHERE event_seq IS NOT NULL AND delivered_at IS NULL ORDER BY text`),
		"only the moved Runs' wake-ups in the former chat are let go of")

	assert.Equal(t, []string{
		"cht_main 1 Runs run_old, run_old2 moved here from chat cht_old, the Bot's main chat before; this chat " +
			"coordinates them now and their events wake it. Read each one's current Tasks, unanswered questions and " +
			"review evidence before acting: hypomnema runs show <run> --json",
		"cht_old 0 Runs run_old, run_old2 moved to chat cht_main, the Bot's main chat now; their events wake that chat.",
	}, rows(ctx, t, db, `SELECT chat_id || ' ' || to_coordinator || ' ' || text FROM chat_messages
		WHERE event_seq IS NULL AND text LIKE 'Run%' ORDER BY chat_id`))
}
