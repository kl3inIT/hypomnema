package githubapp

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/githubstore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
	"github.com/kl3inIT/hypomnema/internal/shared/ciwait"
	"github.com/kl3inIT/hypomnema/internal/shared/githost"
)

// AppSecret is the vault secret that holds the owner's GitHub App's credential (issue #1262): its id, private
// key and webhook secret as one value (Credential), as Omnara keeps them as one credential.
const AppSecret = "github-app" //nolint:gosec // the name of a vault secret, not a credential

// registrationTTL is how long a registration the owner started can be completed: GitHub's code is good for an hour
// ("Registering a GitHub App from a manifest"), as Omnara's state is (githubManifestStateTTL).
const registrationTTL = time.Hour

// ciReadEvery is how often a pull request's checks are read when no event of the App's says anything of them: the
// fallback for an event GitHub did not send or the control plane missed (the issue's design, row 4).
const ciReadEvery = 5 * time.Minute

// gitHubRead bounds one read of GitHub's for the App: an event is answered within GitHub's ten seconds.
const gitHubRead = 8 * time.Second

// App is the owner's GitHub App (issue #1262) between the vault, GitHub and the orchestration core: the APIs may
// not open a secret (issue #66), so they ask it through the control plane's adapters. It registers the App from its
// manifest, records where it is installed, gives the installations' tokens, receives the App's deliveries, and keeps
// and waits for the CI of pull requests.
type App struct {
	svc    *orchestration.Service
	vault  *vault.Service
	client Client
	tokens *Tokens
	// publicURL is the control plane's address as GitHub and the owner's browser reach it.
	publicURL string
	logger    *slog.Logger
	now       func() time.Time

	mu     sync.Mutex
	states map[string]time.Time
	// botLookup serializes backfilling the bot user ID on a credential already in the vault.
	botLookup sync.Mutex
	// waiters are the waits for the CI of a pull request, by its key, each woken when its CI changes.
	waiters map[string]map[chan struct{}]struct{}
}

// New returns the App over the orchestration core and the vault. publicURL is the control plane's address as GitHub
// and the owner's browser reach it; client calls GitHub.
func New(svc *orchestration.Service, secrets *vault.Service, publicURL string, client *http.Client,
	logger *slog.Logger,
) *App {
	c := Client{HTTP: client}

	return &App{
		svc: svc, vault: secrets, client: c, tokens: &Tokens{Client: c}, publicURL: publicURL,
		logger: logger, now: time.Now, states: map[string]time.Time{}, waiters: map[string]map[chan struct{}]struct{}{},
	}
}

func (a *App) store() *githubstore.Store { return a.svc.GitHub() }

// vaultError says what the vault refused, without a secret's value: the vault's errors never carry one.
func vaultError(err error) error {
	if errors.Is(err, vault.ErrNotConfigured) {
		return fmt.Errorf("%w: the vault has no key, so the GitHub App's credential cannot be kept; set "+
			"HYPOMNEMA_VAULT_KEYS (see `hypomnema secrets keygen`)", orchestration.ErrUnavailable)
	}

	return fmt.Errorf("keeping the GitHub App's credential: %w", err)
}

// errNoGitHubApp says no App is registered yet.
var errNoGitHubApp = fmt.Errorf("%w: no GitHub App is registered; the owner registers it at %s",
	orchestration.ErrNotFound, SetupPath)

// credential opens the App's credential, and false when none is registered.
func (a *App) credential(ctx context.Context) (Credential, bool, error) {
	cred, _, ok, err := a.credentialVersion(ctx)

	return cred, ok, err
}

func (a *App) credentialVersion(ctx context.Context) (Credential, int, bool, error) {
	opened, err := a.vault.Open(ctx, AppSecret)
	if errors.Is(err, vault.ErrNotFound) || errors.Is(err, vault.ErrNotConfigured) {
		return Credential{}, 0, false, nil
	}

	if err != nil {
		return Credential{}, 0, false, vaultError(err)
	}

	cred, err := DecodeCredential(opened.Payload[vault.KeyValue])
	if err != nil {
		return Credential{}, 0, false, err
	}

	return cred, opened.Version, true, nil
}

// Status is what the owner is shown of the App: never its key or secret.
type Status struct {
	Registered    bool
	AppID         int64
	Slug          string
	Owner         string
	HTMLURL       string
	InstallURL    string
	Installations []githubstore.Installation
	LastDelivery  *githubstore.Delivery
}

