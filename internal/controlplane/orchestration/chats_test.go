package orchestration

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

// Scenario: the owner writes to pi; the message waits until pi has it, and
// is delivered once. pi's reply is a record only.
func TestOwnerMessagesWaitForPi(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	chat, err := w.s.CreateChat(ctx, "", "")
	require.NoError(t, err)
	assert.Equal(t, "Chat", chat.Title)

	first, err := w.s.AddChatMessage(ctx, chat.ID, ChatOwner, "add a health page to demo")
	require.NoError(t, err)

	pending, err := w.s.ChatsWithUndelivered(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{chat.ID}, pending)

	require.NoError(t, w.s.MarkDelivered(ctx, chat.ID, first.Seq))

	_, err = w.s.AddChatMessage(ctx, chat.ID, ChatCoordinator, "On it.")
	require.NoError(t, err)
	_, err = w.s.AddChatMessage(ctx, chat.ID, ChatSystem, "ran: hypomnema runs create")
	require.NoError(t, err)

	undelivered, err := w.s.Undelivered(ctx, chat.ID)
	require.NoError(t, err)
	assert.Empty(t, undelivered, "replies and tool lines are never delivered")

	second, err := w.s.AddChatMessage(ctx, chat.ID, ChatOwner, "and a test")
	require.NoError(t, err)

	undelivered, err = w.s.Undelivered(ctx, chat.ID)
	require.NoError(t, err)
	require.Len(t, undelivered, 1)
	assert.Equal(t, second.Seq, undelivered[0].Seq)

	all, err := w.s.ListChatMessages(ctx, chat.ID, 0, 0)
	require.NoError(t, err)
	assert.Len(t, all, 4)
	assert.NotNil(t, all[0].DeliveredAt)

	_, err = w.s.AddChatMessage(ctx, chat.ID, ChatOwner, "   ")
	require.ErrorIs(t, err, ErrInvalid)

	_, err = w.s.AddChatMessage(ctx, "cht_none", ChatOwner, "hello")
	require.ErrorIs(t, err, ErrNotFound)
}

