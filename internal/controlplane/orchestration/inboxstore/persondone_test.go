package inboxstore_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/inboxstore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/storeerr"
)

// The people of the team's WhatsApp group, by their ids there: Lan asks, Đức Anh and Việt are the owner's team.
const (
	lanID  = "84911111111@s.whatsapp.net"
	ducID  = "84933333333@s.whatsapp.net"
	vietID = "84944444444@s.whatsapp.net"
)

// teamAsk is an item kai found in the team's WhatsApp group of the owner's account main, from Lan's message
// 'asked': open, asked_directly, "Sửa key Luma"; or proposed, a group ask nobody took yet.
func (w *world) teamAsk(t *testing.T, state string) inboxstore.Todo {
	t.Helper()

	n := inboxstore.NewTodo{
		Title: "Sửa key Luma", State: state, Assignee: me,
		Source: inboxstore.Source{Kind: inboxstore.FromWhatsApp, ID: "team", Account: "main",
			Quote: "Key Luma lỗi rồi", Ref: &inboxstore.Link{Kind: inboxstore.LinkWhatsAppMessage, Ref: "asked"}},
	}
	if state == inboxstore.Open {
		n.WhyOpen = inboxstore.AskedDirectly
	}

	td, err := w.inbox.Create(t.Context(), bot(w.kai), n)
	require.NoError(t, err)

	return td
}

// teamMessages is what the server keeps of the owner's WhatsApp account main, and reads of its chats: Lan asked
// in the team's group; later, Đức Anh says it is fixed there, in the ops group Lan is in, in the group without her,
// in a group whose members cannot be read, and in his direct chat; Việt takes the group ask; the owner's own
// message, and Đức Anh's from before the item. The same message of another account and of Zalo.
func teamMessages() withMembers {
	later, earlier := t0.Add(time.Minute), t0.Add(-time.Minute)
	from := func(chat, sender string, at time.Time) inboxstore.SentMessage {
		return inboxstore.SentMessage{Chat: chat, At: at, Sender: sender}
	}
	wa := inboxstore.FromWhatsApp + "/main/"

	return withMembers{
		sentMessages: sentMessages{
			wa + "team/asked":       from("team", lanID, earlier),
			wa + "team/took":        from("team", ducID, later),
			wa + "team/fixed":       from("team", ducID, later),
			wa + "team/claim":       from("team", vietID, later),
			wa + "team/before":      from("team", ducID, earlier),
			wa + "team/mine":        {Chat: "team", At: later, FromMe: true, Sender: "84900000000@s.whatsapp.net"},
			wa + "ops/fixed":        from("ops", ducID, later),
			wa + "others/fixed":     from("others", ducID, later),
			wa + "others/viet":      from("others", vietID, later),
			wa + "unreadable/fixed": from("unreadable", ducID, later),
			wa + "duc/fixed":        from("duc", ducID, later),
			inboxstore.FromWhatsApp + "/work/team/fixed": from("team", ducID, later),
			inboxstore.FromZalo + "/main/team/fixed":     from("team", ducID, later),
		},
		askers: map[string][]string{wa + "team/asked": {lanID}},
		members: map[string]inboxstore.ChatMembers{
			inboxstore.FromWhatsApp + "/main/ops":    {Name: "Nhóm vận hành", IDs: []string{lanID, ducID}},
			inboxstore.FromWhatsApp + "/main/others": {Name: "Nhóm khác", IDs: []string{ducID, vietID}},
			inboxstore.FromWhatsApp + "/main/duc":    {Name: "Đức Anh", IDs: []string{ducID}},
		},
	}
}

// theirs is a person's WhatsApp message of account main, given as evidence.
func theirs(chat, id string) []inboxstore.Link {
	return []inboxstore.Link{{Kind: inboxstore.LinkWhatsAppMessage, Ref: id, Account: "main", Chat: chat}}
}

// byDuc is kai recording that Đức Anh did it, with his words and that evidence.
func byDuc(evidence []inboxstore.Link) inboxstore.Closing {
	return inboxstore.Closing{DoneBy: inboxstore.Assignee{Kind: inboxstore.Person, Name: "Đức Anh"},
		How: inboxstore.ClosedByBot, Reason: "Đức Anh: 'It's fixed and live on production'", Evidence: evidence}
}

// answeredIn is the chat the closing of an item says it was done in.
func answeredIn(t *testing.T, w *world, id string) any {
	t.Helper()

	got, err := w.inbox.Get(t.Context(), id)
	require.NoError(t, err)

	var after map[string]any
	require.NoError(t, json.Unmarshal(last(got).After, &after))

	return after["answered_in"]
}

