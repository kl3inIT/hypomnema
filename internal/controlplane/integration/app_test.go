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
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

const (
	appID     = "cli_a1b2c3d4e5f6a7b8"
	appSecret = "app-secret-never-printed"
	appName   = "Hypomnema"
	// reportID is a second app of the same Lark organization, for a Bot that writes reports.
	reportID     = "cli_b9c8d7e6f5a4b3c2"
	reportSecret = "report-secret-never-printed"
	reportName   = "Superset Report"
)

// larkApp is one app at Lark: its secret, which the owner can reset there,
// and its name.
type larkApp struct{ secret, name string }

// lark stands in for Lark's two endpoints an app Integration calls. It checks
// what the real ones check: an app's id and secret for a token, and a token it
// gave for the information of the app it gave it to. A refusal is a body with
// a code, as Lark's are.
type lark struct {
	t   *testing.T
	srv *httptest.Server

	mu     sync.Mutex
	apps   map[string]*larkApp
	minted int
	// tokens is the app each token was given to.
	tokens map[string]string
}

func newLark(t *testing.T) *lark {
	t.Helper()

	l := &lark{t: t, tokens: map[string]string{}, apps: map[string]*larkApp{
		appID:    {secret: appSecret, name: appName},
		reportID: {secret: reportSecret, name: reportName},
	}}
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

	if json.NewDecoder(r.Body).Decode(&asked) != nil || l.apps[asked.AppID] == nil ||
		l.apps[asked.AppID].secret != asked.AppSecret {
		l.answer(w, http.StatusBadRequest, map[string]any{"code": 10014, "msg": "app secret invalid"})

		return
	}

	l.minted++
	token := fmt.Sprintf("t-%d", l.minted)
	l.tokens[token] = asked.AppID
	l.answer(w, http.StatusOK, map[string]any{"code": 0, "msg": "ok", "tenant_access_token": token, "expire": 7200})
}

func (l *lark) bot(w http.ResponseWriter, r *http.Request) {
	l.mu.Lock()
	defer l.mu.Unlock()

	var token string
	if _, err := fmt.Sscanf(r.Header.Get("Authorization"), "Bearer %s", &token); err != nil || l.tokens[token] == "" {
		l.answer(w, http.StatusBadRequest, map[string]any{"code": 99991663, "msg": "invalid access token"})

		return
	}

	l.answer(w, http.StatusOK, map[string]any{
		"code": 0, "msg": "ok", "bot": map[string]any{"app_name": l.apps[l.tokens[token]].name},
	})
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

// appOf is the app Lark gave a token to.
func (l *lark) appOf(token string) string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.tokens[token]
}

