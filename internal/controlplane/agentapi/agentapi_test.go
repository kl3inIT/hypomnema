package agentapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/agentapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/routinestore"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

type world struct {
	t   *testing.T
	svc *orchestration.Service
	srv *httptest.Server
	// asked are the Connections a token was fetched for, in order.
	asked []string
	logs  *bytes.Buffer
	// browser stands for the shared browser.
	browser *sharedBrowser
	// whatsapp stands for the owner's WhatsApp the control plane keeps.
	whatsapp *ownWhatsApp
	// judge stands for the service proxy's decision on what the owner approves.
	judge *ownerJudge
	// api is the server, for a test to give it more.
	api *agentapi.Server
}

// newWorld is a control plane with two accounts of a service that does not go through the service proxy of the owner's: work,
// and personal.
func newWorld(t *testing.T) *world {
	t.Helper()

	return newWorldWriting(t, 0)
}

// newWorldWriting is newWorld on a server that ends an answer not written within writeTimeout, as the control
// plane's does (platform/http.go); zero is none.
func newWorldWriting(t *testing.T, writeTimeout time.Duration) *world {
	t.Helper()

	return newWorldWith(t, writeTimeout, nil)
}

// newWorldWith is newWorldWriting with more given to its agent API by more, when it is set.
func newWorldWith(t *testing.T, writeTimeout time.Duration, more func(*agentapi.Server)) *world {
	t.Helper()

	return newWorldOn(t, platform.SystemClock{}, writeTimeout, more)
}

// newWorldAt is newWorld whose control plane reads the time from clock, for a test where the day decides.
func newWorldAt(t *testing.T, clock platform.Clock) *world {
	t.Helper()

	return newWorldOn(t, clock, 0, nil)
}

// newWorldOn is newWorldWith whose control plane reads the time from clock.
func newWorldOn(t *testing.T, clock platform.Clock, writeTimeout time.Duration, more func(*agentapi.Server)) *world {
	t.Helper()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	w := &world{t: t, svc: orchestration.New(db, clock, platform.NewID), logs: &bytes.Buffer{},
		browser: &sharedBrowser{}, whatsapp: &ownWhatsApp{}, judge: &ownerJudge{}}

	for _, label := range []string{"work", "personal"} {
		_, err := w.svc.SaveServiceConnection(t.Context(), orchestration.Connection{
			Integration: "notion", Label: label, Account: label + "@example.com", Secret: "conn-notion-" + label,
		})
		require.NoError(t, err)
	}

	mux := http.NewServeMux()
	api := agentapi.New(w.svc, func(_ context.Context, integration, label string) (agentapi.Token, error) {
		w.asked = append(w.asked, integration+":"+label)

		return agentapi.Token{AccessToken: "ya29.token-of-" + label, ExpiresAt: time.Unix(1_800_000_000, 0).UTC()}, nil
	}, slog.New(slog.NewTextHandler(w.logs, nil))).WithBrowser(w.browser).WithMessaging(w.whatsapp).
		WithJudge(w.judge)
	if more != nil {
		more(api)
	}

	api.Register(mux)

	w.srv = httptest.NewUnstartedServer(mux)
	w.srv.Config.WriteTimeout = writeTimeout
	w.srv.Start()
	t.Cleanup(w.srv.Close)

	return w
}

func (w *world) get(key, integration string) (int, []string) {
	w.t.Helper()

	status, body := w.call(http.MethodGet, agenttool.PathConnections+"?integration="+integration, key, nil)

	var list agenttool.ConnectionList
	if status == http.StatusOK {
		require.NoError(w.t, json.Unmarshal(body, &list))
	}

	labels := []string{}
	for _, c := range list.Data {
		labels = append(labels, c.Label)
	}

	return status, labels
}

func (w *world) token(key, label string) (int, string) {
	w.t.Helper()

	status, body := w.call(http.MethodPost, agenttool.PathToken, key,
		agenttool.TokenRequest{Integration: "notion", Label: label})

	var token agenttool.TokenResponse
	if status == http.StatusOK {
		require.NoError(w.t, json.Unmarshal(body, &token))
	}

	return status, token.AccessToken
}

func (w *world) call(method, path, key string, body any) (int, []byte) {
	w.t.Helper()

	var reader io.Reader

	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(w.t, err)

		reader = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(w.t.Context(), method, w.srv.URL+path, reader)
	require.NoError(w.t, err)

	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	resp, err := w.srv.Client().Do(req)
	require.NoError(w.t, err)

	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	require.NoError(w.t, err)

	return resp.StatusCode, raw
}