// Scenario: issue #1204 (S1 of #1164). Lan asks in the team's WhatsApp group; Đức Anh writes that it is fixed. kai
// records the item done by Đức Anh, with his message as evidence and his words as the reason: in the item's own
// group, or in another group of the account where Lan is a member, which the closing names. Without a way to read
// the account's messages, nothing is closed so.
func TestABotRecordsWhatAPersonDidWithTheirMessage(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newWorld(t)

	item := w.teamAsk(t, inboxstore.Open)
	_, err := w.inbox.Done(ctx, by(bot(w.kai), item), byDuc(theirs("team", "fixed")))
	require.ErrorIs(t, err, storeerr.ErrUnavailable, "no store of the account's messages")

	w.inbox.WithSent(teamMessages())

	done, err := w.inbox.Done(ctx, by(bot(w.kai), item), byDuc(theirs("team", "fixed")))
	require.NoError(t, err)
	assert.Equal(t, inboxstore.Done, done.State)
	assert.Equal(t, &inboxstore.Assignee{Kind: inboxstore.Person, Name: "Đức Anh"}, done.DoneBy)
	assert.Equal(t, &inboxstore.Actor{Kind: inboxstore.Bot, ID: w.kai}, done.RecordedBy, "kai recorded it")
	assert.Equal(t, theirs("team", "fixed"), done.Evidence)
	assert.Equal(t, "Đức Anh: 'It's fixed and live on production'", done.CloseReason)
	assert.Nil(t, answeredIn(t, w, done.ID), "done in the item's own chat")

	elsewhere := w.teamAsk(t, inboxstore.Open)
	done, err = w.inbox.Done(ctx, by(bot(w.kai), elsewhere), byDuc(theirs("ops", "fixed")))
	require.NoError(t, err)
	assert.Equal(t, inboxstore.Done, done.State)
	assert.Equal(t, "Nhóm vận hành", answeredIn(t, w, done.ID), "the closing says the group it was done in")
}

// Scenario: issue #1204. The owner waits on Đức Anh, who took Lan's ask in the team's group ("em nhận"); he later
// says it is done in his direct chat with the owner, where Lan is not. kai records it done by him: on an item
// waiting on a person, their message anywhere on the account counts, by its sender. Việt's message there does not.
func TestAWaitedOnPersonsMessageAnywhereOnTheAccountClosesTheItem(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newWorld(t)
	w.inbox.WithSent(teamMessages())

	waiting, err := w.inbox.WaitOn(ctx, by(bot(w.kai), w.teamAsk(t, inboxstore.Open)), inboxstore.WaitOn{
		WaitingOn: inboxstore.WaitingOn{By: "Đức Anh", Quote: "em nhận",
			Ref: inboxstore.Link{Kind: inboxstore.LinkWhatsAppMessage, Ref: "took"}},
	})
	require.NoError(t, err)

	_, err = w.inbox.Done(ctx, by(bot(w.kai), waiting), byDuc(theirs("others", "viet")))
	require.ErrorIs(t, err, storeerr.ErrInvalid, "Việt is not whom it waits on, and Lan is not in that group")
	w.unchanged(t, waiting)

	done, err := w.inbox.Done(ctx, by(bot(w.kai), waiting), byDuc(theirs("duc", "fixed")))
	require.NoError(t, err)
	assert.Equal(t, inboxstore.Done, done.State)
	assert.Equal(t, "Đức Anh", answeredIn(t, w, done.ID))
}

// Scenario: issue #1204, triage rule 3. Lan asks the team; kai proposes it to the owner, nobody having taken it.
// Việt then takes it in the group: kai drops its proposal with his message, without the owner. A proposal kai did
// not add, or an open item, is not dropped so.
func TestABotDropsItsProposalATeammateTook(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newWorld(t)
	w.inbox.WithSent(teamMessages())

	claim := theirs("team", "claim")
	reason := "Việt nhận: 'để em làm'"

	open := w.teamAsk(t, inboxstore.Open)
	_, err := w.inbox.Drop(ctx, by(bot(w.kai), open), reason, claim)
	require.ErrorIs(t, err, storeerr.ErrForbidden, "only a proposal is dropped on a person's message")

	lans, err := w.inbox.Create(ctx, bot(w.lan), inboxstore.NewTodo{
		Title: "Sửa key Luma", State: inboxstore.Proposed, Assignee: me, Source: open.Source,
	})
	require.NoError(t, err)
	_, err = w.inbox.Drop(ctx, by(bot(w.kai), lans), reason, claim)
	require.ErrorIs(t, err, storeerr.ErrForbidden, "another Bot's proposal")
	w.unchanged(t, lans)

	proposal := w.teamAsk(t, inboxstore.Proposed)
	_, err = w.inbox.Drop(ctx, by(bot(w.kai), proposal), reason, theirs("team", "mine"))
	require.ErrorIs(t, err, storeerr.ErrInvalid, "the owner's own message is not a teammate's claim")
	w.unchanged(t, proposal)

	dropped, err := w.inbox.Drop(ctx, by(bot(w.kai), proposal), reason, claim)
	require.NoError(t, err)
	assert.Equal(t, inboxstore.Dropped, dropped.State)
	assert.Equal(t, claim, dropped.Evidence)
	assert.Equal(t, reason, dropped.CloseReason)
}

