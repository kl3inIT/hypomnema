package inboxstore_test

import (
	"encoding/json"
	"errors"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/inboxstore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/storeerr"
)

// threadAsk is an item kai found in a Lark thread of the owner's account me, LinhHTT's ask to him there: open,
// asked_directly, given to him.
func (w *world) threadAsk(t *testing.T) inboxstore.Todo {
	t.Helper()

	td, err := w.inbox.Create(t.Context(), bot(w.kai), inboxstore.NewTodo{
		Title: "Trả lời thread LinhHTT: liệt kê pipeline đã làm từ 1/10", State: inboxstore.Open, Assignee: me,
		WhyOpen: inboxstore.AskedDirectly,
		Source: inboxstore.Source{Kind: inboxstore.FromLark, ID: "oc_ai", Account: "me",
			Quote: "anh liệt kê giúp em các pipeline anh đã làm từ 1/10 nhé",
			Ref:   &inboxstore.Link{Kind: inboxstore.LinkLarkMessage, Ref: "om_root"}},
	})
	require.NoError(t, err)

	return td
}

// commentOf is a comment as evidence: its revision in the item's history.
func commentOf(e inboxstore.Event) []inboxstore.Link {
	return []inboxstore.Link{{Kind: inboxstore.LinkComment, Ref: strconv.Itoa(e.Revision)}}
}

// Scenario: issue #1206, the owner's comment of 2026-11-27. kai found LinhHTT's ask to the owner in a Lark thread;
// the owner handed the item to kai, which started it; then he commented on it that he had sent the list himself. His
// reply is the item's work, so kai never closes it as its own: not on LinhHTT's message, nor on his comment, which
// shows he did it. Done by him, with his comment as evidence, it closes; a comment that is not his shows nothing.
func TestAnAskAnsweredByTheOwnerIsDoneByHimNotTheBot(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newWorld(t)

	item := w.threadAsk(t)
	handed, err := w.inbox.Hand(ctx, by(owner, item), w.kai, "Em xem giúp anh")
	require.NoError(t, err)
	doing, err := w.inbox.Start(ctx, by(bot(w.kai), handed), inboxstore.Work{Kind: inboxstore.WorkChat, ID: w.chat})
	require.NoError(t, err)

	mine, err := w.inbox.Comment(ctx, bot(w.kai), item.ID, "Em đang đọc thread.")
	require.NoError(t, err)
	his, err := w.inbox.Comment(ctx, owner, item.ID, "Anh gửi danh sách pipeline cho anh Duy rồi nhé.")
	require.NoError(t, err)
	doing.Revision = his.Revision

	itsOwn := inboxstore.Assignee{Kind: inboxstore.ABot, Name: w.kai}
	for name, cl := range map[string]inboxstore.Closing{
		"on the asker's message": {DoneBy: itsOwn, How: inboxstore.ClosedByBot,
			Evidence: []inboxstore.Link{{Kind: inboxstore.LinkLarkMessage, Ref: "om_root"}}},
		"on his comment":      {DoneBy: itsOwn, How: inboxstore.ClosedByBot, Evidence: commentOf(last(his))},
		"with a pull request": {DoneBy: itsOwn, How: inboxstore.ClosedByBot, Evidence: prLink},
	} {
		_, err := w.inbox.Done(ctx, inAttempt(bot(w.kai), doing), cl)
		require.ErrorIs(t, err, storeerr.ErrForbidden, "%s: someone asked the owner; his answer closes it", name)
	}

	w.unchanged(t, his)

	for name, evidence := range map[string][]inboxstore.Link{
		"kai's comment":     commentOf(last(mine)),
		"no such comment":   {{Kind: inboxstore.LinkComment, Ref: "999"}},
		"not a revision":    {{Kind: inboxstore.LinkComment, Ref: "the last one"}},
		"the item's making": commentOf(item.Events[0]),
	} {
		_, err := w.inbox.Done(ctx, inAttempt(bot(w.kai), doing), inboxstore.Closing{DoneBy: me,
			How: inboxstore.ClosedByBot, Evidence: evidence})
		require.ErrorIs(t, err, storeerr.ErrInvalid, "%s is not his message", name)
	}

	w.unchanged(t, his)

	done, err := w.inbox.Done(ctx, inAttempt(bot(w.kai), doing), inboxstore.Closing{DoneBy: me,
		How: inboxstore.ClosedByBot, Reason: "Anh Đạt: 'Anh gửi danh sách pipeline cho anh Duy rồi nhé.'",
		Evidence: commentOf(last(his))})
	require.NoError(t, err)
	assert.Equal(t, inboxstore.Done, done.State)
	require.NotNil(t, done.DoneBy)
	assert.Equal(t, me, *done.DoneBy)
	assert.Equal(t, commentOf(last(his)), done.Evidence)
}

