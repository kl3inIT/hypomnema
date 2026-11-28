package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
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
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
	"github.com/kl3inIT/hypomnema/internal/shared/ciwait"
	"github.com/kl3inIT/hypomnema/internal/shared/githost"
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

const appPublicURL = "https://hypomnema.example"

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
	w.vault = vault.New(db, platform.SystemClock{}, platform.NewID, keys, w.svc.Profiles().ProvidersUsingKeyRef)

	logger := slog.New(slog.NewTextHandler(w.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	w.app = githubapp.New(w.svc, w.vault, appPublicURL, w.gh.Client(), logger)

	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })

	mux := http.NewServeMux()
	api := clientapi.New(w.svc, logger, stop).WithGitHubApp(clientGitHubApp{w.app}, appPublicURL)
	require.NoError(t, api.Register(mux))
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

// login signs the CLI in as the owner and returns its home.
func (w *appWorld) login() string {
	w.t.Helper()

	home := w.t.TempDir()
	tokenFile := filepath.Join(home, "token")
	require.NoError(w.t, os.WriteFile(tokenFile, []byte(w.owner), 0o600))
	require.NoError(w.t, clientcli.Command(w.t.Context(), home,
		[]string{"login", "--url", w.url, "--token-file", tokenFile}, strings.NewReader(""), io.Discard))

	return home
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

	var record struct {
		BotUserID int64 `json:"bot_user_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(opened.Payload[vault.KeyValue]), &record))
	assert.Equal(t, int64(githubapptest.BotUserID), record.BotUserID, "the bot user ID is kept beside the App ID")
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

// An existing installation can use the bot without re-registering, and later control planes reuse the ID.
func TestAnExistingGitHubAppKeepsItsBotUserAfterTheFirstOffer(t *testing.T) {
	t.Parallel()

	w := newAppWorld(t)
	ctx := t.Context()
	legacy := githubapp.Credential{
		AppID: githubapptest.AppID, Slug: githubapptest.Slug, Owner: "kl3inIT",
		PrivateKey: w.gh.PEM, WebhookSecret: w.gh.Secret,
	}
	encoded, err := legacy.Encode()
	require.NoError(t, err)
	_, err = w.vault.Create(ctx, vault.NewSecret{
		Name: githubapp.AppSecret, Kind: vault.KindGeneric, Payload: vault.Payload{vault.KeyValue: encoded},
	})
	require.NoError(t, err)
	w.install()

	bot, owner := w.app.OfferIdentity(ctx, githost.GitHubHost, "kl3inIT/hypomnema")
	assert.Equal(t, githost.Identity{Name: "hypomnema-owner[bot]", Email: "4343+hypomnema-owner[bot]@users.noreply.github.com"},
		bot)
	assert.Equal(t, githost.Identity{Name: "kl3inIT", Email: "164090677+kl3inIT@users.noreply.github.com"}, owner)
	require.Equal(t, 1, w.gh.BotReads())

	restarted := githubapp.New(w.svc, w.vault, appPublicURL, w.gh.Client(), slog.Default())
	restartedBot, restartedOwner := restarted.OfferIdentity(ctx, githost.GitHubHost, "kl3inIT/hypomnema")
	assert.Equal(t, bot, restartedBot)
	assert.Equal(t, owner, restartedOwner)
	assert.Equal(t, 1, w.gh.BotReads())
}

// The owner's form works when a proxy forwards it with a loopback Host (issue #1308).
func TestGitHubAppSetupFormChecksRequestOrigin(t *testing.T) {
	w := newAppWorld(t)
	registered, _ := w.register()
	require.Equal(t, githubapp.SetupPath+"?done=registered", registered.Header.Get("Location"))

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	for _, tc := range []struct {
		name   string
		origin string
		site   string
		want   string
	}{
		{name: "public origin through proxy", origin: appPublicURL, want: "installed"},
		{name: "request host", origin: w.url, want: "installed"},
		{name: "foreign origin", origin: "https://foreign.example", want: "not_same_site"},
		{name: "same-origin takes precedence", origin: "https://foreign.example", site: "same-origin", want: "installed"},
		{name: "cross-site with request host", origin: w.url, site: "cross-site", want: "not_same_site"},
		{name: "cross-site with public origin", origin: appPublicURL, site: "cross-site", want: "not_same_site"},
		{name: "neither header", want: "installed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, w.url+githubapp.SetupPath,
				strings.NewReader("action=sync"))
			require.NoError(t, err)
			req.Header.Set("Authorization", "Bearer "+w.owner)
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			if tc.site != "" {
				req.Header.Set("Sec-Fetch-Site", tc.site)
			}

			resp, err := client.Do(req)
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())
			require.Equal(t, http.StatusSeeOther, resp.StatusCode)
			assert.Equal(t, githubapp.SetupPath+"?done="+tc.want, resp.Header.Get("Location"))
		})
	}
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

	home := w.login()

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

// Scenario (issue #1262, review round 1): the App is installed on several repositories. A worker's token is made
// for its Task's repository alone (repository_ids, as Omnara's mintInstallationToken asks), so it cannot read or
// write the installation's other repositories; each repository has its own token.
func TestAnInstallationTokenIsScopedToItsRepository(t *testing.T) {
	t.Parallel()

	w := newAppWorld(t)
	resp, _ := w.register()
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	w.install()

	before := len(w.gh.Scopes())

	hypomnema, ok, err := w.app.TokenFor(t.Context(), "github.com", "kl3inIT/hypomnema")
	require.NoError(t, err)
	require.True(t, ok)

	memoryOS, ok, err := w.app.TokenFor(t.Context(), "github.com", "kl3inIT/MemoryOS")
	require.NoError(t, err)
	require.True(t, ok)

	assert.NotEqual(t, hypomnema.Value, memoryOS.Value, "a token a repository")
	assert.Equal(t, [][]int64{
		{w.gh.RepositoryID("kl3inIT/hypomnema")}, {w.gh.RepositoryID("kl3inIT/MemoryOS")},
	}, w.gh.Scopes()[before:])

	// The checks a wait reads are read with the pull request's repository's token too.
	_, err = w.app.WaitCI(t.Context(), "https://github.com/kl3inIT/hypomnema/pull/1262", 0)
	require.NoError(t, err)

	for _, scope := range w.gh.Scopes()[before:] {
		assert.Len(t, scope, 1, "no token of the whole installation is made for work")
	}
}

// Scenario (issue #1262, review round 1): the checks of a pull request's head passed, then its author pushes a new
// head. The pull_request synchronize delivery resets its CI: the next wait reads the new head's checks, which run,
// instead of answering the old head's passed.
func TestANewHeadResetsTheCIWaitedFor(t *testing.T) {
	t.Parallel()

	w := newAppWorld(t)
	resp, _ := w.register()
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	w.install()

	pr := "https://github.com/kl3inIT/hypomnema/pull/1262"

	w.gh.Conclude("SUCCESS")
	st, err := w.app.WaitCI(t.Context(), pr, 0)
	require.NoError(t, err)
	require.Equal(t, "passed", st.State)

	const next = "fedcba9876543210"

	w.gh.Push(next)

	body := []byte(`{"action":"synchronize","installation":{"id":77},"repository":{"full_name":"kl3inIT/hypomnema"},
		"pull_request":{"number":1262,"state":"open","merged":false,"head":{"sha":"` + next + `","ref":"hyp/tsk_1"}}}`)
	assert.Equal(t, http.StatusAccepted, w.deliver("pull_request", "delivery-push", body, w.gh.Secret))

	st, err = w.app.WaitCI(t.Context(), pr, 0)
	require.NoError(t, err)
	assert.Equal(t, "pending", st.State, "the new head's check runs")
	assert.Equal(t, next, st.Head)
	assert.Equal(t, 2, w.gh.Reads(), "the new head was read")
}

// Scenario (issue #1262, review round 1): the App does not cover the pull request's repository, so `hypomnema ci
// wait` waits with gh, polling once a minute (--interval 60, GitHub's shared limit). Its --timeout still bounds
// the wait: a gh that does not answer is stopped, and the command says the checks are pending (exit 8).
func TestTheGhFallbackOfCIWaitHonoursItsTimeout(t *testing.T) {
	w := newAppWorld(t)

	bin := t.TempDir()
	args := filepath.Join(bin, "args")
	script := "#!/bin/sh\necho \"$*\" > '" + args + "'\nexec sleep 30\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o700)) //nolint:gosec // G306: runs
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	home := w.login()
	start := time.Now()

	err := clientcli.Command(t.Context(), home,
		[]string{"ci", "wait", "https://github.com/kl3inIT/hypomnema/pull/1262", "--timeout", "1s"},
		strings.NewReader(""), io.Discard)

	var exit *agenttool.ExitError
	require.ErrorAs(t, err, &exit)
	assert.Equal(t, ciwait.ExitPending, exit.Code)
	assert.Less(t, time.Since(start), 10*time.Second, "the timeout stopped gh")

	sent, err := os.ReadFile(args) //nolint:gosec // the test's own file
	require.NoError(t, err)
	assert.Equal(t, "pr checks https://github.com/kl3inIT/hypomnema/pull/1262 --required --watch --interval 60\n",
		string(sent))
}

// Scenario (issue #1262, review round 2): a wait reads the checks, and while GitHub's answer for the old head is on
// its way the author pushes a new head, whose pull_request synchronize resets the CI to pending. The read that
// started before the reset must not overwrite it: the CI kept, and what the wait answers, is the new head's
// pending, not the old head's passed.
func TestReviewSynchronizeDuringRead(t *testing.T) {
	t.Parallel()

	w := newAppWorld(t)
	resp, _ := w.register()
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	w.install()

	pr := "https://github.com/kl3inIT/hypomnema/pull/1262"
	w.gh.Conclude("SUCCESS")

	arrived, release := w.gh.HoldNextRead()
	t.Cleanup(release)

	answered := make(chan error, 1)

	var st ciwait.State

	go func() {
		var err error

		st, err = w.app.WaitCI(context.WithoutCancel(t.Context()), pr, 0)
		answered <- err
	}()

	<-arrived // GitHub answered "passed at the old head"; the answer is on its way.

	const next = "fedcba9876543210"

	w.gh.Push(next)

	body := []byte(`{"action":"synchronize","installation":{"id":77},"repository":{"full_name":"kl3inIT/hypomnema"},
		"pull_request":{"number":1262,"state":"open","merged":false,"head":{"sha":"` + next + `","ref":"hyp/tsk_1"}}}`)
	assert.Equal(t, http.StatusAccepted, w.deliver("pull_request", "delivery-push", body, w.gh.Secret))

	release()
	require.NoError(t, <-answered)

	assert.Equal(t, "pending", st.State, "not the old head's passed")
	assert.Equal(t, next, st.Head)

	kept, ok, err := w.svc.GitHub().CIOf(t.Context(), "kl3init/hypomnema", 1262)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, githubstore.CIPending, kept.State, "the reset is kept")
	assert.Equal(t, next, kept.Head)
}

// Scenario (issue #1262, review round 3): a wait begins while CI runs, and its read gets pending. Before that read is
// kept, the workflow_run completed delivery reads the same head as passed. The wait's pending is kept first; the
// delivery's passed, read of the same head, is newer and must still be kept, so the wait ends on it without waiting
// five minutes for the next read.
func TestReviewCompletionDuringRead(t *testing.T) {
	t.Parallel()

	w := newAppWorld(t)
	resp, _ := w.register()
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	w.install()

	pr := "https://github.com/kl3inIT/hypomnema/pull/1262"

	waitRead, releaseWait := w.gh.HoldNextRead()
	eventRead, releaseEvent := w.gh.HoldNextRead()
	t.Cleanup(releaseWait)
	t.Cleanup(releaseEvent)

	waited := make(chan error, 1)

	go func() {
		_, err := w.app.WaitCI(context.WithoutCancel(t.Context()), pr, 0)
		waited <- err
	}()

	<-waitRead // the wait's read answered pending: the check runs

	w.gh.Conclude("SUCCESS")

	delivered := make(chan int, 1)

	go func() {
		delivered <- w.deliver("workflow_run", "delivery-1", githubapptest.WorkflowRunCompleted(1262, "success"),
			w.gh.Secret)
	}()

	<-eventRead // the delivery's read answered passed

	releaseWait() // pending is kept first
	require.NoError(t, <-waited)

	releaseEvent() // then passed, of the same head
	assert.Equal(t, http.StatusAccepted, <-delivered)

	kept, ok, err := w.svc.GitHub().CIOf(t.Context(), "kl3init/hypomnema", 1262)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, githubstore.CIPassed, kept.State, "the completion is kept")
	assert.Equal(t, githubapptest.Head, kept.Head)

	st, err := w.app.WaitCI(t.Context(), pr, 0)
	require.NoError(t, err)
	assert.Equal(t, "passed", st.State, "the wait ends on the delivery, not five minutes later")
	assert.Equal(t, 2, w.gh.Reads(), "no read more than the two")
}

