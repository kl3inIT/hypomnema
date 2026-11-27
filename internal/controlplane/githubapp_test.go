package controlplane

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientcli"
	"github.com/kl3inIT/hypomnema/internal/controlplane/githubapp"
	"github.com/kl3inIT/hypomnema/internal/controlplane/githubapp/githubapptest"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/githubstore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

// appWorld is a control plane with the owner's GitHub App wired as serve wires it (issue #1262): the client API with
// its pages, the App's deliveries, and a GitHub that answers in memory.
type appWorld struct {
	t     *testing.T
	svc   *orchestration.Service
	vault *vault.Service
	app   *githubapp.App
	gh    *githubapptest.GitHub
	url   string
	owner string
	logs  *lockedBuffer
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.b.String()
}

func newAppWorld(t *testing.T) *appWorld {
	t.Helper()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	entry, err := vault.GenerateKey("k1")
	require.NoError(t, err)
	keys, err := vault.ParseKeyring(entry, "k1")
	require.NoError(t, err)

	w := &appWorld{t: t, gh: githubapptest.New(t), logs: &lockedBuffer{}}
	w.svc = orchestration.New(db, platform.SystemClock{}, platform.NewID)
	w.vault = vault.New(db, platform.SystemClock{}, platform.NewID, keys, w.svc.ProvidersUsingKeyRef)

	logger := slog.New(slog.NewTextHandler(w.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	w.app = githubapp.New(w.svc, w.vault, "https://hypomnema.example", w.gh.Client(), logger)

	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })

	mux := http.NewServeMux()
	require.NoError(t, clientapi.New(w.svc, logger, stop).WithGitHubApp(clientGitHubApp{w.app}).Register(mux))
	mux.HandleFunc("POST "+githubapp.EventsPath, w.app.ServeEvents)

	hs := httptest.NewServer(mux)
	t.Cleanup(hs.Close)
	w.url = hs.URL

	_, w.owner, err = w.svc.AddToken(t.Context(), "owner", bearertoken.Owner)
	require.NoError(t, err)

	return w
}

// page is an answer of the control plane's, its body read.
type page struct {
	StatusCode int
	Header     http.Header
	body       string
}

// get asks the control plane as the owner's browser does, without following a redirect.
func (w *appWorld) get(path string) (page, string) {
	w.t.Helper()

	req, err := http.NewRequestWithContext(w.t.Context(), http.MethodGet, w.url+path, nil)
	require.NoError(w.t, err)
	req.Header.Set("Authorization", "Bearer "+w.owner)

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	resp, err := client.Do(req)
	require.NoError(w.t, err)

	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(w.t, err)

	return page{StatusCode: resp.StatusCode, Header: resp.Header, body: string(body)}, string(body)
}

var registrationState = regexp.MustCompile(`settings/apps/new\?state=([A-Za-z0-9_-]+)`)

// register registers the App as the owner does: the page's form goes to GitHub, which sends him back with a code.
func (w *appWorld) register() (page, string) {
	w.t.Helper()

	resp, setup := w.get(githubapp.SetupPath)
	require.Equal(w.t, http.StatusOK, resp.StatusCode, setup)

	state := registrationState.FindStringSubmatch(setup)
	require.NotNil(w.t, state, "the page posts the manifest to GitHub with a state")

	return w.get(githubapp.CallbackPath + "?code=the-manifest-code&state=" + state[1])
}

// install records the installation as GitHub's redirect after the owner installs the App does.
func (w *appWorld) install() {
	w.t.Helper()

	resp, _ := w.get(githubapp.InstalledPath + "?installation_id=77&setup_action=install")
	require.Equal(w.t, http.StatusSeeOther, resp.StatusCode)
	require.Contains(w.t, resp.Header.Get("Location"), "done=installed")
}

// deliver sends a delivery as GitHub does, signed with secret.
func (w *appWorld) deliver(event, id string, body []byte, secret string) int {
	w.t.Helper()

	req, err := http.NewRequestWithContext(w.t.Context(), http.MethodPost, w.url+githubapp.EventsPath,
		bytes.NewReader(body))
	require.NoError(w.t, err)
	req.Header.Set(githubapp.EventHeader, event)
	req.Header.Set(githubapp.DeliveryHeader, id)
	req.Header.Set(githubapp.SignatureHeader, githubapp.Sign(body, secret))

	resp, err := http.DefaultClient.Do(req)
	require.NoError(w.t, err)
	_ = resp.Body.Close()

	return resp.StatusCode
}

