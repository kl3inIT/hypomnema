package orchestration

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/inboxstore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/storeerr"
)

// passWorld is the main Bot and two project Bots, each with what it is and the accounts it may use, and an item the
// owner handed to the main Bot.
type passWorld struct {
	*world
	beyond, vmap Bot
	item         inboxstore.Todo
}

func newPassWorld(t *testing.T) passWorld {
	t.Helper()

	w := newSeedWorld(t)
	ctx := t.Context()

	for _, c := range []Connection{
		{Integration: "google", Label: "beyondpilot", Secret: "s-mail"},
		{Integration: "whatsapp", Label: "beyondpilot", Secret: "s-wa"},
	} {
		_, err := w.s.SaveServiceConnection(ctx, c)
		require.NoError(t, err)
	}

	beyond, err := w.s.PutBot(ctx, Bot{
		Name:        "BeyondPilot",
		Persona:     "The BeyondPilot project for GenAI Fund: Kai Yong and Eugene, the pilot's mail and its repository.",
		Connections: AllowList{Limited: true, Names: []string{"google:beyondpilot", "whatsapp:beyondpilot"}},
	})
	require.NoError(t, err)

	vmap, err := w.s.PutBot(ctx, Bot{Name: "VMAP", Persona: "VMAP at VTII: the map app's data and its Lark groups."})
	require.NoError(t, err)

	item, err := w.s.Inbox().Create(ctx, inboxOwner, inboxstore.NewTodo{
		Title: "Send Kai the pilot's report", State: inboxstore.Open, Source: inboxstore.Source{Kind: inboxstore.ByHand},
		Assignee: inboxstore.Assignee{Kind: inboxstore.Me},
	})
	require.NoError(t, err)

	item, err = w.s.Inbox().Hand(ctx, inboxstore.Change{Actor: inboxOwner, ID: item.ID, Revision: item.Revision},
		DefaultBotID, "Do it")
	require.NoError(t, err)

	return passWorld{world: w, beyond: beyond, vmap: vmap, item: item}
}

func asBot(id string) inboxstore.Actor { return inboxstore.Actor{Kind: inboxstore.Bot, ID: id} }

// pass is a Bot's edit that gives the item to another.
func (w passWorld) pass(by string, td inboxstore.Todo, to inboxstore.Assignee) (inboxstore.Todo, error) {
	return w.s.Inbox().Edit(w.t.Context(), inboxstore.Change{Actor: asBot(by), ID: td.ID, Revision: td.Revision},
		inboxstore.Edit{Assignee: &to})
}

// Scenario: issue #1118, section 2. The main Bot handed an item is told the project Bots it may pass it to, each
// with what it is and the accounts it may use, and when to pass it rather than do it or start a Run.
func TestTheMainBotHandedAnItemIsToldTheProjectBots(t *testing.T) {
	t.Parallel()

	w := newPassWorld(t)

	wakes := w.wakes(w.item.Chat)
	require.Len(t, wakes, 1)

	for _, want := range []string{
		"BeyondPilot", w.beyond.ID, "GenAI Fund", "google:beyondpilot", "whatsapp:beyondpilot",
		"VMAP", w.vmap.ID, "Lark groups", "every Connection", "pass_to",
	} {
		assert.Contains(t, wakes[0], want)
	}

	assert.NotContains(t, wakes[0], DefaultBotID+" (", "the main Bot is not listed as a Bot to pass to")
}

// Scenario: issue #1118, sections 1, 3 and 4. The main Bot passes the item to the project Bot that fits: it is that
// Bot's, through the main Bot, the thread shows the hand-over, and that Bot is woken in a chat of its own with who
// passed it and the rule every Bot given an item follows.
func TestTheMainBotPassesAnItemToAProjectBot(t *testing.T) {
	t.Parallel()

	w := newPassWorld(t)

	got, err := w.pass(DefaultBotID, w.item, inboxstore.Assignee{Kind: inboxstore.ABot, Name: w.beyond.ID})
	require.NoError(t, err)

	assert.Equal(t, inboxstore.Assignee{Kind: inboxstore.ABot, Name: w.beyond.ID}, got.Assignee)
	assert.Equal(t, DefaultBotID, got.PassedBy, "the item is the project Bot's through the main Bot")
	require.NotEmpty(t, got.Chat)
	assert.NotEqual(t, w.item.Chat, got.Chat, "the project Bot works in a chat of its own")

	chat, err := w.s.GetChat(t.Context(), got.Chat)
	require.NoError(t, err)
	assert.Equal(t, w.beyond.ID, chat.Bot)

	wakes := w.wakes(got.Chat)
	require.Len(t, wakes, 1)

	for _, want := range []string{
		"passed you Inbox item " + got.ID, "inbox_get", "inbox_comment", "inbox_done", "hand_back", "pass_to",
	} {
		assert.Contains(t, wakes[0], want)
	}

	assert.NotContains(t, wakes[0], "GenAI Fund", "only the main Bot is told the project Bots")

	last := got.Events[len(got.Events)-1]
	assert.Equal(t, inboxstore.EventEdited, last.Kind)
	assert.Equal(t, asBot(DefaultBotID), last.Actor)

	var after map[string]any
	require.NoError(t, json.Unmarshal(last.After, &after))
	assert.Equal(t, map[string]any{"assignee_kind": "bot", "assignee": w.beyond.ID, "passed_by": DefaultBotID}, after,
		"the thread shows the hand-over: to whom, and through whom")
}

