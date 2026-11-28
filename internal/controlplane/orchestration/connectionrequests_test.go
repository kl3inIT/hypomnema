package orchestration

import (
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// requestWorld is a Bot whose Connections list is limited to google:work, in its main chat, beside the Connections
// it may and may not ask for (issue #471, part D).
type requestWorld struct {
	*world
	bot  Bot
	main Chat
}

func newRequestWorld(t *testing.T) requestWorld {
	t.Helper()

	w := newWorld(t)
	ctx := t.Context()

	for _, c := range []Connection{
		{Integration: "google", Label: "work", Secret: "s-work"},
		{Integration: "google", Label: "team", Secret: "s-team"},
		{Integration: "google", Label: "off", Secret: "s-off"},
		{Integration: "whatsapp", Label: "me", Secret: "s-me"},
		{Integration: "lark", Label: "me", Secret: "s-lark-me", AppLabel: "app"},
	} {
		_, err := w.s.SaveServiceConnection(ctx, c)
		require.NoError(t, err)

		if c.Label != "off" {
			_, err = w.s.SetConnectionChats(ctx, c.Integration, c.Label, true)
			require.NoError(t, err)
		}
	}

	bot, err := w.s.PutBot(ctx, Bot{
		Name:        "hr",
		Connections: AllowList{Limited: true, Names: []string{"google:work"}},
		AgentSpec:   AgentSpec{Instructions: "people"},
	})
	require.NoError(t, err)

	main, _, err := w.s.OpenMainChat(ctx, bot.ID, "", "", ChatModel{})
	require.NoError(t, err)

	return requestWorld{world: w, bot: bot, main: main}
}

func (w requestWorld) request(t *testing.T, use string) ConnectionRequest {
	t.Helper()

	r, err := w.s.RequestConnection(t.Context(), w.bot.ID, w.main.ID, use, "to read the team's calendar")
	require.NoError(t, err)

	return r
}

func (w requestWorld) requests(t *testing.T) []ConnectionRequest {
	t.Helper()

	all, err := w.s.ListConnectionRequests(t.Context(), ConnectionRequestFilter{})
	require.NoError(t, err)

	return all
}

// wakes are the lines pi is given that it has not had yet, which marks them given.
func (w requestWorld) wakes(t *testing.T) []string {
	t.Helper()

	got, err := w.s.Undelivered(t.Context(), w.main.ID)
	require.NoError(t, err)

	var out []string

	for _, m := range got {
		out = append(out, m.Text)
		require.NoError(t, w.s.MarkDelivered(t.Context(), w.main.ID, m.Seq))
	}

	return out
}

// Scenario: what a Bot may ask for is each Connection that exists, is on for chats, is not the owner's own account
// and is outside its limited list; a Bot whose list is not limited asks for none.
func TestABotMayAskOnlyForGrantableConnections(t *testing.T) {
	t.Parallel()

	w := newRequestWorld(t)
	ctx := t.Context()

	got, err := w.s.GrantableConnections(ctx, w.bot)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "google:team", Use(got[0].Integration, got[0].Label))

	for _, use := range []string{"google:work", "google:off", "whatsapp:me", "lark:me", "google:none"} {
		_, err := w.s.RequestConnection(ctx, w.bot.ID, w.main.ID, use, "")
		require.Error(t, err, use)
	}

	assert.Empty(t, w.requests(t), "a Connection that cannot be granted makes no card")

	every, err := w.s.PutBot(ctx, Bot{Name: "every", AgentSpec: AgentSpec{Instructions: "p"}})
	require.NoError(t, err)

	got, err = w.s.GrantableConnections(ctx, every)
	require.NoError(t, err)
	assert.Empty(t, got)
}

