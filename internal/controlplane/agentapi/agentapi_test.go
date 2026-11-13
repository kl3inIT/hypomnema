package agentapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/agentapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
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
}

// newWorld is a control plane with two accounts of a service that does not go through the service proxy of the owner's: work,
// and personal.
func newWorld(t *testing.T) *world {
	t.Helper()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: filepath.Join(t.TempDir(), "hypomnema.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, orchestration.Migrate(t.Context(), db))

	w := &world{t: t, svc: orchestration.New(db, platform.SystemClock{}, platform.NewID), logs: &bytes.Buffer{},
		browser: &sharedBrowser{}}

	for _, label := range []string{"work", "personal"} {
		_, err := w.svc.SaveServiceConnection(t.Context(), orchestration.Connection{
			Integration: "notion", Label: label, Account: label + "@example.com", Secret: "conn-notion-" + label,
		})
		require.NoError(t, err)
	}

	mux := http.NewServeMux()
	agentapi.New(w.svc, func(_ context.Context, integration, label string) (agentapi.Token, error) {
		w.asked = append(w.asked, integration+":"+label)

		return agentapi.Token{AccessToken: "ya29.token-of-" + label, ExpiresAt: time.Unix(1_800_000_000, 0).UTC()}, nil
	}, slog.New(slog.NewTextHandler(w.logs, nil))).WithBrowser(w.browser).Register(mux)

	w.srv = httptest.NewServer(mux)
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
	_, err = w.svc.PutBot(ctx, orchestration.Bot{Name: "vmap", Connections: limited})
	require.NoError(t, err)

	chat, err := w.svc.CreateChat(ctx, "vmap", "")
	require.NoError(t, err)

	vmap, err := w.svc.AddBotToken(ctx, "pi", chat.ID, "vmap")
	require.NoError(t, err)

	mine, err := w.svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	coordinator, err := w.svc.AddBotToken(ctx, "pi", mine.ID, orchestration.DefaultBot)
	require.NoError(t, err)

	routine, err := w.svc.PutRoutine(ctx, orchestration.RoutineSpec{
		Bot: "vmap", Name: "sync", Kind: orchestration.RoutineScript, Script: "sync.sh", ScriptText: "#!/bin/sh\n",
		EveryMinutes: 60,
	}, orchestration.RoutineAuthor{})
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

	_, err = w.svc.PutBot(ctx, orchestration.Bot{Name: "vmap", Connections: orchestration.AllowList{Limited: true}})
	require.NoError(t, err)

	status, _ = w.token(vmap, "work")
	assert.Equal(t, http.StatusForbidden, status, "taken off the list, the next call is refused")
}

// Scenario: two Lark accounts, each an app of its own (issue #311): the
// owner's "work" and a reports app. Lark's tool is told each account's app
// with it, so that it runs as the app of the account it acts as. A launcher
// from before reads one id for every account: it is told one only while
// every account given is the same app.
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
		Data: []agenttool.Connection{{Label: "work", Account: "Hypomnema", ClientID: "cli_work"}}, ClientID: "cli_work",
	}, list())

	_, err = w.svc.SaveServiceConnection(ctx, orchestration.Connection{
		Integration: "lark", Label: "report", Account: "Superset Report", Secret: "conn-lark-report",
		AppID: "cli_report",
	})
	require.NoError(t, err)

	got := list()
	assert.ElementsMatch(t, []agenttool.Connection{
		{Label: "work", Account: "Hypomnema", ClientID: "cli_work"},
		{Label: "report", Account: "Superset Report", ClientID: "cli_report"},
	}, got.Data)
	assert.Empty(t, got.ClientID, "two apps have no one id")
}
