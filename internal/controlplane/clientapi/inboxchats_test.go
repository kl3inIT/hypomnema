package clientapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/inbox"
	"github.com/kl3inIT/hypomnema/internal/controlplane/messaging"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/workspacefs"
)

// The owner's accounts in these tests, every name and id made up: a Zalo, a WhatsApp, his own Lark sign-in, and a
// Lark app's bot, which reads none of his chats.
var keptChats = map[string][]messaging.Chat{
	"zalo:main": {
		{ID: "1001", Kind: "dm", Name: "Lan Anh"},
		{ID: "2002", Kind: "group", Name: "Team Alpha"},
		{ID: "3003", Kind: "dm", Name: "Zalo Pay"},
		{ID: "4004", Kind: "dm", Name: "Shop ABC OA"},
	},
	"whatsapp:me": {
		{ID: "8490000001@s.whatsapp.net", Kind: "dm", Name: "Kai"},
		{ID: "120363000000000001@g.us", Kind: "group", Name: "Family"},
		{ID: "status@broadcast", Kind: "broadcast", Name: ""},
	},
}

// larkChatsAnswer is Lark's list of the owner's chats (GET /open-apis/im/v1/chats), on two pages.
func larkChatsAnswer(t *testing.T) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/open-apis/im/v1/chats", r.URL.Path)
		assert.Equal(t, "Bearer lark-me-token", r.Header.Get("Authorization"))

		page := `{"code":0,"data":{"items":[` +
			`{"chat_id":"oc_aaa","name":"Newsletter Editor","chat_mode":"p2p","p2p_target_type":"user"},` +
			`{"chat_id":"oc_bbb","name":"Design review","chat_mode":"group"}],"has_more":true,"page_token":"p2"}}`
		if r.URL.Query().Get("page_token") == "p2" {
			page = `{"code":0,"data":{"items":[` +
				`{"chat_id":"oc_ccc","name":"File Desk","chat_mode":"p2p","p2p_target_type":"app"},` +
				`{"chat_id":"oc_ddd","name":"Paper Trail","chat_mode":"bot"}],` +
				`"has_more":false}}`
		}

		_, _ = fmt.Fprint(rw, page)
	}))
	t.Cleanup(srv.Close)

	return srv
}

// botFolder plays the Bots' folders: the files of each Bot by its id.
type botFolder map[string]map[string]string

func (f botFolder) Root(bot string) string { return bot }

func (f botFolder) Ask(_ context.Context, bot string, req workspacefs.Request) (workspacefs.Result, error) {
	content, ok := f[bot][req.Path]
	if req.Op != workspacefs.OpRead || !ok {
		return workspacefs.Result{Error: &workspacefs.Error{Code: workspacefs.CodeNotFound, Message: "no such file"}}, nil
	}

	return workspacefs.Result{File: &workspacefs.File{Path: req.Path, Content: content}}, nil
}

// newSecretaryChatsWorld is an Inbox world whose owner has those accounts, and whose main Bot's folder holds files.
func newSecretaryChatsWorld(t *testing.T, files botFolder) *inboxWorld {
	t.Helper()

	return newSecretaryChatsWorldWith(t, files, &atomic.Bool{})
}

// newSecretaryChatsWorldWith is that world, whose Zalo session is not open while closed holds.
func newSecretaryChatsWorldWith(t *testing.T, files botFolder, closed *atomic.Bool) *inboxWorld {
	t.Helper()

	ctx := t.Context()
	w := newInboxWorld(t)

	for _, c := range []orchestration.Connection{
		{Integration: "zalo", Label: "main", Account: "Alex Demo", Secret: "test-zalo"},
		{Integration: "whatsapp", Label: "me", Account: "+84 90 000 0000", Secret: "test-whatsapp"},
		{Integration: "lark", Label: "me", Secret: "test-lark", AppLabel: "work"},
		{Integration: "lark", Label: "helper", Secret: "test-lark-helper"},
	} {
		_, err := w.svc.SaveServiceConnection(ctx, c)
		require.NoError(t, err)
	}

	lark := larkChatsAnswer(t)
	w.srv.WithInboxChats(inbox.NewChats(inbox.Config{
		Chats: func(_ context.Context, integration, label string, limit int) ([]messaging.Chat, error) {
			assert.Equal(t, messaging.MaxChats, limit, "every chat of the account")

			if integration == "zalo" && closed.Load() {
				return nil, fmt.Errorf("%w: the session of zalo:%s is not open; link it again",
					orchestration.ErrUnavailable, label)
			}

			return keptChats[integration+":"+label], nil
		},
		ChatReads: w.svc.Inbox().ChatReads,
		LarkToken: func(_ context.Context, label string) (string, error) { return "lark-" + label + "-token", nil },
		LarkURL:   lark.URL, Client: lark.Client(),
	}))
	w.srv.WithWorkspaces(nil, files)

	return w
}