// Scenario: issue #1204. What is not a person's message doing the item is refused and writes nothing: the owner's
// own message given as someone else's, a message older than the item, one in a group Lan is not in or whose members
// cannot be read, one of another account or service, one without its account and chat, without the words in the
// reason; and any message on an item the owner added himself.
func TestABotRecordsAPersonsWorkOnlyWithTheirMessage(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newWorld(t)
	w.inbox.WithSent(teamMessages())

	item := w.teamAsk(t, inboxstore.Open)

	noReason := byDuc(theirs("team", "fixed"))
	noReason.Reason = ""

	for name, cl := range map[string]inboxstore.Closing{
		"the owner's own message":          byDuc(theirs("team", "mine")),
		"a message older than the item":    byDuc(theirs("team", "before")),
		"a group Lan is not in":            byDuc(theirs("others", "fixed")),
		"a group whose members are unread": byDuc(theirs("unreadable", "fixed")),
		"another account":                  byDuc([]inboxstore.Link{{Kind: inboxstore.LinkWhatsAppMessage, Ref: "fixed", Account: "work", Chat: "team"}}),
		"another service":                  byDuc([]inboxstore.Link{{Kind: inboxstore.LinkZaloMessage, Ref: "fixed", Account: "main", Chat: "team"}}),
		"a message without its chat":       byDuc([]inboxstore.Link{{Kind: inboxstore.LinkWhatsAppMessage, Ref: "fixed"}}),
		"a message the server lacks":       byDuc(theirs("team", "gone")),
		"no words in the reason":           noReason,
	} {
		_, err := w.inbox.Done(ctx, by(bot(w.kai), item), cl)
		require.ErrorIs(t, err, storeerr.ErrInvalid, name)
	}

	w.unchanged(t, item)

	_, err := w.inbox.Done(ctx, by(bot(w.kai), item), byDuc([]inboxstore.Link{
		{Kind: inboxstore.LinkWhatsAppMessage, Ref: "fixed", Account: "main"},
	}))
	require.ErrorIs(t, err, storeerr.ErrInvalid, "an account without its chat")

	byHand := w.open(t)
	_, err = w.inbox.Done(ctx, by(bot(w.kai), byHand), byDuc(theirs("team", "fixed")))
	require.ErrorIs(t, err, storeerr.ErrForbidden, "the owner added it himself")
	w.unchanged(t, byHand)
}

// Scenario: issue #1204, the brief. What a Bot changed since a moment is listed with the Bots' own changes since
// then, closed items too: kai closing an item that Đức Anh did, not an item only the owner changed, nor one changed
// before. The owner's changes and the Bots' are not asked together.
func TestWhatABotChangedSinceAMomentIsListed(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newWorld(t)
	w.inbox.WithSent(teamMessages())

	before := w.teamAsk(t, inboxstore.Open)
	closed := w.teamAsk(t, inboxstore.Open)
	ownerOnly := w.open(t)

	since := t0.Add(time.Second)
	w.clock.now = since.Add(time.Second)

	notes := "Key Luma"
	_, err := w.inbox.Edit(ctx, by(owner, ownerOnly), inboxstore.Edit{Notes: &notes})
	require.NoError(t, err)

	_, err = w.inbox.Done(ctx, by(bot(w.kai), closed), byDuc(theirs("team", "fixed")))
	require.NoError(t, err)

	page, err := w.inbox.List(ctx, inboxstore.Filter{BotChangedSince: since}, "", 0)
	require.NoError(t, err)
	require.Equal(t, []string{closed.ID}, idsOf(page.Items), "not %s, changed before; not %s, the owner's", before.ID,
		ownerOnly.ID)
	assert.Equal(t, 1, page.Total)

	got := page.Items[0]
	assert.Equal(t, inboxstore.Done, got.State)
	require.Len(t, got.Events, 1, "only the Bots' changes since then")
	assert.Equal(t, bot(w.kai), got.Events[0].Actor)
	assert.Contains(t, string(got.Events[0].After), "It's fixed and live on production")

	_, err = w.inbox.List(ctx, inboxstore.Filter{BotChangedSince: since, OwnerChangedSince: since}, "", 0)
	require.ErrorIs(t, err, storeerr.ErrInvalid)
}