// Status says whether an App is registered, where it is installed, and the last delivery it was sent.
func (a *App) Status(ctx context.Context) (Status, error) {
	cred, ok, err := a.credential(ctx)
	if err != nil {
		return Status{}, err
	}

	st := Status{
		Registered: ok, AppID: cred.AppID, Slug: cred.Slug, Owner: cred.Owner, HTMLURL: cred.HTMLURL,
	}
	if cred.Slug != "" {
		st.InstallURL = "https://github.com/apps/" + cred.Slug + "/installations/new"
	}

	if st.Installations, err = a.store().Installations(ctx); err != nil {
		return Status{}, err
	}

	last, ok, err := a.store().LatestDelivery(ctx)
	if err != nil {
		return Status{}, err
	}

	if ok {
		st.LastDelivery = &last
	}

	return st, nil
}

// StartRegistration makes a registration for the owner's browser to post to GitHub: GitHub's page with a state good
// for registrationTTL and once, and the App's manifest as JSON. It needs the control plane's public HTTPS address,
// which GitHub sends the App's deliveries and the owner back to.
func (a *App) StartRegistration() (string, string, error) {
	public, err := url.Parse(a.publicURL)
	if a.publicURL == "" || err != nil || public.Scheme != "https" || public.Host == "" {
		return "", "", fmt.Errorf("%w: registering a GitHub App needs the control plane's public HTTPS address "+
			"(HYPOMNEMA_PUBLIC_URL), which GitHub sends its events to", orchestration.ErrUnavailable)
	}

	manifest, err := json.Marshal(Manifest(strings.TrimRight(a.publicURL, "/")))
	if err != nil {
		return "", "", fmt.Errorf("encoding the App's manifest: %w", err)
	}

	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("making the registration's state: %w", err)
	}

	state := base64.RawURLEncoding.EncodeToString(raw)

	a.mu.Lock()
	now := a.now()
	for s, until := range a.states {
		if !now.Before(until) {
			delete(a.states, s)
		}
	}

	a.states[state] = now.Add(registrationTTL)
	a.mu.Unlock()

	return RegistrationURL(state), string(manifest), nil
}

// CompleteRegistration takes GitHub's code back with the state the registration was started with, exchanges it for
// the App's credential and keeps that in the vault, replacing an App registered before. The state is good once. The
// credential's key and secret go from GitHub's answer to the vault only: what is returned, logged or shown is the
// App's id and name.
func (a *App) CompleteRegistration(ctx context.Context, state, code string) error {
	a.mu.Lock()
	until, ok := a.states[state]
	delete(a.states, state)
	a.mu.Unlock()

	if state == "" || !ok || !a.now().Before(until) {
		return fmt.Errorf("%w: this registration is not one the control plane started, or it "+
			"is over an hour old; start again at %s", orchestration.ErrInvalid, SetupPath)
	}

	// A browser that leaves must not lose the App's only credential: the exchange and its keeping go on without it,
	// as Omnara's callback does (github_app_setup_callback.go:120-123).
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()

	cred, err := a.client.Convert(ctx, code)
	if err != nil {
		return fmt.Errorf("exchanging GitHub's code for the App: %w", err)
	}

	// Keep the one-use registration even if GitHub does not expose the bot yet. The first App offer retries.
	lookup, stop := context.WithTimeout(ctx, gitHubRead)
	cred.BotUserID, _ = a.client.userID(lookup, cred.Slug+"[bot]", "")
	stop()

	value, err := cred.Encode()
	if err != nil {
		return err
	}

	payload := vault.Payload{vault.KeyValue: value}

	_, err = a.vault.Create(ctx, vault.NewSecret{
		Name: AppSecret, Kind: vault.KindGeneric, Payload: payload,
		Metadata: map[string]string{"integration": Host, "kind": "github-app"},
	})
	if errors.Is(err, vault.ErrConflict) {
		_, err = a.vault.Rotate(ctx, AppSecret, payload)
	}

	if err != nil {
		return vaultError(err)
	}

	a.tokens.Forget()

	// The App's id and name only.
	a.logger.InfoContext(ctx, "the owner's GitHub App is registered",
		slog.String("event", "githubapp.registered"), slog.Int64("app_id", cred.AppID), slog.String("app", cred.Slug))

	return nil
}