// Scenario: pi created a Run from a chat. Events that need a coordinator
// wake the chat, each once; others do not.
func TestRunEventsWakeTheirChatOnce(t *testing.T) {
	t.Parallel()

	w := newSchedWorld(t)
	ctx := t.Context()

	chat, err := w.s.CreateChat(ctx, "", "health page")
	require.NoError(t, err)

	run, err := w.s.CreateRun(ctx, NewRun{Objective: "health page", CreatedBy: "pi", ChatID: chat.ID})
	require.NoError(t, err)
	assert.Equal(t, chat.ID, run.ChatID)

	_, err = w.s.CreateRun(ctx, NewRun{Objective: "x", CreatedBy: "pi", ChatID: "cht_none"})
	require.ErrorIs(t, err, ErrNotFound)

	tasks, err := w.s.CreateTasks(ctx, run.ID, []NewTask{{Ref: "a", Spec: "a"}})
	require.NoError(t, err)

	n, err := w.s.WakeChats(ctx)
	require.NoError(t, err)
	assert.Zero(t, n, "creating a Task needs no coordinator")

	require.NoError(t, w.s.CancelTask(ctx, tasks[0].ID))

	n, err = w.s.WakeChats(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	n, err = w.s.WakeChats(ctx)
	require.NoError(t, err)
	assert.Zero(t, n, "an event wakes a chat once")

	err = w.s.FinishRun(ctx, run.ID)
	require.NoError(t, err)

	n, err = w.s.WakeChats(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	wakes, err := w.s.Undelivered(ctx, chat.ID)
	require.NoError(t, err)
	require.Len(t, wakes, 2)
	assert.Contains(t, wakes[0].Text, "Task "+tasks[0].ID+" of run "+run.ID+" is canceled")
	assert.Contains(t, wakes[0].Text, "hypomnema tasks show "+tasks[0].ID+" --json")
	assert.Contains(t, wakes[1].Text, "Run "+run.ID+" is finished")
	assert.Equal(t, ChatSystem, wakes[0].Role)
	assert.Equal(t, ChatWake, wakes[0].Kind)
	assert.NotZero(t, wakes[0].EventSeq)
}

// Scenario: the web reads a chat whose Run has a Task done, a Task failed
// and a question to the coordinator. Each wake-up comes with what its event
// says, the Run, the Task, the question and the status; the owner's message
// has none, and pi is given the rows without it.
func TestAWakeUpComesWithItsEvent(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	chat, err := w.s.CreateChat(ctx, "", "health page")
	require.NoError(t, err)

	w.run, err = w.s.CreateRun(ctx, NewRun{Objective: "health page", CreatedBy: "pi", ChatID: chat.ID})
	require.NoError(t, err)

	tasks := w.tasks(NewTask{Ref: "a", Spec: "a"}, NewTask{Ref: "b", Spec: "b"}, NewTask{Ref: "c", Spec: "c"})

	_, err = w.s.AddChatMessage(ctx, chat.ID, ChatOwner, "how is it going?")
	require.NoError(t, err)

	w.succeed(tasks[0].ID, "health page added")
	w.failForGood(tasks[1].ID)

	_, c := w.start(tasks[2].ID)
	question := w.ask(c, libraryChoice(AddressCoordinator))

	_, err = w.s.WakeChats(ctx)
	require.NoError(t, err)

	msgs, err := w.s.ListChatMessages(ctx, chat.ID, 0, 0)
	require.NoError(t, err)
	require.Len(t, msgs, 4)
	assert.Nil(t, msgs[0].Wake, "an owner message reports no event")

	want := []WakeEvent{
		{Event: EventTaskStatus, RunID: w.run.ID, TaskID: tasks[0].ID, Status: "succeeded"},
		{Event: EventTaskStatus, RunID: w.run.ID, TaskID: tasks[1].ID, Status: "failed",
			Reason: "3 failed attempts; the last: worker_error"},
		{Event: EventQuestionOpened, RunID: w.run.ID, TaskID: tasks[2].ID, QuestionID: question},
	}
	for i, m := range msgs[1:] {
		assert.Equal(t, ChatWake, m.Kind)
		require.NotNil(t, m.Wake, m.Text)
		assert.Equal(t, want[i], *m.Wake, m.Text)
	}

	undelivered, err := w.s.Undelivered(ctx, chat.ID)
	require.NoError(t, err)
	require.Len(t, undelivered, 4)
	assert.Nil(t, undelivered[1].Wake, "pi reads the wake-up's text, not its event")
}

// Scenario: a chat started without a title, as from the CLI, takes its first
// owner message's first line; a chat with a title keeps it.
func TestAChatIsNamedByItsFirstMessage(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	untitledChat, err := w.s.CreateChat(ctx, "", "")
	require.NoError(t, err)
	assert.Equal(t, "Chat", untitledChat.Title)

	_, err = w.s.AddChatMessage(ctx, untitledChat.ID, ChatOwner, "  add a health page\nwith a test")
	require.NoError(t, err)
	_, err = w.s.AddChatMessage(ctx, untitledChat.ID, ChatOwner, "and docs")
	require.NoError(t, err)

	got, err := w.s.GetChat(ctx, untitledChat.ID)
	require.NoError(t, err)
	assert.Equal(t, "add a health page", got.Title, "the first message names it, once")

	named, err := w.s.CreateChat(ctx, "", "release notes")
	require.NoError(t, err)
	_, err = w.s.AddChatMessage(ctx, named.ID, ChatOwner, "write them")
	require.NoError(t, err)

	got, err = w.s.GetChat(ctx, named.ID)
	require.NoError(t, err)
	assert.Equal(t, "release notes", got.Title)
}

// Scenario: the owner answers an Approval the coordinator asked for in a
// chat. The coordinator is told with an approval row, which is not a failure.
func TestAnAnsweredApprovalIsAnApprovalRow(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	chat, err := w.s.CreateChat(ctx, "", "")
	require.NoError(t, err)

	told, err := w.s.TellAgentOfApproval(ctx, Approval{
		ID: "apr_1", ChatID: chat.ID, Title: "Send email", Status: ApprovalApproved, Scope: ScopeOnce,
	})
	require.NoError(t, err)
	require.True(t, told)

	msgs, err := w.s.Undelivered(ctx, chat.ID)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.Equal(t, [2]string{"system", "approval"}, [2]string{string(msgs[0].Role), string(msgs[0].Kind)})
	assert.Contains(t, msgs[0].Text, "The owner approved \"Send email\"")
}

// Scenario: a database from before system rows had a kind. After the
// migration a wake-up is a wake row, an Approval's notice an approval row, a
// failed call a failed row, and other rows have none.
func TestMigrationGivesSystemRowsTheirKind(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db, err := sqlite.Open(ctx, sqlite.Config{Path: filepath.Join(t.TempDir(), "hypomnema.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	p, err := provider(db)
	require.NoError(t, err)
	_, err = p.UpTo(ctx, 53)
	require.NoError(t, err)

	_, err = db.ExecContext(ctx, `INSERT INTO chats (id, title, bot, created_at, updated_at)
		VALUES ('cht_1', 'Chat', 'coordinator', 1, 1)`)
	require.NoError(t, err)

	_, err = db.ExecContext(ctx, `INSERT INTO chat_messages (chat_id, role, text, event_seq, to_coordinator, created_at)
		VALUES ('cht_1', 'owner', 'pi''s model call failed, says the owner', NULL, 0, 1),
		       ('cht_1', 'system', 'Run r1 is finished: hypomnema runs show r1 --json', 7, 0, 2),
		       ('cht_1', 'system', 'The owner approved "X" (apr_1).', NULL, 1, 3),
		       ('cht_1', 'system', 'pi''s model call failed: overloaded', NULL, 0, 4),
		       ('cht_1', 'system', 'pi ran bash: ls', NULL, 0, 5),
		       ('cht_1', 'coordinator', 'Done.', NULL, 0, 6)`)
	require.NoError(t, err)

	require.NoError(t, Migrate(ctx, db))
	require.NoError(t, CheckSchema(ctx, db))

	s := New(db, &fakeClock{now: t0}, func(prefix string) string { return prefix + "_new" })

	msgs, err := s.ListChatMessages(ctx, "cht_1", 0, 0)
	require.NoError(t, err)

	kinds := make([]ChatKind, 0, len(msgs))
	for _, m := range msgs {
		kinds = append(kinds, m.Kind)
	}

	assert.Equal(t, []ChatKind{"", ChatWake, ChatApproval, ChatFailed, "", ""}, kinds)

	// The way back drops the kind and keeps the rows.
	_, err = p.DownTo(ctx, 53)
	require.NoError(t, err)

	var n int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM chat_messages`).Scan(&n))
	assert.Equal(t, 6, n)
}
