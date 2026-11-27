package orchestration

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/inboxstore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/storeerr"
)

var inboxOwner = inboxstore.Actor{Kind: inboxstore.Owner}

// wakes are the wake-ups a chat has for its Bot's pi, oldest first.
func (w *world) wakes(chatID string) []string {
	w.t.Helper()

	msgs, err := w.s.Undelivered(w.t.Context(), chatID)
	require.NoError(w.t, err)

	var out []string

	for _, m := range msgs {
		if m.Role == ChatSystem && m.Kind == ChatWake {
			out = append(out, m.Text)
		}
	}

	return out
}

// Scenario: issue #1111, section 1. The owner hands a proposal the Thư ký Bot found to the main Bot with a prompt:
// it is accepted and given to the Bot, the prompt is the first comment of its thread, and the Bot is woken in a
// chat of its own for the item with the prompt and what goes with the item: its quote, source, due and draft.
func TestHandingAnItemToABotWakesItWithThePromptAndTheItem(t *testing.T) {
	t.Parallel()

	w := newSeedWorld(t)
	ctx := t.Context()

	_, err := w.s.SaveServiceConnection(ctx, Connection{
		Integration: "whatsapp", Label: "me", Secret: "conn-whatsapp-me",
	})
	require.NoError(t, err)

	found, err := w.s.Inbox().Create(ctx, inboxBot, inboxstore.NewTodo{
		Title: "Reply to Kai about the pilot", State: inboxstore.Proposed, Assignee: inboxstore.Assignee{Kind: inboxstore.Me},
		Draft: "Hi Kai, the pilot starts Monday.",
		Due:   inboxstore.Due{Kind: inboxstore.DueDate, Date: "2026-11-25", Zone: "Asia/Ho_Chi_Minh"},
		Source: inboxstore.Source{
			Kind: inboxstore.FromWhatsApp, ID: "8491@s.whatsapp.net", Account: "me",
			Quote: "Can we start the pilot next week?",
			Ref:   &inboxstore.Link{Kind: inboxstore.LinkWhatsAppMessage, Ref: "3EB0A1"},
		},
	})
	require.NoError(t, err)
	assert.Empty(t, found.Chat, "an item no Bot was given has no chat")

	prompt := "Do it now, then draft a reply to Kai for me to send."

	got, err := w.s.Inbox().Hand(ctx, inboxstore.Change{Actor: inboxOwner, ID: found.ID, Revision: found.Revision},
		DefaultBotID, prompt)
	require.NoError(t, err)

	assert.Equal(t, inboxstore.Assignee{Kind: inboxstore.ABot, Name: DefaultBotID}, got.Assignee)
	assert.Equal(t, inboxstore.Open, got.State, "a proposal handed over is accepted")
	require.NotEmpty(t, got.Chat)

	chat, err := w.s.GetChat(ctx, got.Chat)
	require.NoError(t, err)
	assert.Equal(t, DefaultBotID, chat.Bot)
	assert.Equal(t, "Inbox: Reply to Kai about the pilot", chat.Title)

	wakes := w.wakes(got.Chat)
	require.Len(t, wakes, 1)

	for _, want := range []string{
		found.ID, prompt, "Can we start the pilot next week?", "whatsapp me 8491@s.whatsapp.net", "2026-11-25",
		"Hi Kai, the pilot starts Monday.", "inbox_comment",
	} {
		assert.Contains(t, wakes[0], want)
	}

	last := got.Events[len(got.Events)-1]
	assert.Equal(t, inboxstore.EventCommented, last.Kind)
	assert.Equal(t, inboxOwner, last.Actor)
	assert.JSONEq(t, `{"text":"`+prompt+`"}`, string(last.After), "the prompt is the thread's first comment")
	assert.Equal(t, got.Revision, last.Revision)
}

// Scenario: issue #1111, section 2. In the item's thread a Bot's comment and the owner's are kept in the order they
// were written; the owner's reaches the Bot in the item's chat, the Bot's wakes nobody.
func TestTheThreadKeepsTheBotsAndTheOwnersCommentsInOrder(t *testing.T) {
	t.Parallel()

	w := newSeedWorld(t)
	ctx := t.Context()

	td, err := w.s.Inbox().Create(ctx, inboxOwner, inboxstore.NewTodo{
		Title: "Fix the login page", State: inboxstore.Open, Source: inboxstore.Source{Kind: inboxstore.ByHand},
		Assignee: inboxstore.Assignee{Kind: inboxstore.Me},
	})
	require.NoError(t, err)

	td, err = w.s.Inbox().Hand(ctx, inboxstore.Change{Actor: inboxOwner, ID: td.ID, Revision: td.Revision},
		DefaultBotID, "Give it to a worker to fix the code.")
	require.NoError(t, err)

	_, err = w.s.Inbox().Comment(ctx, inboxBot, td.ID, "Started: Run run_1 fixes it.")
	require.NoError(t, err)

	_, err = w.s.Inbox().Comment(ctx, inboxOwner, td.ID, "Keep the old button too.")
	require.NoError(t, err)

	got, err := w.s.Inbox().Comment(ctx, inboxBot, td.ID, "Done: pull request #12.")
	require.NoError(t, err)

	var thread []string

	for _, e := range got.Events {
		if e.Kind == inboxstore.EventCommented {
			thread = append(thread, e.Actor.Kind+": "+string(e.After))
		}
	}

	assert.Equal(t, []string{
		`owner: {"text":"Give it to a worker to fix the code."}`,
		`bot: {"text":"Started: Run run_1 fixes it."}`,
		`owner: {"text":"Keep the old button too."}`,
		`bot: {"text":"Done: pull request #12."}`,
	}, thread)
	assert.Equal(t, td.Revision+3, got.Revision, "each comment moves the revision on")

	wakes := w.wakes(td.Chat)
	require.Len(t, wakes, 2, "the hand-over and the owner's comment wake the Bot; its own comments do not")
	assert.Contains(t, wakes[1], "Keep the old button too.")
	assert.Contains(t, wakes[1], "<owner_comment>")
	// His comment is evidence that he did it, by its revision (issue #1206).
	assert.Contains(t, wakes[1], fmt.Sprintf(`{"kind":"comment","ref":"%d"}`, td.Revision+2))
	assert.Contains(t, wakes[1], "for_owner")

	other, err := w.s.PutBot(ctx, Bot{Name: "other"})
	require.NoError(t, err)

	_, err = w.s.Inbox().Comment(ctx, inboxstore.Actor{Kind: inboxstore.Bot, ID: other.ID}, td.ID, "Mine?")
	require.ErrorIs(t, err, storeerr.ErrForbidden, "a Bot comments only on its own items")
}