// Scenario: a coordinator's Run was given the work account, which the owner
// allowed for chats. Its worker, with its Dispatch's own key, sees that
// account and gets its token; the personal account, also allowed for chats
// but not given to this Run, is not listed and its token is refused. When
// the owner takes the work account back from the coordinator, the Run's
// worker loses it at its next call. Once the Dispatch is over the key gets
// nothing. What was handed out is logged with the Dispatch, never the token.
//
// A Run the owner created keeps what he gave it whatever the switch says;
// that is checked where a real Dispatch exists (the node API's tests). Here
// the key's Dispatch is unknown, which counts as a coordinator's.
func TestAWorkerGetsOnlyTheAccountsItsRunWasGivenAndStillHas(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	for _, label := range []string{"work", "personal"} {
		_, err := w.svc.SetConnectionChats(ctx, "notion", label, true)
		require.NoError(t, err)
	}

	key, err := w.svc.AddAgentSession(ctx, "dsp_1", []string{"notion:work"})
	require.NoError(t, err)

	status, labels := w.get(key, "notion")
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, []string{"work"}, labels)

	status, token := w.token(key, "work")
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, "ya29.token-of-work", token)

	status, token = w.token(key, "personal")
	assert.Equal(t, http.StatusForbidden, status)
	assert.Empty(t, token)

	status, _ = w.token(key, "nobody")
	assert.Equal(t, http.StatusForbidden, status, "an account that does not exist answers as one not given")
	assert.Equal(t, []string{"notion:work"}, w.asked, "no token was fetched for what was refused")

	assert.Contains(t, w.logs.String(), "dsp_1")
	assert.Contains(t, w.logs.String(), "notion:work")
	assert.NotContains(t, w.logs.String(), "ya29.")

	// The owner takes the account back from the coordinator: its Run's worker loses it too.
	_, err = w.svc.SetConnectionChats(ctx, "notion", "work", false)
	require.NoError(t, err)

	status, _ = w.token(key, "work")
	assert.Equal(t, http.StatusForbidden, status)

	status, labels = w.get(key, "notion")
	require.Equal(t, http.StatusOK, status)
	assert.Empty(t, labels)

	_, err = w.svc.SetConnectionChats(ctx, "notion", "work", true)
	require.NoError(t, err)

	status, _ = w.token(key, "work")
	require.Equal(t, http.StatusOK, status)

	require.NoError(t, w.svc.RevokeAgentSessions(ctx, "dsp_1"))

	status, _ = w.token(key, "work")
	assert.Equal(t, http.StatusUnauthorized, status)

	status, _ = w.get(key, "notion")
	assert.Equal(t, http.StatusUnauthorized, status)
}

// Scenario: the coordinator may use an account only once the owner allowed
// it for chats; the owner's own token may use every account. Without a
// credential, or with one of another kind, nothing is answered.
func TestTheCoordinatorGetsOnlyWhatTheOwnerAllowedForChats(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	_, pi, err := w.svc.AddToken(ctx, "pi", bearertoken.Coordinator)
	require.NoError(t, err)
	_, owner, err := w.svc.AddToken(ctx, "me", bearertoken.Owner)
	require.NoError(t, err)

	status, labels := w.get(pi, "notion")
	require.Equal(t, http.StatusOK, status)
	assert.Empty(t, labels, "nothing is allowed until the owner says so")

	status, _ = w.token(pi, "work")
	assert.Equal(t, http.StatusForbidden, status)

	_, err = w.svc.SetConnectionChats(ctx, "notion", "work", true)
	require.NoError(t, err)

	status, labels = w.get(pi, "notion")
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, []string{"work"}, labels)

	status, token := w.token(pi, "work")
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, "ya29.token-of-work", token)

	status, _ = w.token(pi, "personal")
	assert.Equal(t, http.StatusForbidden, status)

	status, labels = w.get(owner, "notion")
	require.Equal(t, http.StatusOK, status)
	// Two accounts connected within the same moment are listed by label, so their order is not this test's claim.
	assert.ElementsMatch(t, []string{"work", "personal"}, labels)

	_, err = w.svc.SetConnectionChats(ctx, "notion", "work", false)
	require.NoError(t, err)

	status, _ = w.token(pi, "work")
	assert.Equal(t, http.StatusForbidden, status, "switched off, the next call is refused")

	gateway, err := bearertoken.Generate(bearertoken.Gateway)
	require.NoError(t, err)

	for name, key := range map[string]string{"no credential": "", "not a token": "hello", "a gateway key": gateway} {
		status, _ := w.token(key, "work")
		assert.Equal(t, http.StatusUnauthorized, status, name)
	}
}