// Scenario (issue #1262, review round 4): a wait's read finds the pull request closed. Before that read is kept, the
// pull_request closed and then reopened deliveries arrive: reopened keeps the head and resets its CI to pending. The
// stale closed read must not overwrite the reopen, so the next completion ends the wait passed instead of the wait
// staying closed for good.
func TestReviewReopenDuringRead(t *testing.T) {
	t.Parallel()

	w := newAppWorld(t)
	resp, _ := w.register()
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	w.install()

	pr := "https://github.com/kl3inIT/hypomnema/pull/1262"
	prEvent := func(action string) []byte {
		return []byte(`{"action":"` + action + `","installation":{"id":77},` +
			`"repository":{"full_name":"kl3inIT/hypomnema"},"pull_request":{"number":1262,"state":"open",` +
			`"merged":false,"head":{"sha":"` + githubapptest.Head + `","ref":"hyp/tsk_1"}}}`)
	}

	w.gh.SetState("CLOSED")

	read, release := w.gh.HoldNextRead()
	t.Cleanup(release)

	waited := make(chan error, 1)

	go func() {
		_, err := w.app.WaitCI(context.WithoutCancel(t.Context()), pr, 0)
		waited <- err
	}()

	<-read // the wait's read found the pull request closed

	assert.Equal(t, http.StatusAccepted, w.deliver("pull_request", "delivery-closed", prEvent("closed"), w.gh.Secret))
	w.gh.SetState("OPEN")
	assert.Equal(t, http.StatusAccepted, w.deliver("pull_request", "delivery-reopened", prEvent("reopened"),
		w.gh.Secret))

	release()
	require.NoError(t, <-waited)

	kept, ok, err := w.svc.GitHub().CIOf(t.Context(), "kl3init/hypomnema", 1262)
	require.NoError(t, err)
	require.True(t, ok)
	assert.NotEqual(t, githubstore.CIClosed, kept.State, "the reopen stands")

	w.gh.Conclude("SUCCESS")
	assert.Equal(t, http.StatusAccepted, w.deliver("workflow_run", "delivery-done",
		githubapptest.WorkflowRunCompleted(1262, "success"), w.gh.Secret))

	st, err := w.app.WaitCI(t.Context(), pr, 0)
	require.NoError(t, err)
	assert.Equal(t, "passed", st.State)
}

