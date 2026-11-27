package inboxstore_test

import (
	"encoding/json"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/inboxstore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/storeerr"
)

// threadAsk is an item kai found in a Lark thread of the owner's account me, Hà My's ask to him there: open,
// asked_directly, given to him. The people and the work are made up.
func (w *world) threadAsk(t *testing.T) inboxstore.Todo {
	t.Helper()

	td, err := w.inbox.Create(t.Context(), bot(w.kai), inboxstore.NewTodo{
		Title: "Trả lời Hà My: tổng hợp các dashboard đã làm trong quý 3", State: inboxstore.Open, Assignee: me,
		WhyOpen: inboxstore.AskedDirectly,
		Source: inboxstore.Source{Kind: inboxstore.FromLark, ID: "oc_guild", Account: "me",
			Quote: "anh tổng hợp giúp em các dashboard anh làm trong quý 3 nhé",
			Ref:   &inboxstore.Link{Kind: inboxstore.LinkLarkMessage, Ref: "om_ask"}},
	})
	require.NoError(t, err)

	return td
}

// commentOf is a comment as evidence: its revision in the item's history.
func commentOf(e inboxstore.Event) []inboxstore.Link {
	return []inboxstore.Link{{Kind: inboxstore.LinkComment, Ref: strconv.Itoa(e.Revision)}}
}

// handedTo hands an item to kai and starts it there, as the owner and kai do.
func (w *world) handedTo(t *testing.T, item inboxstore.Todo) inboxstore.Todo {
	t.Helper()

	handed, err := w.inbox.Hand(t.Context(), by(owner, item), w.kai, "Em xem giúp anh")
	require.NoError(t, err)
	doing, err := w.inbox.Start(t.Context(), by(bot(w.kai), handed), inboxstore.Work{Kind: inboxstore.WorkChat,
		ID: w.chat})
	require.NoError(t, err)

	return doing
}

// threadMessages is what the server keeps of the Lark thread of threadAsk: Hà My's ask, and the owner's own reply.
func threadMessages() sentMessages {
	later := t0.Add(time.Minute)
	lark := inboxstore.FromLark + "/me/oc_guild/"

	return sentMessages{
		lark + "om_ask":   {Chat: "oc_guild", At: t0.Add(-time.Minute), Sender: "ou_hamy"},
		lark + "om_reply": {Chat: "oc_guild", At: later, FromMe: true},
	}
}

// Scenario: issue #1206, the owner's comment of 2026-11-27 (its people made up here). kai found Hà My's ask to the
// owner in a Lark thread and made it his promise when he took it (W1); the owner handed the item to kai, which
// started it; then he answered in the thread himself and commented on the item that he had. His reply is the item's
// work, whatever its why_open, so kai never closes it as its own: not on Hà My's message, nor on his reply or his
// comment, which show that he did it. Done by him, with his comment or his reply as evidence, it closes; a comment
// that is not his shows nothing.
func TestReviewOwnersPromisedReply(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newWorld(t)
	w.inbox.WithSent(threadMessages())

	for _, why := range []string{inboxstore.AskedDirectly, inboxstore.Promised} {
		item := w.threadAsk(t)
		if why == inboxstore.Promised {
			var err error
			item, err = w.inbox.Edit(ctx, by(bot(w.kai), item), inboxstore.Edit{WhyOpen: &why})
			require.NoError(t, err)
		}

		doing := w.handedTo(t, item)
		mine, err := w.inbox.Comment(ctx, bot(w.kai), item.ID, "Em đang đọc thread.")
		require.NoError(t, err)
		his, err := w.inbox.Comment(ctx, owner, item.ID, "Anh gửi danh sách dashboard cho Hà My rồi nhé.")
		require.NoError(t, err)
		doing.Revision = his.Revision

		itsOwn := inboxstore.Assignee{Kind: inboxstore.ABot, Name: w.kai}
		reply := inboxstore.Link{Kind: inboxstore.LinkLarkMessage, Ref: "om_reply", Account: "me", Chat: "oc_guild"}
		for name, evidence := range map[string][]inboxstore.Link{
			"the asker's message":           {{Kind: inboxstore.LinkLarkMessage, Ref: "om_ask"}},
			"the asker's message, its chat": {{Kind: inboxstore.LinkLarkMessage, Ref: "om_ask", Account: "me", Chat: "oc_guild"}},
			"his reply, as a message":       {reply},
			"his reply, as his":             {{Kind: inboxstore.LinkLarkSent, Ref: "om_reply", Account: "me", Chat: "oc_guild"}},
			"his comment":                   commentOf(last(his)),
		} {
			_, err := w.inbox.Done(ctx, inAttempt(bot(w.kai), doing), inboxstore.Closing{DoneBy: itsOwn,
				How: inboxstore.ClosedByBot, Evidence: evidence})
			require.ErrorIs(t, err, storeerr.ErrInvalid, "%s, %s: his answer is no work of the Bot's", why, name)
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
			require.ErrorIs(t, err, storeerr.ErrInvalid, "%s, %s is not his message", why, name)
		}

		w.unchanged(t, his)

		evidence := commentOf(last(his))
		if why == inboxstore.Promised {
			evidence = []inboxstore.Link{{Kind: inboxstore.LinkLarkSent, Ref: "om_reply", Account: "me", Chat: "oc_guild"}}
		}

		done, err := w.inbox.Done(ctx, inAttempt(bot(w.kai), doing), inboxstore.Closing{DoneBy: me,
			How: inboxstore.ClosedByBot, Reason: "Anh: 'Anh gửi danh sách dashboard cho Hà My rồi nhé.'",
			Evidence: evidence})
		require.NoError(t, err, why)
		assert.Equal(t, inboxstore.Done, done.State)
		require.NotNil(t, done.DoneBy)
		assert.Equal(t, me, *done.DoneBy)
		assert.Equal(t, evidence, done.Evidence)
	}
}