// SyncInstallations reads from GitHub the repositories the App is installed on and keeps them.
func (a *App) SyncInstallations(ctx context.Context) error {
	cred, ok, err := a.credential(ctx)
	if err != nil {
		return err
	}

	if !ok {
		return errNoGitHubApp
	}

	installs, err := a.client.Installations(ctx, cred)
	if err != nil {
		return fmt.Errorf("reading the App's installations: %w", err)
	}

	var out []githubstore.Installation

	for _, i := range installs {
		for _, repo := range i.Repositories {
			out = append(out, githubstore.Installation{
				Repository: repo.Name, RepositoryID: repo.ID, InstallationID: i.ID, Account: i.Account,
			})
		}
	}

	return a.store().SetInstallations(ctx, out)
}

// TestDelivery asks GitHub to send the App's latest delivery again, with its id, which is answered as handled before.
func (a *App) TestDelivery(ctx context.Context) (string, string, error) {
	cred, ok, err := a.credential(ctx)
	if err != nil {
		return "", "", err
	}

	if !ok {
		return "", "", errNoGitHubApp
	}

	d, err := a.client.Redeliver(ctx, cred)
	if err != nil {
		return "", "", fmt.Errorf("asking GitHub to deliver again: %w", err)
	}

	return d.GUID, d.Event, nil
}

// TokenFor is a token of the App's installation on a repository (host github.com, path owner/name), and false when
// no App is registered or none of its installations covers it: its work then uses the owner's token, as before.
func (a *App) TokenFor(ctx context.Context, host, path string) (Token, bool, error) {
	token, _, ok, err := a.tokenFor(ctx, host, path)

	return token, ok, err
}

func (a *App) tokenFor(ctx context.Context, host, path string) (Token, Credential, bool, error) {
	if a == nil || !strings.EqualFold(host, Host) {
		return Token{}, Credential{}, false, nil
	}

	installation, ok, err := a.store().InstallationOf(ctx, path)
	if err != nil || !ok {
		return Token{}, Credential{}, false, err
	}

	cred, ok, err := a.credential(ctx)
	if err != nil || !ok {
		return Token{}, Credential{}, false, err
	}

	token, err := a.tokens.Of(ctx, cred, installation.InstallationID, installation.RepositoryID)
	if err != nil {
		return Token{}, Credential{}, false, fmt.Errorf("making a token of the GitHub App's installation on %s: %w",
			path, err)
	}

	return token, cred, true, nil
}

// Covers says whether a repository's work goes with the App's tokens: an App is registered and installed on it.
func (a *App) Covers(ctx context.Context, host, path string) bool {
	if a == nil || !strings.EqualFold(host, Host) {
		return false
	}

	if _, ok, err := a.store().InstallationOf(ctx, path); err != nil || !ok {
		return false
	}

	_, ok, err := a.credential(ctx)

	return err == nil && ok
}

// conversations are the events whose body is kept, for issue #1232.
var conversations = map[string]bool{
	"issue_comment": true, "pull_request_review": true, "pull_request_review_comment": true,
}

// ServeEvents receives the App's deliveries (POST EventsPath). A delivery not signed with the App's
// secret is refused with 401 and changes nothing; one whose id was handled before is answered and not handled
// again. The rest is recorded, then acted on: the CI of a pull request someone waits for, a pull request that
// merged or closed, the App's installations.
func (a *App) ServeEvents(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxEventBytes))
	if err != nil {
		http.Error(w, "the delivery is too large", http.StatusRequestEntityTooLarge)

		return
	}

	cred, ok, err := a.credential(ctx)
	if err != nil {
		a.logger.ErrorContext(ctx, "the GitHub App's credential could not be read for a delivery",
			slog.String("event", "githubapp.delivery_failed"), slog.String("error", err.Error()))
		http.Error(w, "the delivery could not be checked", http.StatusServiceUnavailable)

		return
	}

	if !ok {
		http.Error(w, "no GitHub App is registered", http.StatusUnauthorized)

		return
	}

	ev, err := DecodeEvent(r.Header, body, cred.WebhookSecret)
	if errors.Is(err, ErrSignature) {
		http.Error(w, "the signature does not match", http.StatusUnauthorized)

		return
	}

	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)

		return
	}

	delivery := githubstore.Delivery{ID: ev.Delivery, Event: ev.Name, Action: ev.Action, Repository: ev.Repo()}
	if conversations[ev.Name] {
		delivery.Payload = string(body)
	}

	recorded, err := a.store().RecordDelivery(ctx, delivery)
	if err != nil {
		a.logger.ErrorContext(ctx, "recording a GitHub delivery", slog.String("event", "githubapp.delivery_failed"),
			slog.String("delivery", ev.Delivery), slog.String("error", err.Error()))
		http.Error(w, "the delivery could not be recorded", http.StatusInternalServerError)

		return
	}

	if !recorded {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "handled before\n")

		return
	}

	a.handle(ctx, cred, ev)
	w.WriteHeader(http.StatusAccepted)
}