// Scenario: a Connection is asked for only from the Bot's main chat: another chat of the Bot, a Routine's chat, a
// Lark chat, another Bot's main chat and a Bot that is not there are refused, and make no card.
func TestAConnectionIsAskedForOnlyFromTheBotsMainChat(t *testing.T) {
	t.Parallel()

	w := newRequestWorld(t)
	ctx := t.Context()

	other, err := w.s.CreateChatWithID(ctx, "", w.bot.ID, "another chat", ChatModel{})
	require.NoError(t, err)

	mainBot, _, err := w.s.OpenMainChat(ctx, "", "", "", ChatModel{})
	require.NoError(t, err)

	for _, c := range []struct{ name, bot, chat string }{
		{"another chat", w.bot.ID, other.ID},
		{"another Bot's main chat", w.bot.ID, mainBot.ID},
		{"a Bot that is not there", "bot_none", w.main.ID},
		{"no chat", w.bot.ID, ""},
	} {
		_, err := w.s.RequestConnection(ctx, c.bot, c.chat, "google:team", "")
		require.ErrorIs(t, err, ErrForbidden, c.name)
	}

	// The main chat marked as a Lark group's after it was made main is refused as well.
	_, err = w.db.ExecContext(ctx, `UPDATE chats SET channel_group = 1 WHERE id = ?`, w.main.ID)
	require.NoError(t, err)

	_, err = w.s.RequestConnection(ctx, w.bot.ID, w.main.ID, "google:team", "")
	require.ErrorIs(t, err, ErrForbidden, "a Lark chat")

	main, err := w.s.IsMainChat(ctx, w.bot.ID, w.main.ID)
	require.NoError(t, err)
	assert.False(t, main)

	assert.Empty(t, w.requests(t))
}

// Scenario: two concurrent asks and a repeated one for the same Connection make one pending request, and each is
// answered with it.
func TestRepeatedAndConcurrentAsksMakeOneCard(t *testing.T) {
	t.Parallel()

	w := newRequestWorld(t)

	const asks = 8

	got := make([]ConnectionRequest, asks)

	var wg sync.WaitGroup

	for i := range asks {
		wg.Go(func() { got[i] = w.request(t, "google:team") })
	}

	wg.Wait()

	for _, r := range got {
		assert.Equal(t, got[0].ID, r.ID)
		assert.Equal(t, RequestPending, r.State)
	}

	again := w.request(t, "google:team")
	assert.Equal(t, got[0].ID, again.ID)

	all := w.requests(t)
	require.Len(t, all, 1)
	assert.Equal(t, "to read the team's calendar", all[0].Reason)
	assert.Equal(t, w.bot.ID, all[0].Bot)
	assert.Equal(t, w.main.ID, all[0].Chat)
}

// Scenario: the owner declines. The Bot's settings stay as they were; the chat has a line, which pi is given once;
// the same answer again is the same request and Always after it a conflict. Woken by that line, pi asking again gets
// "declined" and makes no card (coordinator, 2026-11-21); after the owner writes again, a new turn asks with a new
// card.
func TestADeclinedRequestLeavesTheBotAsItWasAndWakesPiOnce(t *testing.T) {
	t.Parallel()

	w := newRequestWorld(t)
	ctx := t.Context()

	_, err := w.s.AddChatMessage(ctx, w.main.ID, ChatOwner, "check the team's calendar")
	require.NoError(t, err)
	require.NoError(t, w.s.MarkChatTurn(ctx, w.main.ID, "pi-1"))
	w.wakes(t)

	asked := w.request(t, "google:team")

	decided, err := w.s.DecideConnectionRequest(ctx, asked.ID, false)
	require.NoError(t, err)
	assert.Equal(t, RequestDeclined, decided.State)
	assert.False(t, decided.DecidedAt.IsZero())

	bot, err := w.s.GetBot(ctx, w.bot.ID)
	require.NoError(t, err)
	assert.Equal(t, w.bot.Connections, bot.Connections)

	wakes := w.wakes(t)
	require.Len(t, wakes, 1)
	assert.True(t, strings.HasPrefix(wakes[0], "Connection declined: "), wakes[0])
	assert.Contains(t, wakes[0], "google:team")
	assert.Empty(t, w.wakes(t), "given once")

	same, err := w.s.DecideConnectionRequest(ctx, asked.ID, false)
	require.NoError(t, err)
	assert.Equal(t, decided, same)

	_, err = w.s.DecideConnectionRequest(ctx, asked.ID, true)
	require.ErrorIs(t, err, ErrConflict)

	// The line wakes pi: a turn the owner did not start.
	require.NoError(t, w.s.SettleChatTurn(ctx, w.main.ID, "pi-1"))
	require.NoError(t, w.s.MarkChatTurn(ctx, w.main.ID, "pi-2"))

	woken := w.request(t, "google:team")
	assert.Equal(t, asked.ID, woken.ID)
	assert.Equal(t, RequestDeclined, woken.State)
	assert.Len(t, w.requests(t), 1, "the turn the answer wakes makes no second card")

	// The owner writes again: his turn asks anew.
	require.NoError(t, w.s.SettleChatTurn(ctx, w.main.ID, "pi-2"))
	_, err = w.s.AddChatMessage(ctx, w.main.ID, ChatOwner, "try again, I'll allow it")
	require.NoError(t, err)
	require.NoError(t, w.s.MarkChatTurn(ctx, w.main.ID, "pi-3"))

	fresh := w.request(t, "google:team")
	assert.NotEqual(t, asked.ID, fresh.ID)
	assert.Equal(t, RequestPending, fresh.State)
}