// Scenario (issue #1262, review round 4): a workflow of a head failed, and its rerun on the same head passed. Two
// reads of that head overlap, one finding the failure, the other the rerun's pass, which GitHub says ended later.
// Whichever is kept first, the later result, by the check's completedAt, is the one kept.
func TestReviewSameHeadRerunWriteOrder(t *testing.T) {
	t.Parallel()

	for _, olderFirst := range []bool{true, false} {
		w := newAppWorld(t)
		resp, _ := w.register()
		require.Equal(t, http.StatusSeeOther, resp.StatusCode)
		w.install()

		pr := "https://github.com/kl3inIT/hypomnema/pull/1262"

		w.gh.ConcludeAt("FAILURE", "2026-11-27T10:05:00Z")

		olderRead, releaseOlder := w.gh.HoldNextRead()
		newerRead, releaseNewer := w.gh.HoldNextRead()
		t.Cleanup(releaseOlder)
		t.Cleanup(releaseNewer)

		waited := make(chan error, 1)

		go func() {
			_, err := w.app.WaitCI(context.WithoutCancel(t.Context()), pr, 0)
			waited <- err
		}()

		<-olderRead // the wait read the first run's failure

		w.gh.ConcludeAt("SUCCESS", "2026-11-27T10:20:00Z") // the rerun passed

		delivered := make(chan int, 1)

		go func() {
			delivered <- w.deliver("workflow_run", "delivery-rerun",
				githubapptest.WorkflowRunCompleted(1262, "success"), w.gh.Secret)
		}()

		<-newerRead // the delivery read the rerun's pass

		if olderFirst {
			releaseOlder()
			require.NoError(t, <-waited)
			releaseNewer()
			assert.Equal(t, http.StatusAccepted, <-delivered)
		} else {
			releaseNewer()
			assert.Equal(t, http.StatusAccepted, <-delivered)
			releaseOlder()
			require.NoError(t, <-waited)
		}

		kept, ok, err := w.svc.GitHub().CIOf(t.Context(), "kl3init/hypomnema", 1262)
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, githubstore.CIPassed, kept.State, "the rerun's later pass is kept (older kept first: %v)",
			olderFirst)
	}
}