// handle acts on a delivery recorded now. What fails is logged: the five-minute read of a waited pull request's
// checks, and the minute's read of a followed one, make up for it.
func (a *App) handle(ctx context.Context, cred Credential, ev Event) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), gitHubRead)
	defer cancel()

	var err error

	switch ev.Name {
	case "installation", "installation_repositories":
		err = a.SyncInstallations(ctx)
	case "check_suite", "workflow_run":
		suite := ev.CheckSuite
		if ev.Name == "workflow_run" {
			suite = ev.WorkflowRun
		}

		if ev.Action != "completed" || suite == nil {
			return
		}

		var errs []error
		for _, pr := range suite.PullRequests {
			errs = append(errs, a.ciEnded(ctx, ev, pr.Number))
		}

		err = errors.Join(errs...)
	case "pull_request":
		switch {
		case ev.PullRequest == nil:
		case ev.Action == "closed":
			err = a.closed(ctx, ev)
		case ev.Action == "synchronize" || ev.Action == "reopened":
			err = a.moved(ctx, ev)
		}
	}

	if err != nil {
		a.logger.WarnContext(ctx, "acting on a GitHub delivery", slog.String("event", "githubapp.delivery_failed"),
			slog.String("delivery", ev.Delivery), slog.String("kind", ev.Name), slog.String("error", err.Error()))
	}
}

// ciEnded reads again the checks of a pull request someone waits for, after a check suite or workflow run on it
// completed: one read of GitHub's with the repository's token, which says whether every check of its head ended.
func (a *App) ciEnded(ctx context.Context, ev Event, number int) error {
	repo := ev.Repo()

	ci, ok, err := a.store().CIOf(ctx, repo, number)
	if err != nil || !ok || ci.State == githubstore.CIMerged || ci.State == githubstore.CIClosed {
		return err
	}

	token, ok, err := a.TokenFor(ctx, Host, repo)
	if err != nil || !ok {
		return err
	}

	_, err = a.readCI(ctx, repo, number, token.Value)

	return err
}

// closed marks the CI of a pull request that merged or closed, and settles it when it is followed (#1053), with no
// read of GitHub's: the event says it.
func (a *App) closed(ctx context.Context, ev Event) error {
	pr := ev.PullRequest
	repo := ev.Repo()

	st := githost.RequestState{Status: githost.RequestClosed, Head: pr.Head.SHA, Branch: pr.Head.Ref}
	state := githubstore.CIClosed

	if pr.Merged {
		st.Status, state = githost.RequestMerged, githubstore.CIMerged
	}

	kept, err := a.store().SetCI(ctx, githubstore.CI{
		Repository: repo, Number: pr.Number, Head: pr.Head.SHA, State: state,
	}, false)
	if err != nil {
		return err
	}

	if kept {
		a.wake(repo, pr.Number)
	}

	_, err = a.svc.SettleClosedPullRequest(ctx, githost.Request{
		Repo: githost.Repo{Host: githost.GitHubHost, Path: repo}, Number: pr.Number,
	}, st)

	return err
}

// moved resets the CI of a pull request someone waits for when its head moves (synchronize) or it reopens: the CI
// kept was the old head's, so the next wait reads the new head's checks instead of answering the old head's
// (review round 1). Nothing is read here: a wait reads when it asks.
func (a *App) moved(ctx context.Context, ev Event) error {
	pr := ev.PullRequest
	repo := ev.Repo()

	ci, ok, err := a.store().CIOf(ctx, repo, pr.Number)
	if err != nil || !ok || (ev.Action == "synchronize" && strings.EqualFold(ci.Head, pr.Head.SHA)) {
		return err
	}

	kept, err := a.store().SetCI(ctx, githubstore.CI{
		Repository: repo, Number: pr.Number, Head: pr.Head.SHA, State: githubstore.CIPending,
	}, false)
	if err != nil {
		return err
	}

	if kept {
		a.wake(repo, pr.Number)
	}

	return nil
}