// chatsOf lists an account's chats as the owner, with filters.
func (w *inboxWorld) chatsOf(t *testing.T, account string, params openapi.ListInboxChatsParams) openapi.InboxChatList {
	t.Helper()

	integration, label, _ := strings.Cut(account, ":")
	resp, err := w.owner.ListInboxChatsWithResponse(t.Context(), integration, label, &params)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode(), string(resp.Body))

	return *resp.JSON200
}

// readsByID is each chat's id with whether the secretary reads it and why.
func readsByID(list openapi.InboxChatList) map[string]string {
	out := map[string]string{}
	for _, c := range list.Data {
		state := "skipped"
		if c.Reads {
			state = "read"
		}

		out[c.Id] = fmt.Sprintf("%s %s by %s", c.Kind, state, c.SetBy)
	}

	return out
}

// Scenario: issue #1270, the defaults (owner, 2026-11-27: "the same for every source"). Every source of the owner's,
// his Zalo, his WhatsApp and his own Lark sign-in, reads his direct chats and groups and skips a bot's or an
// official account's chat (today's dropChats defaults: Zalo's own accounts, OA, newsletters, broadcast lists),
// counted per source; a Lark app's bot is no source of his.
func TestTheSecretaryReadsDirectChatsAndGroupsAndSkipsBotsOnEverySource(t *testing.T) {
	t.Parallel()

	w := newSecretaryChatsWorld(t, botFolder{})

	resp, err := w.owner.ListInboxChatSourcesWithResponse(t.Context())
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode(), string(resp.Body))

	counts := map[string]openapi.InboxChatCounts{}
	for _, s := range resp.JSON200.Data {
		require.Nil(t, s.Problem, "%s:%s", s.Integration, s.Label)
		counts[s.Integration+":"+s.Label] = *s.Counts
	}

	assert.Equal(t, map[string]openapi.InboxChatCounts{
		"zalo:main":   {All: 4, Read: 2, Skipped: 2, Direct: 1, Group: 1, Bot: 2},
		"whatsapp:me": {All: 3, Read: 2, Skipped: 1, Direct: 1, Group: 1, Bot: 1},
		"lark:me":     {All: 4, Read: 2, Skipped: 2, Direct: 1, Group: 1, Bot: 2},
	}, counts, "lark:helper is an app's bot, not his")
	assert.Equal(t, openapi.InboxChatDefaults{
		Direct: true, Group: true, Bot: false,
		BotPatterns: []string{`^Zalo `, `\bOA\b`, `official account`, `quảng cáo`, `newsletter`, `@broadcast$`},
	}, resp.JSON200.Defaults)

	assert.Equal(t, map[string]string{
		"1001": "direct read by default", "2002": "group read by default",
		"3003": "bot skipped by default", "4004": "bot skipped by default",
	}, readsByID(w.chatsOf(t, "zalo:main", openapi.ListInboxChatsParams{})))
	assert.Equal(t, map[string]string{
		"8490000001@s.whatsapp.net": "direct read by default", "120363000000000001@g.us": "group read by default",
		"status@broadcast": "bot skipped by default",
	}, readsByID(w.chatsOf(t, "whatsapp:me", openapi.ListInboxChatsParams{})))
	assert.Equal(t, map[string]string{
		"oc_aaa": "direct read by default", "oc_bbb": "group read by default",
		"oc_ccc": "bot skipped by default", "oc_ddd": "bot skipped by default",
	}, readsByID(w.chatsOf(t, "lark:me", openapi.ListInboxChatsParams{})))

	on, err := w.owner.SetInboxChatWithResponse(t.Context(), "lark", "me", "oc_ccc", openapi.InboxChatChoice{Reads: true})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, on.StatusCode(), string(on.Body))
	bots := openapi.InboxChatBot
	list := w.chatsOf(t, "lark:me", openapi.ListInboxChatsParams{Kind: &bots})
	assert.Equal(t, map[string]string{"oc_ccc": "bot read by owner", "oc_ddd": "bot skipped by default"}, readsByID(list))
	assert.Equal(t, openapi.InboxChatCounts{All: 4, Direct: 1, Group: 1, Bot: 2, Read: 3, Skipped: 1}, list.Counts)

	for name, c := range map[string]*openapi.ClientWithResponses{
		"chat bot": w.chatBot, "routine bot": w.routineBot, "coordinator": w.coordinator,
	} {
		got, err := c.ListInboxChatSourcesWithResponse(t.Context())
		require.NoError(t, err)
		assert.Equal(t, http.StatusForbidden, got.StatusCode(), "%s: the owner's alone", name)
	}

	helper, err := w.owner.ListInboxChatsWithResponse(t.Context(), "lark", "helper", &openapi.ListInboxChatsParams{})
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, helper.StatusCode(), string(helper.Body))
}

