package integration_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/integration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

const (
	appID     = "cli_a1b2c3d4e5f6a7b8"
	appSecret = "app-secret-never-printed"
	appName   = "Hypomnema"
)

// lark stands in for Lark's two endpoints an app Integration calls. It checks
// what the real ones check: the app's id and secret for a token, and a token
// it gave for the app's information. A refusal is a body with a code, as
// Lark's are.
type lark struct {
	t   *testing.T
	srv *httptest.Server

	mu sync.Mutex
	// secret is the app's secret at Lark; the owner can reset it there.
	secret string
	minted int
}

func newLark(t *testing.T) *lark {
	t.Helper()

	l := &lark{t: t, secret: appSecret}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", l.token)
	mux.HandleFunc("GET /bot", l.bot)
	l.srv = httptest.NewServer(mux)
	t.Cleanup(l.srv.Close)

	return l
}

func (l *lark) definition() integration.Definition {
	d := integration.Lark()
	d.AppTokenURL, d.AppInfoURL = l.srv.URL+"/token", l.srv.URL+"/bot"

	return d
}

func (l *lark) token(w http.ResponseWriter, r *http.Request) {
	l.mu.Lock()
	defer l.mu.Unlock()

	var asked struct {
		AppID     string `json:"app_id"`
		AppSecret string `json:"app_secret"`
	}

	if json.NewDecoder(r.Body).Decode(&asked) != nil || asked.AppID != appID || asked.AppSecret != l.secret {
		l.answer(w, http.StatusBadRequest, map[string]any{"code": 10014, "msg": "app secret invalid"})

		return
	}

	l.minted++
	l.answer(w, http.StatusOK, map[string]any{
		"code": 0, "msg": "ok", "tenant_access_token": fmt.Sprintf("t-%d", l.minted), "expire": 7200,
	})
}

func (l *lark) bot(w http.ResponseWriter, r *http.Request) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if r.Header.Get("Authorization") != fmt.Sprintf("Bearer t-%d", l.minted) {
		l.answer(w, http.StatusBadRequest, map[string]any{"code": 99991663, "msg": "invalid access token"})

		return
	}

	l.answer(w, http.StatusOK, map[string]any{"code": 0, "msg": "ok", "bot": map[string]any{"app_name": appName}})
}

func (l *lark) answer(w http.ResponseWriter, status int, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	assert.NoError(l.t, json.NewEncoder(w).Encode(body))
}

func (l *lark) mints() int {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.minted
}

func (l *lark) resetSecret(secret string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.secret = secret
}

// larkWorld is a world whose service knows Lark, at the stand-in's address.
func larkWorld(t *testing.T) (*world, *lark) {
	t.Helper()

	w, l := newWorld(t), newLark(t)
	w.svc = integration.New(w.orch, w.vault, w.clock, platform.NewID, l.definition())

	return w, l
}

func (w *world) theLark() integration.Integration {
	w.t.Helper()

	all, err := w.svc.List(w.t.Context())
	require.NoError(w.t, err)
	require.Len(w.t, all, 1)

	return all[0]
}

// Scenario: the owner gives Hypomnema his Lark app's id and secret and
// connects it as "work"; an agent's token is asked for over the next hours.
// Nothing is signed in: the Connection carries the app's own name. The
// secret is sealed, a token is asked of Lark only when the one in use is
// about to end, and a node is told to have Lark's CLI.
func TestTheOwnerConnectsALarkAppAndItsTokenStaysFresh(t *testing.T) {
	t.Parallel()

	w, l := larkWorld(t)

	assert.Empty(t, w.svc.Tools(t.Context(), "linux", "amd64"), "an app nobody connected asks nothing of a node")

	_, err := w.svc.Connect(t.Context(), integration.LarkID, "work")
	require.ErrorIs(t, err, orchestration.ErrConflict, "there is no app yet")

	require.NoError(t, w.svc.SetClient(t.Context(), integration.LarkID, appID, appSecret))

	conn, err := w.svc.Connect(t.Context(), integration.LarkID, "work")
	require.NoError(t, err)
	assert.Equal(t, appName, conn.Account)
	assert.Empty(t, conn.Grants)
	assert.False(t, conn.SignInRequired)

	listed := w.theLark()
	assert.Equal(t, integration.KindApp, listed.CredentialKind())
	assert.Equal(t, appID, listed.ClientID)
	require.Len(t, listed.Connections, 1)
	assert.Equal(t, "work", listed.Connections[0].Label)

	stored := w.stored()
	assert.Contains(t, stored, appName)
	assert.NotContains(t, stored, appSecret)
	assert.NotContains(t, stored, "t-1", "an app's token is not written down")

	token, err := w.svc.Token(t.Context(), integration.LarkID, "work")
	require.NoError(t, err)
	assert.Equal(t, "t-1", token.AccessToken)
	assert.Equal(t, t0.Add(2*time.Hour), token.ExpiresAt)

	w.clock.advance(2*time.Hour - 3*time.Minute)

	token, err = w.svc.Token(t.Context(), integration.LarkID, "work")
	require.NoError(t, err)
	assert.Equal(t, "t-1", token.AccessToken, "three minutes are enough for a call")
	assert.Equal(t, 1, l.mints())

	w.clock.advance(2 * time.Minute)

	token, err = w.svc.Token(t.Context(), integration.LarkID, "work")
	require.NoError(t, err)
	assert.Equal(t, "t-2", token.AccessToken, "one minute is not")

	_, err = w.svc.Token(t.Context(), integration.LarkID, "personal")
	require.ErrorIs(t, err, integration.ErrNotFound)

	tools := w.svc.Tools(t.Context(), "linux", "arm64")
	require.Len(t, tools, 1)
	assert.Equal(t, nodeproto.Tool{
		Name: "lark-cli", Version: "1.0.100", Binary: "lark-cli",
		URL:    "https://github.com/larksuite/cli/releases/download/v1.0.100/lark-cli-1.0.100-linux-arm64.tar.gz",
		SHA256: "166e95ffaec34f82123f50316480250329884c09e71a6f92313047e2c5c3fa5d",
	}, tools[0])
}