// Scenario (issue #1262, the comment's step 2): GitHub sends the owner back with the manifest's code; the control
// plane exchanges it and keeps the App's id, private key and webhook secret in the vault. Neither the key nor the
// secret is in what the callback answers, in the page it leads to, or in the log.
func TestTheManifestCallbackKeepsTheCredentialInTheVaultAndShowsNoSecret(t *testing.T) {
	t.Parallel()

	w := newAppWorld(t)

	resp, body := w.register()
	require.Equal(t, http.StatusSeeOther, resp.StatusCode, body)
	assert.Equal(t, githubapp.SetupPath+"?done=registered", resp.Header.Get("Location"))

	opened, err := w.vault.Open(t.Context(), "github-app")
	require.NoError(t, err)
	cred, err := githubapp.DecodeCredential(opened.Payload[vault.KeyValue])
	require.NoError(t, err)
	assert.Equal(t, int64(githubapptest.AppID), cred.AppID)
	assert.Equal(t, w.gh.PEM, cred.PrivateKey)
	assert.Equal(t, w.gh.Secret, cred.WebhookSecret)

	_, setup := w.get(githubapp.SetupPath + "?done=registered")
	assert.Contains(t, setup, githubapptest.Slug)

	keyLine := strings.Split(strings.TrimSpace(w.gh.PEM), "\n")[1]
	for _, shown := range []string{body, setup, w.logs.String(), resp.Header.Get("Location")} {
		assert.NotContains(t, shown, keyLine)
		assert.NotContains(t, shown, w.gh.Secret)
	}

	assert.Contains(t, w.logs.String(), "githubapp.registered", "the registration is logged, by the App's id")

	// A state is good once: the same code sent back again changes nothing.
	again, _ := w.get(githubapp.CallbackPath + "?code=the-manifest-code&state=" + "not-a-state")
	assert.Contains(t, again.Header.Get("Location"), "done=expired")
}

// Scenario (issue #1262): a worker waits for its pull request's CI with `hypomnema ci wait`; a signed workflow_run
// completed on its head ends the wait with the conclusion, read once from GitHub with the installation's token.
// The command itself calls no GitHub.
func TestASignedWorkflowRunCompletedEndsTheCIWaitWithItsConclusion(t *testing.T) {
	t.Parallel()

	w := newAppWorld(t)
	resp, _ := w.register()
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	w.install()

	home := t.TempDir()
	tokenFile := filepath.Join(home, "token")
	require.NoError(t, os.WriteFile(tokenFile, []byte(w.owner), 0o600))
	require.NoError(t, clientcli.Command(t.Context(), home, []string{"login", "--url", w.url, "--token-file", tokenFile},
		strings.NewReader(""), io.Discard))

	out := &lockedBuffer{}
	done := make(chan error, 1)

	go func() {
		done <- clientcli.Command(context.WithoutCancel(t.Context()), home,
			[]string{"ci", "wait", "https://github.com/kl3inIT/hypomnema/pull/1262"}, strings.NewReader(""), out)
	}()

	// The first wait reads the checks once: the required check runs.
	require.Eventually(t, func() bool { return strings.Contains(out.String(), "pending") }, 10*time.Second,
		10*time.Millisecond)
	require.Equal(t, 1, w.gh.Reads())

	w.gh.Conclude("SUCCESS")
	status := w.deliver("workflow_run", "delivery-1", githubapptest.WorkflowRunCompleted(1262, "success"), w.gh.Secret)
	assert.Equal(t, http.StatusAccepted, status)

	select {
	case err := <-done:
		require.NoError(t, err, out.String())
	case <-time.After(10 * time.Second):
		t.Fatal("the event did not end the wait")
	}

	assert.Contains(t, out.String(), "https://github.com/kl3inIT/hypomnema/pull/1262\tpassed\tat "+
		githubapptest.Head[:12])
	assert.Equal(t, 2, w.gh.Reads(), "one read when the wait began, one when CI ended")

	for _, auth := range w.gh.Asked() {
		assert.True(t, strings.HasPrefix(auth, "Bearer ghs_installation_token_"), "read with the installation's "+
			"token, not the owner's: %s", auth)
	}
}