// Scenario: issue #1111, section 1, "Waking on assignment". An item given to any Bot wakes that Bot, whoever gives
// it and however: added for it, or edited to it; giving it to the same Bot again does not wake it twice, and a
// deleted chat is replaced by a new one at the next hand-over. Only the owner hands an item over, and not a closed
// one.
func TestAnItemGivenToABotWakesThatBot(t *testing.T) {
	t.Parallel()

	w := newSeedWorld(t)
	ctx := t.Context()

	other, err := w.s.PutBot(ctx, Bot{Name: "writer"})
	require.NoError(t, err)

	added, err := w.s.Inbox().Create(ctx, inboxOwner, inboxstore.NewTodo{
		Title: "Write the weekly report", State: inboxstore.Open, Source: inboxstore.Source{Kind: inboxstore.ByHand},
		Assignee: inboxstore.Assignee{Kind: inboxstore.ABot, Name: other.ID},
	})
	require.NoError(t, err)
	require.NotEmpty(t, added.Chat, "an item added for a Bot wakes it")
	assert.Len(t, w.wakes(added.Chat), 1)

	mine, err := w.s.Inbox().Create(ctx, inboxOwner, inboxstore.NewTodo{
		Title: "Book the flight", State: inboxstore.Open, Source: inboxstore.Source{Kind: inboxstore.ByHand},
		Assignee: inboxstore.Assignee{Kind: inboxstore.Me},
	})
	require.NoError(t, err)

	notes := "the cheap one"
	edited, err := w.s.Inbox().Edit(ctx, inboxstore.Change{Actor: inboxOwner, ID: mine.ID, Revision: mine.Revision},
		inboxstore.Edit{Assignee: &inboxstore.Assignee{Kind: inboxstore.ABot, Name: DefaultBotID}})
	require.NoError(t, err)
	require.NotEmpty(t, edited.Chat, "an item edited to a Bot wakes it")
	assert.Len(t, w.wakes(edited.Chat), 1)

	edited, err = w.s.Inbox().Edit(ctx, inboxstore.Change{Actor: inboxOwner, ID: mine.ID, Revision: edited.Revision},
		inboxstore.Edit{Notes: &notes})
	require.NoError(t, err)
	assert.Len(t, w.wakes(edited.Chat), 1, "an edit that keeps the Bot wakes it no more")

	_, err = w.s.Inbox().Hand(ctx, inboxstore.Change{Actor: inboxBot, ID: mine.ID, Revision: edited.Revision},
		DefaultBotID, "")
	require.ErrorIs(t, err, storeerr.ErrForbidden, "only the owner hands an item over")

	_, err = w.s.DeleteChat(ctx, edited.Chat)
	require.NoError(t, err)
	assert.Empty(t, w.todo(mine.ID).Chat, "a deleted chat is no item's")

	again, err := w.s.Inbox().Hand(ctx, inboxstore.Change{Actor: inboxOwner, ID: mine.ID, Revision: edited.Revision},
		DefaultBotID, "")
	require.NoError(t, err)
	require.NotEmpty(t, again.Chat)
	assert.NotEqual(t, edited.Chat, again.Chat, "the next hand-over opens a new chat")
	assert.Len(t, w.wakes(again.Chat), 1)

	closed, err := w.s.Inbox().Done(ctx, inboxstore.Change{Actor: inboxOwner, ID: mine.ID, Revision: again.Revision},
		inboxstore.Closing{DoneBy: inboxstore.Assignee{Kind: inboxstore.Me}, How: inboxstore.ClosedByHand})
	require.NoError(t, err)

	_, err = w.s.Inbox().Hand(ctx, inboxstore.Change{Actor: inboxOwner, ID: mine.ID, Revision: closed.Revision},
		DefaultBotID, "")
	require.ErrorIs(t, err, storeerr.ErrConflict, "a closed item is reopened before it is handed over")
}