// Scenario: issue #1270, the owner's screen. He finds a chat by a part of its name, in any case, switches it off,
// and it says it is skipped by him; the state and kind filters and the counts follow, and switching a bot's chat on
// reads it. Only he sets one.
func TestTheOwnerFindsAChatAndSwitchesItOff(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newSecretaryChatsWorld(t, botFolder{})

	found := w.chatsOf(t, "zalo:main", openapi.ListInboxChatsParams{Q: ptr("lan")})
	require.Len(t, found.Data, 1)
	assert.Equal(t, "Lan Anh", found.Data[0].Name)
	assert.Equal(t, 1, found.Matched)
	assert.Equal(t, 4, found.Counts.All, "the counts are the account's")
	assert.Equal(t, ptr(openapi.InboxChatCounts{All: 1, Direct: 1, Read: 1}), found.SearchCounts)

	bots := openapi.InboxChatBot
	narrowed := w.chatsOf(t, "zalo:main", openapi.ListInboxChatsParams{Q: ptr("00"), Kind: &bots, Limit: ptr(1)})
	require.Len(t, narrowed.Data, 1)
	assert.Equal(t, 2, narrowed.Matched)
	assert.Equal(t, ptr(openapi.InboxChatCounts{All: 4, Direct: 1, Group: 1, Bot: 2, Read: 2, Skipped: 2}),
		narrowed.SearchCounts, "search counts are taken before the kind filter and limit")

	for name, c := range map[string]*openapi.ClientWithResponses{"chat bot": w.chatBot, "routine bot": w.routineBot} {
		got, err := c.SetInboxChatWithResponse(ctx, "zalo", "main", "1001", openapi.InboxChatChoice{Reads: false})
		require.NoError(t, err)
		assert.Equal(t, http.StatusForbidden, got.StatusCode(), name)
	}

	set, err := w.owner.SetInboxChatWithResponse(ctx, "zalo", "main", "1001", openapi.InboxChatChoice{Reads: false})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, set.StatusCode(), string(set.Body))
	assert.False(t, set.JSON200.Reads)

	found = w.chatsOf(t, "zalo:main", openapi.ListInboxChatsParams{Q: ptr("LAN")})
	require.Len(t, found.Data, 1)
	assert.False(t, found.Data[0].Reads)
	assert.Equal(t, openapi.InboxChatByOwner, found.Data[0].SetBy)
	require.NotNil(t, found.Data[0].SetAt)
	assert.Equal(t, set.JSON200.SetAt, *found.Data[0].SetAt)

	on, err := w.owner.SetInboxChatWithResponse(ctx, "zalo", "main", "4004", openapi.InboxChatChoice{Reads: true})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, on.StatusCode(), string(on.Body))

	skipped := openapi.InboxChatSkipped
	assert.Equal(t, map[string]string{
		"1001": "direct skipped by owner", "3003": "bot skipped by default",
	}, readsByID(w.chatsOf(t, "zalo:main", openapi.ListInboxChatsParams{State: &skipped})))

	assert.Equal(t, map[string]string{
		"3003": "bot skipped by default", "4004": "bot read by owner",
	}, readsByID(w.chatsOf(t, "zalo:main", openapi.ListInboxChatsParams{Kind: &bots})))
	assert.Equal(t, openapi.InboxChatCounts{All: 4, Read: 2, Skipped: 2, Direct: 1, Group: 1, Bot: 2},
		w.chatsOf(t, "zalo:main", openapi.ListInboxChatsParams{}).Counts)

	other, err := w.owner.SetInboxChatWithResponse(ctx, "zalo", "work", "1001", openapi.InboxChatChoice{})
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, other.StatusCode(), "an account he does not have")
}