// Scenario (issue #1262): a delivery not signed with the App's webhook secret is refused with 401 and changes
// nothing: no delivery is recorded and the CI waited for is not read again.
func TestADeliveryWithABadSignatureIsRefusedAndChangesNothing(t *testing.T) {
	t.Parallel()

	w := newAppWorld(t)
	resp, _ := w.register()
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	w.install()

	_, err := w.svc.GitHub().WatchCI(t.Context(), "kl3init/hypomnema", 1262)
	require.NoError(t, err)

	w.gh.Conclude("FAILURE")

	body := githubapptest.WorkflowRunCompleted(1262, "failure")
	assert.Equal(t, http.StatusUnauthorized, w.deliver("workflow_run", "delivery-1", body, "not-the-secret"))

	// Unsigned too.
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, w.url+githubapp.EventsPath,
		bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set(githubapp.EventHeader, "workflow_run")
	req.Header.Set(githubapp.DeliveryHeader, "delivery-2")
	unsigned, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = unsigned.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, unsigned.StatusCode)

	_, recorded, err := w.svc.GitHub().LatestDelivery(t.Context())
	require.NoError(t, err)
	assert.False(t, recorded, "no delivery recorded")

	ci, _, err := w.svc.GitHub().CIOf(t.Context(), "kl3init/hypomnema", 1262)
	require.NoError(t, err)
	assert.Equal(t, githubstore.CIPending, ci.State)
	assert.True(t, ci.ReadAt.IsZero(), "GitHub was not read")
	assert.Zero(t, w.gh.Reads())

	// The same body signed with the App's secret is taken.
	assert.Equal(t, http.StatusAccepted, w.deliver("workflow_run", "delivery-1", body, w.gh.Secret))
}

// Scenario (issue #1262): GitHub sends a delivery again (its "Redeliver", or a retry) with the same id: it is
// handled once.
func TestADeliverySentTwiceIsHandledOnce(t *testing.T) {
	t.Parallel()

	w := newAppWorld(t)
	resp, _ := w.register()
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	w.install()

	_, err := w.svc.GitHub().WatchCI(t.Context(), "kl3init/hypomnema", 1262)
	require.NoError(t, err)

	w.gh.Conclude("SUCCESS")
	body := githubapptest.WorkflowRunCompleted(1262, "success")

	assert.Equal(t, http.StatusAccepted, w.deliver("workflow_run", "delivery-1", body, w.gh.Secret))
	assert.Equal(t, http.StatusOK, w.deliver("workflow_run", "delivery-1", body, w.gh.Secret), "handled before")

	assert.Equal(t, 1, w.gh.Reads(), "the checks were read for the first delivery only")

	ci, _, err := w.svc.GitHub().CIOf(t.Context(), "kl3init/hypomnema", 1262)
	require.NoError(t, err)
	assert.Equal(t, githubstore.CIPassed, ci.State)
}

// Scenario (issue #1262): a pull request someone waits for merges. The pull_request closed delivery says so: its CI
// is marked merged without reading GitHub, and the wait ends.
func TestAPullRequestThatMergesEndsItsWait(t *testing.T) {
	t.Parallel()

	w := newAppWorld(t)
	resp, _ := w.register()
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	w.install()

	_, err := w.svc.GitHub().WatchCI(t.Context(), "kl3init/hypomnema", 1262)
	require.NoError(t, err)

	body := []byte(`{"action":"closed","installation":{"id":77},"repository":{"full_name":"kl3inIT/hypomnema"},
		"pull_request":{"number":1262,"state":"closed","merged":true,"head":{"sha":"` + githubapptest.Head +
		`","ref":"hyp/tsk_1"}}}`)
	assert.Equal(t, http.StatusAccepted, w.deliver("pull_request", "delivery-merged", body, w.gh.Secret))

	st, err := w.app.WaitCI(t.Context(), "kl3inIT/hypomnema#1262", time.Second)
	require.NoError(t, err)
	assert.Equal(t, "merged", st.State)
	assert.Zero(t, w.gh.Reads(), "the event said it; GitHub was not read")
}
