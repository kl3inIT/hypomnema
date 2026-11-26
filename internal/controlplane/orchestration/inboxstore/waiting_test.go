package inboxstore_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/inboxstore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/storeerr"
)

// zaloAsk is "Reply to Lan", an ask to the owner that botID found in the team's Zalo chat: open, asked_directly,
// with the reply it drafted for him.
func (w *world) zaloAsk(t *testing.T, botID string) inboxstore.Todo {
	t.Helper()

	td, err := w.inbox.Create(t.Context(), bot(botID), inboxstore.NewTodo{
		Title: "Reply to Lan", State: inboxstore.Open, WhyOpen: inboxstore.AskedDirectly, Assignee: me,
		Draft: "Dạ để anh xem ạ.",
		Source: inboxstore.Source{Kind: inboxstore.FromZalo, ID: "team", Account: "main",
			Quote: "Anh Đạt sửa giúp em slide?", Ref: &inboxstore.Link{Kind: inboxstore.LinkZaloMessage, Ref: "asked"}},
	})
	require.NoError(t, err)

	return td
}

// took is "em nhận" by name, the Zalo message 'took', with the 48 hours to chase them.
func took(name string) inboxstore.WaitOn {
	return inboxstore.WaitOn{
		WaitingOn: inboxstore.WaitingOn{By: name, Quote: "em nhận",
			Ref: inboxstore.Link{Kind: inboxstore.LinkZaloMessage, Ref: "took"}},
		DueIn: &inboxstore.DueIn{After: 48 * time.Hour, Zone: zone},
	}
}

// waitingOf is the waiting_on of an event's after.
func waitingOf(t *testing.T, e inboxstore.Event) map[string]any {
	t.Helper()

	_, after := eventFields(t, e)
	got, _ := after["waiting_on"].(map[string]any)

	return got
}

func last(td inboxstore.Todo) inboxstore.Event { return td.Events[len(td.Events)-1] }

// Scenario: issue #1142, rule 2. Việt answers in the team's chat the ask that kai listed for the owner: "em nhận".
// kai moves the item without asking: given to Việt, still open, why_open waiting, without the draft the owner has no
// reason to send, and due 48 hours later, the day to chase him. The event says who has it, why, the draft before,
// and Việt's message. The owner owes nothing more: Needs you counts it no more.
func TestABotMovesItsItemToWaitingOnTheTeammateWhoTookIt(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newWorld(t)
	asked := w.zaloAsk(t, w.kai)

	n, err := w.inbox.NeedsYou(ctx, w.clock.now)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "his reply drafted waits on him")

	moved, err := w.inbox.WaitOn(ctx, by(bot(w.kai), asked), took("Việt"))
	require.NoError(t, err)

	assert.Equal(t, inboxstore.Assignee{Kind: inboxstore.Person, Name: "Việt"}, moved.Assignee)
	assert.Equal(t, inboxstore.Open, moved.State)
	assert.Equal(t, inboxstore.Waiting, moved.WhyOpen)
	assert.Empty(t, moved.Draft)
	assert.Equal(t, inboxstore.DueInstant, moved.Due.Kind)
	assert.Equal(t, t0.Add(48*time.Hour), moved.Due.At.UTC())
	assert.Empty(t, moved.PassedBy)

	e := last(moved)
	assert.Equal(t, inboxstore.EventEdited, e.Kind)
	assert.Equal(t, bot(w.kai), e.Actor)

	before, after := eventFields(t, e)
	assert.Equal(t, "Dạ để anh xem ạ.", before["draft"], "the draft is kept in the history")
	assert.Equal(t, inboxstore.AskedDirectly, before["why_open"])
	assert.Equal(t, inboxstore.Waiting, after["why_open"])
	assert.Equal(t, "Việt", after["assignee"])
	assert.Equal(t, inboxstore.Person, after["assignee_kind"])
	assert.Equal(t, map[string]any{"by": "Việt", "quote": "em nhận",
		"ref": map[string]any{"kind": inboxstore.LinkZaloMessage, "ref": "took"}}, waitingOf(t, e))

	n, err = w.inbox.NeedsYou(ctx, w.clock.now.Add(72*time.Hour))
	require.NoError(t, err)
	assert.Zero(t, n, "a person's item, even late, never needs him")
}