// Scenario: the owner answers Always. Exactly that use is appended to the Bot's list, once, and no other setting
// changes; the chat has a line pi is given once; the Bot may use the Connection, and its ask now is answered
// granted. Taken back on the Bot's page, the next use is refused.
func TestAlwaysAppendsExactlyThatUseAndARevokeTakesItBack(t *testing.T) {
	t.Parallel()

	w := newRequestWorld(t)
	ctx := t.Context()

	asked := w.request(t, "google:team")

	decided, err := w.s.DecideConnectionRequest(ctx, asked.ID, true)
	require.NoError(t, err)
	assert.Equal(t, RequestGranted, decided.State)

	bot, err := w.s.GetBot(ctx, w.bot.ID)
	require.NoError(t, err)
	assert.Equal(t, AllowList{Limited: true, Names: []string{"google:work", "google:team"}}, bot.Connections)

	before, after := w.bot, bot
	before.Connections, after.Connections = AllowList{}, AllowList{}
	before.UpdatedAt, after.UpdatedAt = after.UpdatedAt, before.UpdatedAt
	before.MainChat = w.main.ID
	assert.Equal(t, before, after, "no other setting changes")

	team, err := w.s.ConnectionOf(ctx, "google", "team")
	require.NoError(t, err)
	assert.True(t, bot.MayUse(team))

	wakes := w.wakes(t)
	require.Len(t, wakes, 1)
	assert.True(t, strings.HasPrefix(wakes[0], "Connection granted: "), wakes[0])

	same, err := w.s.DecideConnectionRequest(ctx, asked.ID, true)
	require.NoError(t, err)
	assert.Equal(t, decided, same)

	_, err = w.s.DecideConnectionRequest(ctx, asked.ID, false)
	require.ErrorIs(t, err, ErrConflict)

	again := w.request(t, "google:team")
	assert.Equal(t, asked.ID, again.ID)
	assert.Equal(t, RequestGranted, again.State)

	bot, err = w.s.GetBot(ctx, w.bot.ID)
	require.NoError(t, err)
	assert.Len(t, bot.Connections.Names, 2, "asked and answered again, the use is there once")

	bot.Connections = AllowList{Limited: true, Names: []string{"google:work"}}
	bot, err = w.s.PutBot(ctx, bot)
	require.NoError(t, err)
	assert.False(t, bot.MayUse(team), "the Bot's page takes it back")
}