// Scenario: issue #1206, rule 2 of the owner's comment of 2026-11-27. A Bot closes as its own only what it did, with
// evidence of its work: a Run, a pull request, a link to a file, a message it sent. The owner's message alone (his
// comment, a message he sent himself, his message in the chat) shows that he did it, so it is recorded for him; a
// person's message or a meeting's line shows no work of the Bot's.
func TestABotClosesAsItsOwnOnlyWithEvidenceOfItsWork(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newWorld(t)
	itsOwn := inboxstore.Assignee{Kind: inboxstore.ABot, Name: w.kai}
	closing := func(evidence ...inboxstore.Link) inboxstore.Closing {
		return inboxstore.Closing{DoneBy: itsOwn, How: inboxstore.ClosedByBot, Evidence: evidence}
	}

	doing := w.doing(t)
	his, err := w.inbox.Comment(ctx, owner, doing.ID, "Xong rồi, anh tự gửi.")
	require.NoError(t, err)
	doing.Revision = his.Revision

	for name, cl := range map[string]inboxstore.Closing{
		"his comment": closing(commentOf(last(his))...),
		"his message sent": closing(inboxstore.Link{Kind: inboxstore.LinkLarkSent, Ref: "om_his", Account: "me",
			Chat: "oc_ai"}),
		"his message in the chat": closing(w.said...),
		"a person's message": closing(inboxstore.Link{Kind: inboxstore.LinkWhatsAppMessage, Ref: "fixed",
			Account: "main", Chat: "team"}),
		"a meeting's line": closing(inboxstore.Link{Kind: inboxstore.LinkMeetingLine, Ref: "mtg_1/3"}),
	} {
		_, err := w.inbox.Done(ctx, inAttempt(bot(w.kai), doing), cl)
		require.ErrorIs(t, err, storeerr.ErrInvalid, "%s is no work of the Bot's", name)
	}

	w.unchanged(t, his)

	for _, work := range []inboxstore.Link{
		{Kind: inboxstore.LinkRun, Ref: "run_1"},
		{Kind: inboxstore.LinkURL, Ref: "https://example.com/report.xlsx"},
		{Kind: inboxstore.LinkWhatsAppMessage, Ref: "3EB0SENT"},
	} {
		item := w.doing(t)
		done, err := w.inbox.Done(ctx, inAttempt(bot(w.kai), item), closing(work))
		require.NoError(t, err, work.Kind)
		assert.Equal(t, inboxstore.Done, done.State)
		require.NotNil(t, done.DoneBy)
		assert.Equal(t, itsOwn, *done.DoneBy)
	}

	_, err = w.inbox.Done(ctx, inAttempt(bot(w.kai), doing), closing(prLink...))
	require.NoError(t, err)
}

// Scenario: issue #1206, W1. kai listed LinhHTT's ask to the owner; he answered "dạ để em gửi tối nay". The same item
// is now his promise: kai sets why_open promised, which its history records as an edit. Only that change, on an item
// kai added that someone asked him and that is still his: never another reason, nor an item waiting on a person,
// one he wrote himself, or another Bot's.
func TestABotMakesTheAskItListedHisPromise(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newWorld(t)
	promised := inboxstore.Promised

	item := w.threadAsk(t)
	got, err := w.inbox.Edit(ctx, by(bot(w.kai), item), inboxstore.Edit{WhyOpen: &promised})
	require.NoError(t, err)
	assert.Equal(t, inboxstore.Promised, got.WhyOpen)
	assert.Equal(t, inboxstore.Open, got.State)
	assert.Equal(t, me, got.Assignee)

	e := last(got)
	assert.Equal(t, inboxstore.EventEdited, e.Kind)

	var after map[string]any
	require.NoError(t, json.Unmarshal(e.After, &after))
	assert.Equal(t, map[string]any{"why_open": inboxstore.Promised}, after)

	again, err := w.inbox.Edit(ctx, by(bot(w.kai), got), inboxstore.Edit{WhyOpen: &promised})
	require.NoError(t, err, "already his promise: nothing changes")
	assert.Equal(t, got.Revision, again.Revision)

	waiting, asked := inboxstore.Waiting, inboxstore.AskedDirectly
	waitsOnViet, err := w.inbox.WaitOn(ctx, by(bot(w.kai), w.zaloAsk(t, w.kai)), took("Việt"))
	require.NoError(t, err)

	for name, refused := range map[string]struct {
		item inboxstore.Todo
		why  *string
	}{
		"waiting":                {w.threadAsk(t), &waiting},
		"asked again":            {got, &asked},
		"an item waiting on him": {waitsOnViet, &promised},
		"a proposal":             {w.teamAsk(t, inboxstore.Proposed), &promised},
		"one the owner wrote":    {w.fromOwner(t), &promised},
		"another Bot's":          {w.zaloAsk(t, w.lan), &promised},
	} {
		_, err := w.inbox.Edit(ctx, by(bot(w.kai), refused.item), inboxstore.Edit{WhyOpen: refused.why})
		require.Error(t, err, name)
		assert.True(t, errors.Is(err, storeerr.ErrInvalid) || errors.Is(err, storeerr.ErrForbidden), "%s: %v", name,
			err)
	}
}

// fromOwner is an ask the owner wrote by hand, given to him.
func (w *world) fromOwner(t *testing.T) inboxstore.Todo {
	t.Helper()

	td, err := w.inbox.Create(t.Context(), owner, inboxstore.NewTodo{
		Title: "Trả lời Linh", State: inboxstore.Open, Assignee: me, Source: inboxstore.Source{Kind: inboxstore.ByHand},
	})
	require.NoError(t, err)

	return td
}