// readCI reads a pull request's checks with token and keeps what they say, unless an event changed its CI meanwhile,
// then wakes its waits. It returns the CI as kept.
func (a *App) readCI(ctx context.Context, repo string, number int, token string,
) (githubstore.CI, error) {
	ctx, cancel := context.WithTimeout(ctx, gitHubRead)
	defer cancel()

	// What the read finds is kept only if nothing changed the CI while GitHub answered (KeepRead).
	before, watched, err := a.store().CIOf(ctx, repo, number)
	if err != nil || !watched {
		return githubstore.CI{}, err
	}

	st, err := githost.ReadRequest(ctx, a.client.HTTP, githost.GitHub, githost.Request{
		Repo: githost.Repo{Host: githost.GitHubHost, Path: repo}, Number: number,
	}, token)
	if err != nil {
		// What GitHub answers may repeat the token: it never reaches a log.
		return githubstore.CI{}, errors.New(strings.ReplaceAll(err.Error(), token, "[token]"))
	}

	ci := githubstore.CI{Repository: repo, Number: number, Head: st.Head, State: ciStateOf(st), Completed: st.Completed}
	if ci.State == githubstore.CIFailed {
		ci.Failed = st.Failing
	}

	kept, err := a.store().KeepRead(ctx, ci, before.Version)
	if err != nil {
		return githubstore.CI{}, err
	}

	if kept {
		a.wake(repo, number)
	}

	// The CI as kept: what was read, or what an event wrote while GitHub was read, which the wait then reads again.
	got, _, err := a.store().CIOf(ctx, repo, number)

	return got, err
}

// ciStateOf is a pull request's CI from what was read of it: merged or closed; pending while a check it waits for
// runs or before any check is there; else failed when one failed, or passed. As `gh pr checks --watch`, the wait is
// for every check to end, not for the first failure.
func ciStateOf(st githost.RequestState) githubstore.CIState {
	switch {
	case st.Status == githost.RequestMerged:
		return githubstore.CIMerged
	case st.Status == githost.RequestClosed:
		return githubstore.CIClosed
	case st.Checks == 0 || len(st.Pending) > 0:
		return githubstore.CIPending
	case len(st.Failing) > 0:
		return githubstore.CIFailed
	default:
		return githubstore.CIPassed
	}
}

func ciKey(repo string, number int) string {
	return fmt.Sprintf("%s#%d", strings.ToLower(repo), number)
}

// listen is a channel closed when the CI of the pull request changes, and its end.
func (a *App) listen(repo string, number int) (<-chan struct{}, func()) {
	key, ch := ciKey(repo, number), make(chan struct{})

	a.mu.Lock()
	if a.waiters[key] == nil {
		a.waiters[key] = map[chan struct{}]struct{}{}
	}

	a.waiters[key][ch] = struct{}{}
	a.mu.Unlock()

	return ch, func() {
		a.mu.Lock()
		defer a.mu.Unlock()

		delete(a.waiters[key], ch)

		if len(a.waiters[key]) == 0 {
			delete(a.waiters, key)
		}
	}
}

// wake wakes every wait for the CI of the pull request.
func (a *App) wake(repo string, number int) {
	key := ciKey(repo, number)

	a.mu.Lock()
	defer a.mu.Unlock()

	for ch := range a.waiters[key] {
		close(ch)
	}

	delete(a.waiters, key)
}

// errNotCovered says the App does not cover a pull request's repository: its CI is waited for with gh.
var errNotCovered = fmt.Errorf("%w: the GitHub App is not installed on this pull request's repository; wait with "+
	"`gh pr checks <url> --required --watch`", orchestration.ErrConflict)