// Scenario: what changed between the ask and Always makes the request stale and grants nothing: the Connection
// switched off for chats or removed, the Bot's list made unlimited, the main chat moved. The chat is told; Decline
// on a stale request returns it as it is.
func TestAlwaysGrantsNothingWhenTheAskNoLongerHolds(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name   string
		change func(t *testing.T, w requestWorld)
		why    string
	}{
		{"chats switched off", func(t *testing.T, w requestWorld) {
			_, err := w.s.SetConnectionChats(t.Context(), "google", "team", false)
			require.NoError(t, err)
		}, "switched off for chats"},
		{"connection removed", func(t *testing.T, w requestWorld) {
			_, err := w.s.RemoveConnection(t.Context(), "google", "team")
			require.NoError(t, err)
		}, "was removed"},
		{"list made unlimited", func(t *testing.T, w requestWorld) {
			b := w.bot
			b.Connections = AllowList{}
			_, err := w.s.PutBot(t.Context(), b)
			require.NoError(t, err)
		}, "every connection already"},
		{"main chat moved", func(t *testing.T, w requestWorld) {
			_, err := w.s.StartMainChat(t.Context(), "", w.bot.ID, "", ChatModel{})
			require.NoError(t, err)
		}, "no longer the bot's main chat"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			w := newRequestWorld(t)
			ctx := t.Context()

			asked := w.request(t, "google:team")
			c.change(t, w)

			before, err := w.s.GetBot(ctx, w.bot.ID)
			require.NoError(t, err)

			decided, err := w.s.DecideConnectionRequest(ctx, asked.ID, true)
			require.NoError(t, err)
			assert.Equal(t, RequestStale, decided.State)
			assert.Contains(t, decided.Why, c.why)

			after, err := w.s.GetBot(ctx, w.bot.ID)
			require.NoError(t, err)
			assert.Equal(t, before.Connections, after.Connections, "nothing granted")

			wakes := w.wakes(t)
			require.Len(t, wakes, 1)
			assert.True(t, strings.HasPrefix(wakes[0], "Connection not granted: "), wakes[0])

			declined, err := w.s.DecideConnectionRequest(ctx, asked.ID, false)
			require.NoError(t, err)
			assert.Equal(t, RequestStale, declined.State)

			pending, err := w.s.ListConnectionRequests(ctx, ConnectionRequestFilter{State: RequestPending})
			require.NoError(t, err)
			assert.Empty(t, pending)
		})
	}
}

// Scenario: Decline needs no check: a request whose Connection went is still cleared.
func TestDeclineClearsARequestThatNoLongerHolds(t *testing.T) {
	t.Parallel()

	w := newRequestWorld(t)
	ctx := t.Context()

	asked := w.request(t, "google:team")
	_, err := w.s.RemoveConnection(ctx, "google", "team")
	require.NoError(t, err)

	decided, err := w.s.DecideConnectionRequest(ctx, asked.ID, false)
	require.NoError(t, err)
	assert.Equal(t, RequestDeclined, decided.State)
}

// Scenario: a request goes with its chat, and so with a Bot removed with its chats: nothing is left to grant.
func TestARequestGoesWithItsChatAndItsBot(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	w := newRequestWorld(t)
	asked := w.request(t, "google:team")

	_, err := w.s.DeleteChat(ctx, w.main.ID)
	require.NoError(t, err)

	_, err = w.s.DecideConnectionRequest(ctx, asked.ID, true)
	require.ErrorIs(t, err, ErrNotFound)

	w = newRequestWorld(t)
	asked = w.request(t, "google:team")

	_, err = w.s.ForceDeleteBot(ctx, w.bot.ID)
	require.NoError(t, err)

	_, err = w.s.GetConnectionRequest(ctx, asked.ID)
	require.ErrorIs(t, err, ErrNotFound)
}

// Scenario: a pending request's chat is on the first page of chats, so the sidebar's row of its Bot says Needs you,
// whatever the chat's age.
func TestAChatARequestWaitsInIsListed(t *testing.T) {
	t.Parallel()

	w := newRequestWorld(t)
	ctx := t.Context()

	asked := w.request(t, "google:team")

	// The main chat moves: the request's chat is an older one now.
	_, err := w.s.StartMainChat(ctx, "", w.bot.ID, "", ChatModel{})
	require.NoError(t, err)

	for range 3 {
		_, err := w.s.CreateChatWithID(ctx, "", "", "newer", ChatModel{})
		require.NoError(t, err)
	}

	chats, err := w.s.ListChats(ctx, "", 1, 0)
	require.NoError(t, err)

	ids := make([]string, 0, len(chats))
	for _, c := range chats {
		ids = append(ids, c.ID)
	}

	assert.Contains(t, ids, asked.Chat)
}