// Scenario: a Bot with a list of Connections (issue #169). Its pi's token and
// its Routine's script's token see and get only the accounts on its list,
// among those the owner allowed for chats; the Bot coordinator, without a
// list, gets every one allowed. Taken off the list, the next call is refused.
func TestABotGetsOnlyTheAccountsOnItsList(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	_, _, err := w.svc.AddToken(ctx, "pi", bearertoken.Coordinator)
	require.NoError(t, err)

	for _, label := range []string{"work", "personal"} {
		_, err := w.svc.SetConnectionChats(ctx, "notion", label, true)
		require.NoError(t, err)
	}

	limited := orchestration.AllowList{Limited: true, Names: []string{"notion:work"}}
	vmapBot, err := w.svc.PutBot(ctx, orchestration.Bot{Name: "vmap", Connections: limited})
	require.NoError(t, err)

	chat, err := w.svc.CreateChat(ctx, vmapBot.ID, "")
	require.NoError(t, err)

	vmap, err := w.svc.AddBotToken(ctx, "pi", chat.ID, vmapBot.ID)
	require.NoError(t, err)

	mine, err := w.svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	coordinator, err := w.svc.AddBotToken(ctx, "pi", mine.ID, orchestration.DefaultBotID)
	require.NoError(t, err)

	routine, err := w.svc.Routines().PutRoutine(ctx, routinestore.RoutineSpec{
		Bot: vmapBot.ID, Name: "sync", Kind: routinestore.RoutineScript, Script: "sync.sh", ScriptText: "#!/bin/sh\n",
		EveryMinutes: 60,
	}, routinestore.RoutineAuthor{})
	require.NoError(t, err)

	_, err = w.svc.RunRoutineNow(ctx, routine.ID) // a script's token is its live run's (#1023)
	require.NoError(t, err)
	run, err := w.svc.AddRoutineToken(ctx, "pi", routine.ID)
	require.NoError(t, err)

	for name, key := range map[string]string{"its pi": vmap, "its routine's script": run} {
		status, labels := w.get(key, "notion")
		require.Equal(t, http.StatusOK, status, name)
		assert.Equal(t, []string{"work"}, labels, name)

		status, _ = w.token(key, "personal")
		assert.Equal(t, http.StatusForbidden, status, name)

		status, token := w.token(key, "work")
		require.Equal(t, http.StatusOK, status, name)
		assert.Equal(t, "ya29.token-of-work", token, name)
	}

	status, labels := w.get(coordinator, "notion")
	require.Equal(t, http.StatusOK, status)
	assert.ElementsMatch(t, []string{"work", "personal"}, labels, "no list: every one allowed for chats")

	_, err = w.svc.PutBot(ctx, orchestration.Bot{
		ID: vmapBot.ID, Name: "vmap", Connections: orchestration.AllowList{Limited: true},
	})
	require.NoError(t, err)

	status, _ = w.token(vmap, "work")
	assert.Equal(t, http.StatusForbidden, status, "taken off the list, the next call is refused")
}

// Scenario: two Lark accounts, each an app of its own (issue #311): the
// owner's "work" and a reports app. Lark's tool is told each account's app
// with it, so that it runs as the app of the account it acts as.
func TestEachLarkAccountIsListedWithItsOwnApp(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	_, owner, err := w.svc.AddToken(ctx, "me", bearertoken.Owner)
	require.NoError(t, err)

	list := func() agenttool.ConnectionList {
		status, body := w.call(http.MethodGet, agenttool.PathConnections+"?integration=lark", owner, nil)
		require.Equal(t, http.StatusOK, status)

		var out agenttool.ConnectionList
		require.NoError(t, json.Unmarshal(body, &out))

		return out
	}

	//nolint:gosec // G101: the name of a vault secret, not a credential
	_, err = w.svc.SaveServiceConnection(ctx, orchestration.Connection{
		Integration: "lark", Label: "work", Account: "Hypomnema", Secret: "conn-lark-work", AppID: "cli_work",
	})
	require.NoError(t, err)

	assert.Equal(t, agenttool.ConnectionList{
		Data: []agenttool.Connection{{Label: "work", Account: "Hypomnema", ClientID: "cli_work"}},
	}, list())

	_, err = w.svc.SaveServiceConnection(ctx, orchestration.Connection{
		Integration: "lark", Label: "report", Account: "Superset Report", Secret: "conn-lark-report",
		AppID: "cli_report",
	})
	require.NoError(t, err)

	assert.ElementsMatch(t, []agenttool.Connection{
		{Label: "work", Account: "Hypomnema", ClientID: "cli_work"},
		{Label: "report", Account: "Superset Report", ClientID: "cli_report"},
	}, list().Data)
}