// Scenario: issue #1206, rule 2 of the owner's comment of 2026-11-27. A Bot closes as its own only what it did on the
// item in this attempt (botsWork; TestReviewDelegatedFix, TestReviewApprovalDoesNotIdentifyReplyAuthor and
// TestReviewOwnerReplyWithOwnerFileURL show what is). The owner's message alone (his comment, a message he sent
// himself, his message in the chat) shows that he did it, so it is recorded for him; a person's message, a message
// whose send the Bot did not record (one without its account and chat, his own after an approved send), a mail, a
// link or a meeting's line shows no work of the Bot's.
func TestABotClosesAsItsOwnOnlyWithEvidenceOfItsWork(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newWorld(t)
	w.inbox.WithSent(teamMessages())
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
		"a message without its chat": closing(inboxstore.Link{Kind: inboxstore.LinkWhatsAppMessage, Ref: "3EB0SENT"}),
		"his message from his account": closing(inboxstore.Link{Kind: inboxstore.LinkWhatsAppMessage, Ref: "mine",
			Account: "main", Chat: "team"}),
		"a mail":           closing(inboxstore.Link{Kind: inboxstore.LinkMail, Ref: "18c2f0a9b1d2e3f5"}),
		"a link":           closing(inboxstore.Link{Kind: inboxstore.LinkURL, Ref: "https://example.com/report.xlsx"}),
		"a meeting's line": closing(inboxstore.Link{Kind: inboxstore.LinkMeetingLine, Ref: "mtg_1/3"}),
	} {
		_, err := w.inbox.Done(ctx, inAttempt(bot(w.kai), doing), cl)
		require.ErrorIs(t, err, storeerr.ErrInvalid, "%s is no work of the Bot's", name)
	}

	w.unchanged(t, his)

	done, err := w.inbox.Done(ctx, inAttempt(bot(w.kai), doing),
		closing(w.ranFor(t, doing, "run_own", "https://github.com/o/r/pull/2")...))
	require.NoError(t, err)
	require.NotNil(t, done.DoneBy)
	assert.Equal(t, itsOwn, *done.DoneBy)
}

// Scenario: issue #1206, W1. kai listed Hà My's ask to the owner; he answered "dạ để em gửi tối nay". The same item
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
		Title: "Trả lời Hà My", State: inboxstore.Open, Assignee: me, Source: inboxstore.Source{Kind: inboxstore.ByHand},
	})
	require.NoError(t, err)

	return td
}