// Scenario: issue #1270, moving today's ids. The main Bot's thu-ky/filters.json drops one chat by its exact id beside
// the default patterns: the move switches that chat off as the owner's choice, names what it did not move, and run
// again changes nothing, even after he switched the chat back on.
func TestTheMoveOfADropChatsIDIsTheOwnersChoiceAndRunsOnce(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	filters, err := json.Marshal(map[string]any{
		"ownerNames": []string{"Đạt"},
		"dropChats": []string{`^Zalo `, `\bOA\b`, `official account`, `quảng cáo`, `newsletter`, `@broadcast$`,
			`^2002$`, `^8490000001@s\.whatsapp\.net$`, `^9999$`, `spam.*`},
	})
	require.NoError(t, err)

	w := newSecretaryChatsWorld(t, botFolder{
		orchestration.DefaultBotID: {"thu-ky/filters.json": string(filters)},
	})

	first, err := w.owner.MoveInboxDropChatsWithResponse(ctx, openapi.InboxChatMove{})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, first.StatusCode(), string(first.Body))
	assert.Equal(t, openapi.InboxChatMoved{
		Moved: []openapi.InboxChatRef{
			{Integration: "zalo", Label: "main", Chat: "2002"},
			{Integration: "whatsapp", Label: "me", Chat: "8490000001@s.whatsapp.net"},
		},
		Kept: []openapi.InboxChatRef{}, Unknown: []string{"9999"}, Left: []string{"spam.*"},
		Defaults: []string{`^Zalo `, `\bOA\b`, `official account`, `quảng cáo`, `newsletter`, `@broadcast$`},
		NotRead:  []string{},
	}, *first.JSON200)
	assert.Equal(t, "group skipped by owner",
		readsByID(w.chatsOf(t, "zalo:main", openapi.ListInboxChatsParams{}))["2002"])

	on, err := w.owner.SetInboxChatWithResponse(ctx, "zalo", "main", "2002", openapi.InboxChatChoice{Reads: true})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, on.StatusCode(), string(on.Body))

	again, err := w.owner.MoveInboxDropChatsWithResponse(ctx, openapi.InboxChatMove{})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, again.StatusCode(), string(again.Body))
	assert.Empty(t, again.JSON200.Moved, "nothing moves twice")
	assert.Len(t, again.JSON200.Kept, 2)
	assert.Equal(t, "group read by owner",
		readsByID(w.chatsOf(t, "zalo:main", openapi.ListInboxChatsParams{}))["2002"],
		"his choice made since stays")

	none, err := w.owner.MoveInboxDropChatsWithResponse(ctx, openapi.InboxChatMove{Bot: w.vmap.Name})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, none.StatusCode(), string(none.Body))
	assert.Empty(t, none.JSON200.Moved, "a Bot without the file moves nothing")

	bot, err := w.routineBot.MoveInboxDropChatsWithResponse(ctx, openapi.InboxChatMove{})
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, bot.StatusCode(), "the owner's alone")
}

// Review of #1270, round 1: the move is what keeps a chat the owner dropped by its id out of the new check, which
// reads dropChats no more. While one of his accounts cannot be listed (its session not open), an id it may hold is
// not stored: the move fails and names the account and the ids left, so that setup.sh stops before it sets the new
// check. Once the account opens again, the move stores them.
func TestTheMoveFailsWhileAnAccountCannotBeListed(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	filters, err := json.Marshal(map[string]any{"dropChats": []string{`\bOA\b`, `^2002$`}})
	require.NoError(t, err)

	closed := &atomic.Bool{}
	closed.Store(true)
	w := newSecretaryChatsWorldWith(t, botFolder{
		orchestration.DefaultBotID: {"thu-ky/filters.json": string(filters)},
	}, closed)

	failed, err := w.owner.MoveInboxDropChatsWithResponse(ctx, openapi.InboxChatMove{})
	require.NoError(t, err)
	require.Equal(t, http.StatusServiceUnavailable, failed.StatusCode(), string(failed.Body))
	assert.Contains(t, string(failed.Body), "zalo:main")
	assert.Contains(t, string(failed.Body), "2002")

	closed.Store(false)
	assert.Equal(t, "group read by default",
		readsByID(w.chatsOf(t, "zalo:main", openapi.ListInboxChatsParams{}))["2002"], "not stored meanwhile")

	moved, err := w.owner.MoveInboxDropChatsWithResponse(ctx, openapi.InboxChatMove{})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, moved.StatusCode(), string(moved.Body))
	assert.Equal(t, []openapi.InboxChatRef{{Integration: "zalo", Label: "main", Chat: "2002"}}, moved.JSON200.Moved)
}