// Scenario (issues #468 and #618): the owner connected his Lark app as
// "work" and signed in to Lark as himself through it as "me", and switched
// both on for chats. "me" acts as him, so only his own token and the pi of a
// Bot that may use it, on his own turn, get it: a worker given it, a
// coordinator token, a Routine's script of the main Bot and a Bot whose list
// leaves it out never do, whatever the switches say. A Bot's pi gets it while
// the chat's last message is the owner's, and loses it at the next call once
// someone else spoke.
func TestTheOwnersOwnLarkSignInIsABotsOnHisTurn(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	//nolint:gosec // G101: the names of vault secrets, not credentials
	for _, c := range []orchestration.Connection{
		{Integration: "lark", Label: "work", Account: "Hypomnema", Secret: "conn-lark-work", AppID: "cli_work"},
		{
			Integration: "lark", Label: "me", Account: "The Owner", Secret: "conn-lark-me", AppID: "cli_work",
			AppLabel: "work",
		},
	} {
		_, err := w.svc.SaveServiceConnection(ctx, c)
		require.NoError(t, err)
		_, err = w.svc.SetConnectionChats(ctx, "lark", c.Label, true)
		require.NoError(t, err)
	}

	list := func(key string) []agenttool.Connection {
		t.Helper()

		status, body := w.call(http.MethodGet, agenttool.PathConnections+"?integration=lark", key, nil)
		require.Equal(t, http.StatusOK, status)

		var out agenttool.ConnectionList
		require.NoError(t, json.Unmarshal(body, &out))

		return out.Data
	}
	labels := func(key string) []string {
		t.Helper()

		out := []string{}
		for _, c := range list(key) {
			out = append(out, c.Label)
		}

		return out
	}

	_, owner, err := w.svc.AddToken(ctx, "me", bearertoken.Owner)
	require.NoError(t, err)
	_, coordinator, err := w.svc.AddToken(ctx, "pi", bearertoken.Coordinator)
	require.NoError(t, err)

	assert.ElementsMatch(t, []agenttool.Connection{
		{Label: "work", Account: "Hypomnema", ClientID: "cli_work"},
		{Label: "me", Account: "The Owner", ClientID: "cli_work", User: true},
	}, list(owner), "the owner's token gets his own sign-in, told as a user's account with its app")

	worker, err := w.svc.AddAgentSession(ctx, "dsp_1", []string{"lark:work", "lark:me"})
	require.NoError(t, err)
	assert.Equal(t, []string{"work"}, labels(worker), "a worker never acts as the owner, even given it")
	assert.Equal(t, []string{"work"}, labels(coordinator))

	botOn := func(name string, conns ...string) (string, string) {
		t.Helper()

		bot, err := w.svc.PutBot(ctx, orchestration.Bot{
			Name: name, Connections: orchestration.AllowList{Limited: true, Names: conns},
		})
		require.NoError(t, err)
		chat, err := w.svc.CreateChat(ctx, bot.ID, "")
		require.NoError(t, err)
		key, err := w.svc.AddBotToken(ctx, "pi", chat.ID, bot.ID)
		require.NoError(t, err)

		return chat.ID, key
	}

	vmapChat, vmap := botOn("vmap", "lark:work", "lark:me")
	_, err = w.svc.AddChatMessage(ctx, vmapChat, orchestration.ChatOwner, "what is new in the PMO group?")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"work", "me"}, labels(vmap),
		"the owner asked a Bot given his sign-in: it acts as him, though it is not the main Bot")

	_, err = w.svc.AddChatMessages(ctx, vmapChat, orchestration.NewChatMessage{
		Role: orchestration.ChatOwner, Text: "[Linh]: and mine?", Sender: &orchestration.ChatSender{ID: "ou_linh"},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"work"}, labels(vmap), "Linh spoke last: the same Bot is refused the owner's sign-in")

	reportChat, report := botOn("report", "lark:work")
	_, err = w.svc.AddChatMessage(ctx, reportChat, orchestration.ChatOwner, "read my mail")
	require.NoError(t, err)
	assert.Equal(t, []string{"work"}, labels(report), "a Bot whose list leaves it out never acts as the owner")

	routine, err := w.svc.Routines().PutRoutine(ctx, routinestore.RoutineSpec{
		Bot: orchestration.DefaultBotID, Name: "sync", Kind: routinestore.RoutineScript, Script: "sync.sh",
		ScriptText: "#!/bin/sh\n", EveryMinutes: 60,
	}, routinestore.RoutineAuthor{})
	require.NoError(t, err)
	_, err = w.svc.RunRoutineNow(ctx, routine.ID) // a script's token is its live run's (#1023)
	require.NoError(t, err)
	run, err := w.svc.AddRoutineToken(ctx, "pi", routine.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"work"}, labels(run), "a Routine's run is never the owner's turn")

	chat, err := w.svc.CreateChat(ctx, "", "")
	require.NoError(t, err)
	pi, err := w.svc.AddBotToken(ctx, "pi", chat.ID, orchestration.DefaultBotID)
	require.NoError(t, err)
	assert.Equal(t, []string{"work"}, labels(pi), "nothing was asked in the chat yet")

	_, err = w.svc.AddChatMessage(ctx, chat.ID, orchestration.ChatOwner, "what is new in the PMO group?")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"work", "me"}, labels(pi), "the owner asked: the main Bot may act as him")

	_, err = w.svc.AddChatMessages(ctx, chat.ID, orchestration.NewChatMessage{
		Role: orchestration.ChatOwner, Text: "[Linh]: and mine?", Sender: &orchestration.ChatSender{ID: "ou_linh"},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"work"}, labels(pi), "Linh spoke last: the next call is refused the owner's sign-in")

	_, err = w.svc.SetConnectionChats(ctx, "lark", "me", false)
	require.NoError(t, err)
	_, err = w.svc.AddChatMessage(ctx, chat.ID, orchestration.ChatOwner, "go on")
	require.NoError(t, err)
	assert.Equal(t, []string{"work"}, labels(pi), "switched off for chats, not even the main Bot gets it")
}