// Scenario (issue #1262, review round 5): two reads of a head overlap. The first found it passed, with no completion
// time (a status GitHub gives none for), and is kept first; the second found it failed, with a time. A result without
// a time never loses to one with a time: which of the two ended later cannot be told, so the one kept stays.
func TestReviewTimelessPassKeptOverTimedFailure(t *testing.T) {
	t.Parallel()

	w := newAppWorld(t)
	resp, _ := w.register()
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	w.install()

	pr := "https://github.com/kl3inIT/hypomnema/pull/1262"

	w.gh.ConcludeAt("SUCCESS", "")

	firstRead, releaseFirst := w.gh.HoldNextRead()
	secondRead, releaseSecond := w.gh.HoldNextRead()
	t.Cleanup(releaseFirst)
	t.Cleanup(releaseSecond)

	waited := make(chan error, 1)

	go func() {
		_, err := w.app.WaitCI(context.WithoutCancel(t.Context()), pr, 0)
		waited <- err
	}()

	<-firstRead // passed, no time

	w.gh.ConcludeAt("FAILURE", "2026-11-27T10:20:00Z")

	delivered := make(chan int, 1)

	go func() {
		delivered <- w.deliver("workflow_run", "delivery-timed", githubapptest.WorkflowRunCompleted(1262, "failure"),
			w.gh.Secret)
	}()

	<-secondRead // failed, with a time

	releaseFirst()
	require.NoError(t, <-waited)
	releaseSecond()
	assert.Equal(t, http.StatusAccepted, <-delivered)

	kept, ok, err := w.svc.GitHub().CIOf(t.Context(), "kl3init/hypomnema", 1262)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, githubstore.CIPassed, kept.State, "a result without a time never loses to one with a time")
}