// Scenario: issue #1142. A proposal kai found moves too, and opens; a due the person gave is the due; and an item
// already given to a person moves to the one who took it from them (the owner gave 14 items to people by hand on
// 2026-11-25).
func TestWaitOnOpensAProposalTakesTheDueGivenAndMovesFromPersonToPerson(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newWorld(t)

	proposal, err := w.inbox.Create(ctx, bot(w.kai), inboxstore.NewTodo{
		Title: "Slides for Friday", State: inboxstore.Proposed, Assignee: inboxstore.Assignee{Kind: inboxstore.Unknown},
		Source: inboxstore.Source{Kind: inboxstore.FromGmail, ID: "thr_1", Account: "work", Quote: "Who does the slides?",
			Ref: &inboxstore.Link{Kind: inboxstore.LinkMail, Ref: "m1"}},
	})
	require.NoError(t, err)

	friday := inboxstore.Due{Kind: inboxstore.DueDate, Date: "2026-11-27", Zone: zone}
	moved, err := w.inbox.WaitOn(ctx, by(bot(w.kai), proposal), inboxstore.WaitOn{
		WaitingOn: inboxstore.WaitingOn{By: "Kai Yong", Quote: "I'll send you the deck on Friday",
			Ref: inboxstore.Link{Kind: inboxstore.LinkMail, Ref: "m2"}},
		Due: &friday,
	})
	require.NoError(t, err)
	assert.Equal(t, inboxstore.Open, moved.State, "a Bot opens a proposal only so")
	assert.Equal(t, inboxstore.Waiting, moved.WhyOpen)
	assert.Equal(t, friday, moved.Due)

	again, err := w.inbox.WaitOn(ctx, by(bot(w.kai), moved), inboxstore.WaitOn{
		WaitingOn: inboxstore.WaitingOn{By: "Đức Anh", Quote: "Em làm thay anh Kai nhé",
			Ref: inboxstore.Link{Kind: inboxstore.LinkMail, Ref: "m3"}},
	})
	require.NoError(t, err)
	assert.Equal(t, inboxstore.Assignee{Kind: inboxstore.Person, Name: "Đức Anh"}, again.Assignee)
	assert.Equal(t, inboxstore.Waiting, again.WhyOpen)
	assert.Equal(t, friday, again.Due, "no due given keeps the item's")
	assert.Equal(t, "Đức Anh", waitingOf(t, last(again))["by"])
}