// Scenario: the owner mistypes the app's secret, then later resets the secret
// in Lark's console and gives Hypomnema the new one. A secret Lark refuses
// connects nothing and the refusal carries no secret; a new client is used
// from the next token on, not the token minted with the old one.
func TestALarkAppIsCheckedWithLarkAndFollowsItsClient(t *testing.T) {
	t.Parallel()

	w, l := larkWorld(t)

	require.NoError(t, w.svc.SetClient(t.Context(), integration.LarkID, appID, "mistyped"))

	_, err := w.svc.Connect(t.Context(), integration.LarkID, "work")
	require.ErrorIs(t, err, integration.ErrSignInFailed)
	assert.Contains(t, err.Error(), "code 10014")
	assert.NotContains(t, err.Error(), "mistyped")
	assert.Empty(t, w.theLark().Connections)

	require.NoError(t, w.svc.SetClient(t.Context(), integration.LarkID, appID, appSecret))

	_, err = w.svc.Connect(t.Context(), integration.LarkID, "work")
	require.NoError(t, err)

	l.resetSecret("reset-at-lark")
	require.NoError(t, w.svc.SetClient(t.Context(), integration.LarkID, appID, "reset-at-lark"))

	token, err := w.svc.Token(t.Context(), integration.LarkID, "work")
	require.NoError(t, err)
	assert.Equal(t, "t-2", token.AccessToken, "the token of the old secret is not handed out again")
}

// Scenario: the owner tries to sign in to Lark as he does to Google, tries to
// connect Google as an app, then disconnects the Lark app and connects it
// again. Each Integration is connected its own way only; disconnecting an
// app forgets the Connection and keeps the app's client.
func TestAnAppIsConnectedWithoutASignInAndKeepsItsClient(t *testing.T) {
	t.Parallel()

	w, l := larkWorld(t)
	w.svc = integration.New(w.orch, w.vault, w.clock, platform.NewID, l.definition(), w.google.definition())

	require.NoError(t, w.svc.SetClient(t.Context(), integration.LarkID, appID, appSecret))
	require.NoError(t, w.svc.SetClient(t.Context(), integration.GoogleID, clientID, clientSecret))

	_, err := w.svc.Begin(t.Context(), integration.LarkID, "work", nil)
	require.ErrorIs(t, err, integration.ErrInvalid)

	_, err = w.svc.Connect(t.Context(), integration.GoogleID, "work")
	require.ErrorIs(t, err, integration.ErrInvalid)

	_, err = w.svc.Connect(t.Context(), integration.LarkID, "My Work")
	require.ErrorIs(t, err, integration.ErrInvalid)

	_, err = w.svc.Connect(t.Context(), integration.LarkID, "work")
	require.NoError(t, err)

	revoked, err := w.svc.Disconnect(t.Context(), integration.LarkID, "work")
	require.NoError(t, err)
	assert.True(t, revoked, "nothing of the Connection is left at Lark")

	_, err = w.svc.Token(t.Context(), integration.LarkID, "work")
	require.ErrorIs(t, err, integration.ErrNotFound)

	_, err = w.svc.Connect(t.Context(), integration.LarkID, "work")
	require.NoError(t, err, "the app's client is still there")
}