func (l *lark) resetSecret(app, secret string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.apps[app].secret = secret
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
// Nothing is signed in: the Connection carries the app's own name and id. The
// secret is sealed, a token is asked of Lark only when the one in use is
// about to end, and a node is told to have Lark's CLI.
func TestTheOwnerConnectsALarkAppAndItsTokenStaysFresh(t *testing.T) {
	t.Parallel()

	w, l := larkWorld(t)

	assert.Empty(t, w.svc.Tools(t.Context(), "linux", "amd64"), "an app nobody connected asks nothing of a node")

	_, err := w.svc.Connect(t.Context(), integration.LarkID, "work", integration.App{})
	require.ErrorIs(t, err, integration.ErrInvalid, "a new Connection needs its app")

	conn, err := w.svc.Connect(t.Context(), integration.LarkID, "work", integration.App{ID: appID, Secret: appSecret})
	require.NoError(t, err)
	assert.Equal(t, appName, conn.Account)
	assert.Equal(t, appID, conn.AppID)
	assert.Empty(t, conn.Grants)
	assert.False(t, conn.SignInRequired)

	listed := w.theLark()
	assert.Equal(t, integration.KindApp, listed.CredentialKind())
	assert.Empty(t, listed.ClientID, "an app Integration has no client of its own")
	require.Len(t, listed.Connections, 1)
	assert.Equal(t, "work", listed.Connections[0].Label)

	stored := w.stored()
	assert.Contains(t, stored, appName)
	assert.NotContains(t, stored, appSecret)
	assert.NotContains(t, stored, "t-1", "an app's token is not written down")

	token, err := w.svc.Token(t.Context(), integration.LarkID, "work")
	require.NoError(t, err)
	assert.Equal(t, "t-1", token.AccessToken, "the token minted to check the app is used")
	assert.Equal(t, t0.Add(2*time.Hour), token.ExpiresAt)
	assert.Equal(t, appID, token.ClientID, "Lark's tool needs the app's id beside the token")

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

// Scenario: one Lark organization, two apps (issue #311). The owner connects
// his own app as "work" and a reports app as "report", for a Bot that must
// not act as him. Every token asked for "report" is the reports app's, and
// every one for "work" his own, also after both were minted again.
func TestEachLarkConnectionIsItsOwnApp(t *testing.T) {
	t.Parallel()

	w, l := larkWorld(t)

	_, err := w.svc.Connect(t.Context(), integration.LarkID, "work", integration.App{ID: appID, Secret: appSecret})
	require.NoError(t, err)

	report, err := w.svc.Connect(t.Context(), integration.LarkID, "report",
		integration.App{ID: reportID, Secret: reportSecret})
	require.NoError(t, err)
	assert.Equal(t, reportName, report.Account)

	for range 2 {
		work, err := w.svc.Token(t.Context(), integration.LarkID, "work")
		require.NoError(t, err)
		assert.Equal(t, appID, l.appOf(work.AccessToken))
		assert.Equal(t, appID, work.ClientID)

		reports, err := w.svc.Token(t.Context(), integration.LarkID, "report")
		require.NoError(t, err)
		assert.Equal(t, reportID, l.appOf(reports.AccessToken))
		assert.Equal(t, reportID, reports.ClientID)

		w.clock.advance(2 * time.Hour)
	}

	stored := w.stored()
	assert.NotContains(t, stored, appSecret)
	assert.NotContains(t, stored, reportSecret)
}

// Scenario: the owner mistypes the app's secret, then later resets the secret
// in Lark's console and gives Hypomnema the new one under the same label. A
// secret Lark refuses connects nothing and the refusal carries no secret; the
// new secret is used from then on, not the token of the old one. An app has
// no client to set.
func TestALarkAppIsCheckedWithLarkAndTakesItsNewSecret(t *testing.T) {
	t.Parallel()

	w, l := larkWorld(t)

	err := w.svc.SetClient(t.Context(), integration.LarkID, appID, appSecret)
	require.ErrorIs(t, err, integration.ErrInvalid, "each Lark Connection is given its own app")

	_, err = w.svc.Connect(t.Context(), integration.LarkID, "work", integration.App{ID: appID, Secret: "mistyped"})
	require.ErrorIs(t, err, integration.ErrSignInFailed)
	assert.Contains(t, err.Error(), "code 10014")
	assert.NotContains(t, err.Error(), "mistyped")
	assert.Empty(t, w.theLark().Connections)

	_, err = w.svc.Connect(t.Context(), integration.LarkID, "work", integration.App{ID: appID, Secret: appSecret})
	require.NoError(t, err)

	l.resetSecret(appID, "reset-at-lark")

	//nolint:gosec // G101: a made-up secret of the stand-in
	_, err = w.svc.Connect(t.Context(), integration.LarkID, "work", integration.App{ID: appID, Secret: "reset-at-lark"})
	require.NoError(t, err)

	w.clock.advance(2 * time.Hour)

	token, err := w.svc.Token(t.Context(), integration.LarkID, "work")
	require.NoError(t, err)
	assert.Equal(t, "t-3", token.AccessToken, "minted with the new secret")
}

// Scenario: the owner tries to sign in to Lark as he does to Google, tries to
// connect Google as an app, then disconnects the Lark app. Each Integration
// is connected its own way only; disconnecting an app forgets the Connection
// and its secret, so connecting the label again needs the app again.
func TestAnAppIsConnectedWithoutASignInAndForgottenWithItsSecret(t *testing.T) {
	t.Parallel()

	w, l := larkWorld(t)
	w.svc = integration.New(w.orch, w.vault, w.clock, platform.NewID, l.definition(), w.google.definition())

	require.NoError(t, w.svc.SetClient(t.Context(), integration.GoogleID, clientID, clientSecret))

	_, err := w.svc.Begin(t.Context(), integration.LarkID, "work", nil)
	require.ErrorIs(t, err, integration.ErrInvalid)

	_, err = w.svc.Connect(t.Context(), integration.GoogleID, "work", integration.App{ID: appID, Secret: appSecret})
	require.ErrorIs(t, err, integration.ErrInvalid)

	_, err = w.svc.Connect(t.Context(), integration.LarkID, "My Work", integration.App{ID: appID, Secret: appSecret})
	require.ErrorIs(t, err, integration.ErrInvalid)

	_, err = w.svc.Connect(t.Context(), integration.LarkID, "work", integration.App{ID: appID, Secret: appSecret})
	require.NoError(t, err)

	revoked, err := w.svc.Disconnect(t.Context(), integration.LarkID, "work")
	require.NoError(t, err)
	assert.True(t, revoked, "nothing of the Connection is left at Lark")

	_, err = w.svc.Token(t.Context(), integration.LarkID, "work")
	require.ErrorIs(t, err, integration.ErrNotFound)

	_, err = w.vault.Get(t.Context(), "conn-lark-work")
	require.ErrorIs(t, err, vault.ErrNotFound, "the app's secret went with it")

	_, err = w.svc.Connect(t.Context(), integration.LarkID, "work", integration.App{})
	require.ErrorIs(t, err, integration.ErrInvalid, "there is no app to check again")
}

// Scenario: a control plane from before issue #311 had one Lark app, and the
// migration made its Connections "work" and "team" that app, naming the
// secret the Integration had. Both keep working as it, and "work" is checked
// again with nothing given. When the owner gives "work" an app of its own
// and later forgets "team", the old secret goes once no Connection names it.
func TestAConnectionMovedFromTheIntegrationsAppKeepsWorking(t *testing.T) {
	t.Parallel()

	w, _ := larkWorld(t)

	const shared = "integration-lark-client"

	_, err := w.vault.Create(t.Context(), vault.NewSecret{
		Name: shared, Kind: vault.KindGeneric, Payload: vault.Payload{vault.KeyValue: appSecret},
	})
	require.NoError(t, err)

	for _, label := range []string{"work", "team"} {
		_, err = w.orch.SaveServiceConnection(t.Context(), orchestration.Connection{
			Integration: integration.LarkID, Label: label, Account: appName, Secret: shared, AppID: appID,
		})
		require.NoError(t, err)

		token, err := w.svc.Token(t.Context(), integration.LarkID, label)
		require.NoError(t, err, label)
		assert.Equal(t, appID, token.ClientID)
	}

	checked, err := w.svc.Connect(t.Context(), integration.LarkID, "work", integration.App{})
	require.NoError(t, err)
	assert.Equal(t, appID, checked.AppID)

	_, err = w.svc.Connect(t.Context(), integration.LarkID, "work",
		integration.App{ID: reportID, Secret: reportSecret})
	require.NoError(t, err)

	_, err = w.vault.Get(t.Context(), shared)
	require.NoError(t, err, "team still names it")

	_, err = w.svc.Disconnect(t.Context(), integration.LarkID, "team")
	require.NoError(t, err)

	_, err = w.vault.Get(t.Context(), shared)
	require.ErrorIs(t, err, vault.ErrNotFound)

	token, err := w.svc.Token(t.Context(), integration.LarkID, "work")
	require.NoError(t, err)
	assert.Equal(t, reportID, token.ClientID)
}