// Scenario: issue #1142, what is not moved. Another Bot's item, one the owner wrote, one given to a Bot; one from a
// chat or a Routine's run; one doing, in review, snoozed or closed; and a claim without words, with a message of
// another kind or another meeting's line, or naming a Bot. Each is refused and writes nothing.
func TestWaitOnRefusesWhatIsNotTheBotsOwnFoundItem(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newWorld(t)

	meeting, err := w.inbox.Create(ctx, bot(w.kai), inboxstore.NewTodo{
		Title: "Check the tiles", State: inboxstore.Proposed, Assignee: me,
		Source: inboxstore.Source{Kind: inboxstore.FromMeeting, ID: "mtg_1", Quote: "Ai check tiles?"},
	})
	require.NoError(t, err)

	givenToBot, err := w.inbox.Create(ctx, bot(w.kai), inboxstore.NewTodo{
		Title: "Fix the deploy", State: inboxstore.Proposed, Assignee: inboxstore.Assignee{Kind: inboxstore.ABot,
			Name: w.kai},
		Source: inboxstore.Source{Kind: inboxstore.FromZalo, ID: "team", Account: "main",
			Ref: &inboxstore.Link{Kind: inboxstore.LinkZaloMessage, Ref: "x"}},
	})
	require.NoError(t, err)

	snoozed := w.zaloAsk(t, w.kai)
	later := w.clock.now.Add(time.Hour)
	snoozed, err = w.inbox.Snooze(ctx, by(owner, snoozed), &later)
	require.NoError(t, err)

	dropped, err := w.inbox.Drop(ctx, by(owner, w.zaloAsk(t, w.kai)), "Not mine.", nil)
	require.NoError(t, err)

	line := func(ref string) inboxstore.WaitOn {
		wo := took("Lan")
		wo.Ref = inboxstore.Link{Kind: inboxstore.LinkMeetingLine, Ref: ref}

		return wo
	}
	noQuote := took("Lan")
	noQuote.Quote = " "
	otherKind := took("Lan")
	otherKind.Ref.Kind = inboxstore.LinkWhatsAppMessage

	cases := []struct {
		name  string
		actor inboxstore.Actor
		td    inboxstore.Todo
		wo    inboxstore.WaitOn
		want  error
	}{
		{"another Bot's item", bot(w.lan), w.zaloAsk(t, w.kai), took("Lan"), storeerr.ErrForbidden},
		{"the owner moves it", owner, w.zaloAsk(t, w.kai), took("Lan"), storeerr.ErrForbidden},
		{"an item the owner wrote", bot(w.kai), w.open(t), took("Lan"), storeerr.ErrForbidden},
		{"an item given to a Bot", bot(w.kai), givenToBot, took("Lan"), storeerr.ErrForbidden},
		{"an item from its chat", bot(w.kai), w.fromChat(t), took("Lan"), storeerr.ErrInvalid},
		{"an item from a Routine's run", bot(w.kai), w.proposed(t), took("Lan"), storeerr.ErrInvalid},
		{"an item doing", bot(w.kai), w.doing(t), took("Lan"), storeerr.ErrConflict},
		{"an item in review", bot(w.kai), w.review(t), took("Lan"), storeerr.ErrConflict},
		{"a snoozed item", bot(w.kai), snoozed, took("Lan"), storeerr.ErrConflict},
		{"a closed item", bot(w.kai), dropped, took("Lan"), storeerr.ErrConflict},
		{"no words", bot(w.kai), w.zaloAsk(t, w.kai), noQuote, storeerr.ErrInvalid},
		{"nobody named", bot(w.kai), w.zaloAsk(t, w.kai), took(" "), storeerr.ErrInvalid},
		{"a message of another kind", bot(w.kai), w.zaloAsk(t, w.kai), otherKind, storeerr.ErrInvalid},
		{"a Bot named as the person", bot(w.kai), w.zaloAsk(t, w.kai), took(w.lan), storeerr.ErrInvalid},
		{"another meeting's line", bot(w.kai), meeting, line("mtg_2/4"), storeerr.ErrInvalid},
		{"a meeting's line without its number", bot(w.kai), meeting, line("mtg_1/"), storeerr.ErrInvalid},
	}

	for _, c := range cases {
		before, err := w.inbox.Get(ctx, c.td.ID)
		require.NoError(t, err, c.name)

		_, err = w.inbox.WaitOn(ctx, by(c.actor, before), c.wo)
		require.ErrorIs(t, err, c.want, c.name)
		w.unchanged(t, before)
	}

	moved, err := w.inbox.WaitOn(ctx, by(bot(w.kai), meeting), line("mtg_1/7"))
	require.NoError(t, err, "a line of the item's own meeting")
	assert.Equal(t, inboxstore.Waiting, moved.WhyOpen)
}

// Scenario: issue #1142, rule 2 on create. A meeting action Lan promised the owner is added waiting on her, from the
// action's line, though the meeting's item names no line of its own; the created event holds waiting_on. Without
// waiting_on, given to someone else than whom it waits on, or from a Routine's run it is refused; a retry with the
// same key and another waiting_on is another request.
func TestABotAddsAnItemWaitingOnAPerson(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newWorld(t)

	lan := inboxstore.WaitingOn{By: "Lan", Quote: "Lan: em gửi anh bản deck thứ Sáu",
		Ref: inboxstore.Link{Kind: inboxstore.LinkMeetingLine, Ref: "mtg_1/12"}}
	n := inboxstore.NewTodo{
		Title: "Lan sends the deck", State: inboxstore.Open, WhyOpen: inboxstore.Waiting, WaitingOn: &lan,
		Assignee:       inboxstore.Assignee{Kind: inboxstore.Person, Name: "Lan"},
		DueIn:          &inboxstore.DueIn{After: 48 * time.Hour, Zone: zone},
		Source:         inboxstore.Source{Kind: inboxstore.FromMeeting, ID: "mtg_1", Quote: "Em gửi anh bản deck thứ Sáu"},
		IdempotencyKey: "mtg_1/12",
	}

	td, err := w.inbox.Create(ctx, bot(w.kai), n)
	require.NoError(t, err)
	assert.Equal(t, inboxstore.Waiting, td.WhyOpen)
	assert.Equal(t, inboxstore.Open, td.State)
	assert.Equal(t, t0.Add(48*time.Hour), td.Due.At.UTC())
	ref, _ := waitingOf(t, td.Events[0])["ref"].(map[string]any)
	assert.Equal(t, "mtg_1/12", ref["ref"])

	same, err := w.inbox.Create(ctx, bot(w.kai), n)
	require.NoError(t, err)
	assert.Equal(t, td.ID, same.ID)

	other := n
	otherLan := lan
	otherLan.Quote = "Lan: thứ Hai nhé"
	other.WaitingOn = &otherLan
	_, err = w.inbox.Create(ctx, bot(w.kai), other)
	require.ErrorIs(t, err, storeerr.ErrConflict, "the same key for another request")

	without := n
	without.WaitingOn, without.IdempotencyKey = nil, ""
	notHers := n
	notHers.Assignee, notHers.IdempotencyKey = inboxstore.Assignee{Kind: inboxstore.Person, Name: "Việt"}, ""
	notWaiting := n
	notWaiting.WhyOpen, notWaiting.IdempotencyKey = inboxstore.Promised, ""
	fromRun := n
	fromRun.Source, fromRun.IdempotencyKey = inboxstore.Source{Kind: inboxstore.FromRoutine, ID: "rrn_1",
		Quote: "x"}, ""
	toMe := n
	toMe.Assignee, toMe.IdempotencyKey = me, ""

	for name, c := range map[string]struct {
		n    inboxstore.NewTodo
		want error
	}{
		"without waiting_on":          {without, storeerr.ErrInvalid},
		"given to another person":     {notHers, storeerr.ErrInvalid},
		"given to the owner":          {toMe, storeerr.ErrInvalid},
		"waiting_on with another why": {notWaiting, storeerr.ErrInvalid},
		"from a Routine's run":        {fromRun, storeerr.ErrForbidden},
	} {
		_, err := w.inbox.Create(ctx, bot(w.kai), c.n)
		require.ErrorIs(t, err, c.want, name)
	}
}