// Scenario: issue #1118, sections 1 and 5. No loops: a project Bot passes an item to no other project Bot; it gives
// it back to the main Bot, which is woken with it, or to the owner, and then it is nobody's through a Bot.
func TestAProjectBotGivesAnItemBackOnlyToTheMainBotOrTheOwner(t *testing.T) {
	t.Parallel()

	w := newPassWorld(t)

	passed, err := w.pass(DefaultBotID, w.item, inboxstore.Assignee{Kind: inboxstore.ABot, Name: w.beyond.ID})
	require.NoError(t, err)

	_, err = w.pass(w.beyond.ID, passed, inboxstore.Assignee{Kind: inboxstore.ABot, Name: w.vmap.ID})
	require.ErrorIs(t, err, storeerr.ErrForbidden, "a project Bot passes to no other project Bot")

	_, err = w.pass(w.beyond.ID, passed, inboxstore.Assignee{Kind: inboxstore.Person, Name: "Kai"})
	require.ErrorIs(t, err, storeerr.ErrForbidden, "nor to a person")

	back, err := w.pass(w.beyond.ID, passed, inboxstore.Assignee{Kind: inboxstore.ABot, Name: DefaultBotID})
	require.NoError(t, err)
	assert.Equal(t, inboxstore.Assignee{Kind: inboxstore.ABot, Name: DefaultBotID}, back.Assignee)
	assert.Equal(t, w.beyond.ID, back.PassedBy)

	chat, err := w.s.GetChat(t.Context(), back.Chat)
	require.NoError(t, err)
	assert.Equal(t, DefaultBotID, chat.Bot, "the main Bot is woken with it in a chat of its own")

	wakes := w.wakes(back.Chat)
	require.Len(t, wakes, 1)
	assert.Contains(t, wakes[0], "BeyondPilot passed you Inbox item", "the main Bot is told who gave it back")

	again, err := w.pass(DefaultBotID, back, inboxstore.Assignee{Kind: inboxstore.ABot, Name: w.beyond.ID})
	require.NoError(t, err)

	mine, err := w.pass(w.beyond.ID, again, inboxstore.Assignee{Kind: inboxstore.Me})
	require.NoError(t, err)
	assert.Equal(t, inboxstore.Assignee{Kind: inboxstore.Me}, mine.Assignee)
	assert.Empty(t, mine.PassedBy, "an item back with the owner is nobody's through a Bot")

	handed, err := w.s.Inbox().Hand(t.Context(),
		inboxstore.Change{Actor: inboxOwner, ID: mine.ID, Revision: mine.Revision}, w.vmap.ID, "")
	require.NoError(t, err)
	assert.Empty(t, handed.PassedBy, "the owner gives it himself")
}

// Scenario: issue #1118, section 5, for an item a Bot adds: a project Bot adds none for another project Bot, which
// would pass it round them; the main Bot adds one for a project Bot, through itself, and wakes it.
func TestOnlyTheMainBotAddsAnItemForAnotherBot(t *testing.T) {
	t.Parallel()

	w := newPassWorld(t)
	ctx := t.Context()
	n := func(to string) inboxstore.NewTodo {
		return inboxstore.NewTodo{
			Title: "Check the pilot's mail", State: inboxstore.Open,
			Source:   inboxstore.Source{Kind: inboxstore.FromChat, ID: w.item.Chat},
			Assignee: inboxstore.Assignee{Kind: inboxstore.ABot, Name: to},
		}
	}

	_, err := w.s.Inbox().Create(ctx, asBot(w.vmap.ID), n(w.beyond.ID))
	require.ErrorIs(t, err, storeerr.ErrForbidden)

	own, err := w.s.Inbox().Create(ctx, asBot(w.vmap.ID), n(w.vmap.ID))
	require.NoError(t, err, "a Bot adds one for itself")
	assert.Empty(t, own.PassedBy)

	added, err := w.s.Inbox().Create(ctx, inboxBot, n(w.beyond.ID))
	require.NoError(t, err)
	assert.Equal(t, DefaultBotID, added.PassedBy)
	require.NotEmpty(t, added.Chat)
	assert.Len(t, w.wakes(added.Chat), 1)
}