// Scenario: glab is for every GitLab host (issue #740). Asked for the accounts of gitlab, the agent API lists those
// the agent may use on every host whose kind is gitlab, gitlab.com and a server the owner declared, each with its
// host, and none of a host of another kind. A GitLab host goes through the service proxy: its token is handed to no
// agent.
func TestGlabIsToldTheAccountsOfEveryGitLabHost(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	_, err := w.svc.AddGitHost(ctx, "git.example.com", "gitlab")
	require.NoError(t, err)
	_, err = w.svc.AddGitHost(ctx, "ghe.example.com", "github")
	require.NoError(t, err)

	for _, host := range []string{"git.example.com", "gitlab.com", "ghe.example.com"} {
		_, err := w.svc.SetConnection(ctx, host, "default", "", "conn-"+host)
		require.NoError(t, err)
		_, err = w.svc.SetConnectionChats(ctx, host, "default", true)
		require.NoError(t, err)
	}

	_, err = w.svc.SetConnection(ctx, "gitlab.com", "other", "", "conn-gitlab-other")
	require.NoError(t, err)

	_, pi, err := w.svc.AddToken(ctx, "pi", bearertoken.Coordinator)
	require.NoError(t, err)

	status, body := w.call(http.MethodGet, agenttool.PathConnections+"?integration=gitlab", pi, nil)
	require.Equal(t, http.StatusOK, status)

	var list agenttool.ConnectionList
	require.NoError(t, json.Unmarshal(body, &list))

	var accounts []string
	for _, c := range list.Data {
		accounts = append(accounts, c.Host+":"+c.Label)
	}

	assert.ElementsMatch(t, []string{"git.example.com:default", "gitlab.com:default"}, accounts,
		"gitlab.com:other is not on for chats, and ghe.example.com is GitHub")

	status, _ = w.call(http.MethodPost, agenttool.PathToken, pi,
		agenttool.TokenRequest{Integration: "git.example.com", Label: "default"})
	assert.Equal(t, http.StatusForbidden, status)
	assert.Empty(t, w.asked, "no token of a GitLab host is fetched for an agent")
}