// WaitCI waits, at most hold, for the CI of a pull request of a repository the App is installed on, and returns it
// as it is then. The first wait for a pull request reads its checks; after that only the App's events change it, and
// a read when none came for ciReadEvery. GitHub is never called by the one who waits.
func (a *App) WaitCI(ctx context.Context, ref string, hold time.Duration) (ciwait.State, error) {
	r, ok := githost.ParseRequest(ref, func(host string) (githost.Kind, bool) {
		return githost.GitHub, host == githost.GitHubHost
	})
	if !ok {
		return ciwait.State{}, fmt.Errorf("%w: %q is not a pull request on github.com", orchestration.ErrInvalid, ref)
	}

	repo, ok := RepositoryName(r.Repo.Path)
	if !ok {
		return ciwait.State{}, fmt.Errorf("%w: %q is not a pull request on github.com", orchestration.ErrInvalid, ref)
	}

	link := "https://github.com/" + r.Repo.Path + "/pull/" + fmt.Sprint(r.Number)

	if !a.Covers(ctx, Host, repo) {
		return ciwait.State{}, errNotCovered
	}

	ci, err := a.store().WatchCI(ctx, repo, r.Number)
	if err != nil {
		return ciwait.State{}, err
	}

	deadline := time.NewTimer(hold)
	defer deadline.Stop()

	for {
		woken, stop := a.listen(repo, r.Number)

		// A state is read again when it is ciReadEvery old, a passed or failed one too: its head may have moved
		// without an event saying so (review round 1). A merge or a close is final until a reopen resets it.
		if !final(ci.State) && a.now().Sub(ci.ReadAt) >= ciReadEvery {
			token, covered, err := a.TokenFor(ctx, Host, repo)
			if err == nil && !covered {
				err = errNotCovered
			}

			if err == nil {
				ci, err = a.readCI(ctx, repo, r.Number, token.Value)
			}

			if err != nil {
				stop()

				return ciwait.State{}, fmt.Errorf("reading the checks of %s: %w", link, err)
			}
		}

		if ci.State.Done() {
			stop()

			return stateOf(link, ci), nil
		}

		// The next read is due ciReadEvery after the last, if no event comes before.
		read := time.NewTimer(ciReadEvery - a.now().Sub(ci.ReadAt))

		select {
		case <-woken:
		case <-read.C:
		case <-deadline.C:
			read.Stop()
			stop()

			return stateOf(link, ci), nil
		case <-ctx.Done():
			read.Stop()
			stop()

			return ciwait.State{}, ctx.Err()
		}

		read.Stop()
		stop()

		if ci, _, err = a.store().CIOf(ctx, repo, r.Number); err != nil {
			return ciwait.State{}, err
		}
	}
}

// final says a pull request's CI is over for good: it merged or closed.
func final(s githubstore.CIState) bool { return s == githubstore.CIMerged || s == githubstore.CIClosed }

func stateOf(link string, ci githubstore.CI) ciwait.State {
	return ciwait.State{PullRequest: link, Head: ci.Head, State: string(ci.State), Failed: ci.Failed}
}

// TokenOfDispatch is a token of the App's installation on a Dispatch's repository, for its worker's gh and git, and
// false when the App does not cover it.
func (a *App) TokenOfDispatch(ctx context.Context, dispatchID string) (agenttool.GitHubToken, bool, error) {
	d, err := a.svc.GetDispatch(ctx, dispatchID)
	if err != nil {
		return agenttool.GitHubToken{}, false, err
	}

	task, err := a.svc.GetTask(ctx, d.TaskID)
	if err != nil {
		return agenttool.GitHubToken{}, false, err
	}

	run, err := a.svc.GetRun(ctx, task.RunID)
	if err != nil {
		return agenttool.GitHubToken{}, false, err
	}

	token, ok, err := a.TokenFor(ctx, run.RepoHost, run.Repo)
	if err != nil || !ok {
		return agenttool.GitHubToken{}, false, err
	}

	return agenttool.GitHubToken{Token: token.Value, ExpiresAt: token.ExpiresAt}, true, nil
}

// FindRequest finds and reads a followed pull request (githost.FindRequest) with the App's token, when the App covers
// its repository, so the control plane's own reads count against the App's limit; false when it does not cover it.
func (a *App) FindRequest(ctx context.Context, kind githost.Kind, repo githost.Repo, branch string,
	hint *githost.Request,
) (githost.Request, githost.RequestState, bool, error) {
	if a == nil || kind != githost.GitHub {
		return githost.Request{}, githost.RequestState{}, false, nil
	}

	token, ok, err := a.TokenFor(ctx, repo.Host, repo.Path)
	if err != nil || !ok {
		return githost.Request{}, githost.RequestState{}, false, nil
	}

	ctx, cancel := context.WithTimeout(ctx, gitHubRead)
	defer cancel()

	r, st, err := githost.FindRequest(ctx, a.client.HTTP, kind, repo, branch, hint, token.Value)
	if err != nil && !errors.Is(err, githost.ErrNoRequest) {
		// What GitHub answers may repeat the token: it never reaches a log.
		err = errors.New(strings.ReplaceAll(err.Error(), token.Value, "[token]"))
	}

	return r, st, true, err
}