// Scenario: issue #1142, B7. Whatever gives a waiting item to someone else clears why_open waiting in the same write,
// shown in its event: the owner's Take it back (he gives it to himself), which the triage learns from as his change,
// his hand-over to a Bot, a Bot's hand_back and the main Bot's pass_to. A change that leaves it with the person keeps
// it.
func TestGivingAWaitingItemToAnyoneElseClearsWaiting(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newWorld(t)
	main := orchestration.DefaultBotID

	waiting := func(botID string) inboxstore.Todo {
		td, err := w.inbox.WaitOn(t.Context(), by(bot(botID), w.zaloAsk(t, botID)), took("Việt"))
		require.NoError(t, err)

		return td
	}
	since := w.clock.now

	notes := "Việt has the slides."
	kept, err := w.inbox.Edit(ctx, by(bot(w.kai), waiting(w.kai)), inboxstore.Edit{Notes: &notes})
	require.NoError(t, err)
	assert.Equal(t, inboxstore.Waiting, kept.WhyOpen, "another change keeps it")

	takenBack := waiting(w.kai)
	cases := map[string]func() (inboxstore.Todo, error){
		"the owner takes it back": func() (inboxstore.Todo, error) {
			return w.inbox.Edit(ctx, by(owner, takenBack), inboxstore.Edit{Assignee: &me})
		},
		"the owner hands it to a Bot": func() (inboxstore.Todo, error) {
			return w.inbox.Hand(ctx, by(owner, waiting(w.kai)), w.kai, "")
		},
		"a Bot hands it back": func() (inboxstore.Todo, error) {
			return w.inbox.Edit(ctx, by(bot(w.kai), waiting(w.kai)), inboxstore.Edit{Assignee: &me})
		},
		"the main Bot passes it": func() (inboxstore.Todo, error) {
			return w.inbox.Edit(ctx, by(bot(main), waiting(main)),
				inboxstore.Edit{Assignee: &inboxstore.Assignee{Kind: inboxstore.ABot, Name: w.kai}})
		},
	}

	for name, do := range cases {
		td, err := do()
		require.NoError(t, err, name)
		assert.Empty(t, td.WhyOpen, name)

		before, after := eventFields(t, last(td))
		assert.Equal(t, inboxstore.Waiting, before["why_open"], name)
		assert.Contains(t, after, "why_open", name)
		assert.Nil(t, after["why_open"], name)
	}

	page, err := w.inbox.List(ctx, inboxstore.Filter{OwnerChangedSince: since}, "", 0)
	require.NoError(t, err)
	assert.Contains(t, idsOf(page.Items), takenBack.ID, "Take it back is his change, which the triage reads")
}
