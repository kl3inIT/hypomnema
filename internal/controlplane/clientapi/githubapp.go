package clientapi

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/githubstore"
	"github.com/kl3inIT/hypomnema/internal/shared/ciwait"
)

// The owner's GitHub App (issue #1262). The contract's routes give its state to the CLI (`hypomnema hosts app`) and
// wait for a pull request's CI (`hypomnema ci wait`). Registering and installing it happen in the owner's signed-in
// browser, which GitHub sends back to the control plane: those are pages outside the contract, as the WebSockets
// are, each checked by the session's cookie, which a top-level navigation from github.com carries (SameSite=Lax).

// Where the App's pages are, under BasePath (githubapp's SetupPath, CallbackPath and InstalledPath).
const (
	gitHubAppSetupPath     = BasePath + "/integrations/github/app/setup"
	gitHubAppCallbackPath  = BasePath + "/integrations/github/app/callback"
	gitHubAppInstalledPath = BasePath + "/integrations/github/app/installed"
)

// GitHubApp is the owner's GitHub App as the API reaches it: the control plane's own wiring over the vault, which
// this package never opens (issue #66).
type GitHubApp interface {
	Status(ctx context.Context) (GitHubAppStatus, error)
	// StartRegistration gives GitHub's registration page, with a state good once, and the App's manifest as JSON.
	StartRegistration() (registrationURL, manifest string, err error)
	// CompleteRegistration exchanges GitHub's code and keeps the App's credential in the vault.
	CompleteRegistration(ctx context.Context, state, code string) error
	SyncInstallations(ctx context.Context) error
	// TestDelivery asks GitHub to send the App's latest delivery again.
	TestDelivery(ctx context.Context) (guid, event string, err error)
	WaitCI(ctx context.Context, ref string, hold time.Duration) (ciwait.State, error)
}

// GitHubAppStatus is what the owner is shown of the App: never its key or secret.
type GitHubAppStatus struct {
	Registered    bool
	AppID         int64
	Slug          string
	Owner         string
	HTMLURL       string
	InstallURL    string
	Installations []githubstore.Installation
	LastDelivery  *githubstore.Delivery
}

// WithGitHubApp sets the owner's GitHub App and the public URL of its setup page.
func (s *Server) WithGitHubApp(app GitHubApp, publicURL string) *Server {
	s.githubApp = app
	s.githubAppPublicHost = ""
	if u, err := url.Parse(publicURL); err == nil {
		s.githubAppPublicHost = u.Host
	}

	return s
}

var errNoGitHubAppWired = fmt.Errorf("%w: this control plane has no GitHub App", orchestration.ErrUnavailable)

func (s *Server) gitHubAppView(st GitHubAppStatus) openapi.GitHubApp {
	out := openapi.GitHubApp{
		Registered: st.Registered, AppId: ptr(st.AppID), Slug: ptr(st.Slug), Owner: ptr(st.Owner),
		HtmlUrl: ptr(st.HTMLURL), InstallUrl: ptr(st.InstallURL), SetupUrl: gitHubAppSetupPath,
		Installations: make([]openapi.GitHubAppInstallation, 0, len(st.Installations)),
	}

	for _, i := range st.Installations {
		out.Installations = append(out.Installations, openapi.GitHubAppInstallation{
			Repository: i.Repository, InstallationId: i.InstallationID, Account: i.Account,
		})
	}

	if d := st.LastDelivery; d != nil {
		out.LastDelivery = &openapi.GitHubDelivery{
			Id: d.ID, Event: d.Event, Action: ptr(d.Action), Repository: ptr(d.Repository), ReceivedAt: d.ReceivedAt,
		}
	}

	return out
}

// GetGitHubApp implements openapi.StrictServerInterface.
func (s *Server) GetGitHubApp(ctx context.Context, _ openapi.GetGitHubAppRequestObject,
) (openapi.GetGitHubAppResponseObject, error) {
	if s.githubApp == nil {
		return nil, errNoGitHubAppWired
	}

	st, err := s.githubApp.Status(ctx)
	if err != nil {
		return nil, err
	}

	return openapi.GetGitHubApp200JSONResponse(s.gitHubAppView(st)), nil
}

// SyncGitHubAppInstallations implements openapi.StrictServerInterface.
func (s *Server) SyncGitHubAppInstallations(ctx context.Context, _ openapi.SyncGitHubAppInstallationsRequestObject,
) (openapi.SyncGitHubAppInstallationsResponseObject, error) {
	if s.githubApp == nil {
		return nil, errNoGitHubAppWired
	}

	if err := s.githubApp.SyncInstallations(ctx); err != nil {
		return nil, err
	}

	st, err := s.githubApp.Status(ctx)
	if err != nil {
		return nil, err
	}

	return openapi.SyncGitHubAppInstallations200JSONResponse(s.gitHubAppView(st)), nil
}

// TestGitHubAppDelivery implements openapi.StrictServerInterface.
func (s *Server) TestGitHubAppDelivery(ctx context.Context, _ openapi.TestGitHubAppDeliveryRequestObject,
) (openapi.TestGitHubAppDeliveryResponseObject, error) {
	if s.githubApp == nil {
		return nil, errNoGitHubAppWired
	}

	guid, event, err := s.githubApp.TestDelivery(ctx)
	if err != nil {
		return nil, err
	}

	return openapi.TestGitHubAppDelivery200JSONResponse{Guid: guid, Event: event}, nil
}

// WaitPullRequestCI implements openapi.StrictServerInterface.
func (s *Server) WaitPullRequestCI(ctx context.Context, request openapi.WaitPullRequestCIRequestObject,
) (openapi.WaitPullRequestCIResponseObject, error) {
	if s.githubApp == nil {
		return nil, errNoGitHubAppWired
	}

	hold := ciwait.Hold
	if w := request.Params.Wait; w != nil {
		hold = min(time.Duration(*w)*time.Second, ciwait.Hold)
	}

	st, err := s.githubApp.WaitCI(ctx, request.Params.PullRequest, hold)
	if err != nil {
		return nil, err
	}

	out := openapi.WaitPullRequestCI200JSONResponse{
		PullRequest: st.PullRequest, Head: ptr(st.Head), State: openapi.PullRequestCIState(st.State),
	}
	if len(st.Failed) > 0 {
		out.Failed = &st.Failed
	}

	return out, nil
}

// registerGitHubAppPages adds the App's browser pages to mux.
func (s *Server) registerGitHubAppPages(mux *http.ServeMux) {
	mux.HandleFunc("GET "+gitHubAppSetupPath, s.gitHubAppSetup)
	mux.HandleFunc("POST "+gitHubAppSetupPath, s.gitHubAppAct)
	mux.HandleFunc("GET "+gitHubAppCallbackPath, s.gitHubAppCallback)
	mux.HandleFunc("GET "+gitHubAppInstalledPath, s.gitHubAppInstalled)
}

// ownerPage authenticates a page's request as the owner's, by its session or its token, and answers it when it is
// not; false then.
func (s *Server) ownerPage(w http.ResponseWriter, r *http.Request) bool {
	pageHeaders(w)

	if s.githubApp == nil {
		http.Error(w, "this control plane has no GitHub App", http.StatusServiceUnavailable)

		return false
	}

	tok, _, err := s.authenticate(r.Context(), r)
	if err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		_ = pageTemplate.Execute(w, page{Message: "Sign in to Hypomnema in this browser first, then open this page again."})

		return false
	}

	if tok.Kind != bearertoken.Owner {
		http.Error(w, "only the owner registers the GitHub App", http.StatusForbidden)

		return false
	}

	return true
}

// pageHeaders keeps the App's pages out of caches and frames, and lets their one form go to GitHub.
func pageHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; "+
		"form-action 'self' https://github.com; frame-ancestors 'none'; base-uri 'none'")
}

// gitHubAppMessages are what a page says after an action, by the code the action redirected with: never text from
// the address itself.
var gitHubAppMessages = map[string]string{
	"registered": "The App is registered and its credential is in the vault. Install it on the repositories " +
		"Runs use.",
	"installed": "The App's installations were read again.",
	"tested": "GitHub was asked to send the App's latest delivery again. It keeps its id, so it is answered as " +
		"handled before and Last below stays; the App's Recent Deliveries on GitHub shows the answer.",
	"failed":        "That did not work; the control plane's log says why.",
	"expired":       "That registration is not one this control plane started, or it is over an hour old. Start again.",
	"no_code":       "GitHub sent no code back: the App was not registered. Start again.",
	"not_same_site": "That form was not sent from this page.",
}

// gitHubAppSetup is the owner's page: the App as it is, and the form that posts its manifest to GitHub (the
// comment's step 1), which GitHub's docs give as the way to register an App from a manifest.
func (s *Server) gitHubAppSetup(w http.ResponseWriter, r *http.Request) {
	if !s.ownerPage(w, r) {
		return
	}

	p := page{Message: gitHubAppMessages[r.URL.Query().Get("done")]}

	st, err := s.githubApp.Status(r.Context())
	if err != nil {
		s.pageFailed(w, r, "reading the GitHub App", err)

		return
	}

	p.Status = st

	p.RegistrationURL, p.Manifest, err = s.githubApp.StartRegistration()
	if err != nil {
		p.Problem = err.Error()
	}

	_ = pageTemplate.Execute(w, p)
}

// gitHubAppAct runs a button of the page: read the installations again, or ask for a test delivery.
func (s *Server) gitHubAppAct(w http.ResponseWriter, r *http.Request) {
	if !s.ownerPage(w, r) {
		return
	}

	if !sameSite(r, s.githubAppPublicHost) {
		redirectPage(w, r, "not_same_site")

		return
	}

	var (
		err  error
		done string
	)

	switch r.FormValue("action") {
	case "sync":
		err, done = s.githubApp.SyncInstallations(r.Context()), "installed"
	case "test":
		_, _, err = s.githubApp.TestDelivery(r.Context())
		done = "tested"
	default:
		http.Error(w, "no such action", http.StatusBadRequest)

		return
	}

	if err != nil {
		s.logger.WarnContext(r.Context(), "a GitHub App action failed", slog.String("event", "clientapi.github_app"),
			slog.String("error", err.Error()))

		done = "failed"
	}

	redirectPage(w, r, done)
}

// gitHubAppCallback is where GitHub sends the owner back with the manifest's code (the comment's step 2): it is
// exchanged, and the App's credential kept in the vault. Nothing of the credential is answered or logged here.
func (s *Server) gitHubAppCallback(w http.ResponseWriter, r *http.Request) {
	if !s.ownerPage(w, r) {
		return
	}

	q := r.URL.Query()

	code := q.Get("code")
	if len(q["code"]) != 1 || code == "" {
		redirectPage(w, r, "no_code")

		return
	}

	err := s.githubApp.CompleteRegistration(r.Context(), q.Get("state"), code)

	switch {
	case errors.Is(err, orchestration.ErrInvalid):
		redirectPage(w, r, "expired")
	case err != nil:
		s.logger.WarnContext(r.Context(), "the GitHub App could not be registered",
			slog.String("event", "clientapi.github_app"), slog.String("error", err.Error()))
		redirectPage(w, r, "failed")
	default:
		redirectPage(w, r, "registered")
	}
}

// gitHubAppInstalled is the App's setup URL: GitHub sends the owner there after he installs it or changes its
// repositories, and its installations are read again (the comment's step 3).
func (s *Server) gitHubAppInstalled(w http.ResponseWriter, r *http.Request) {
	if !s.ownerPage(w, r) {
		return
	}

	if err := s.githubApp.SyncInstallations(r.Context()); err != nil {
		s.logger.WarnContext(r.Context(), "the GitHub App's installations could not be read",
			slog.String("event", "clientapi.github_app"), slog.String("error", err.Error()))
		redirectPage(w, r, "failed")

		return
	}

	redirectPage(w, r, "installed")
}

func (s *Server) pageFailed(w http.ResponseWriter, r *http.Request, what string, err error) {
	s.logger.ErrorContext(r.Context(), what, slog.String("event", "clientapi.github_app"),
		slog.String("error", err.Error()))
	http.Error(w, what+" failed", http.StatusInternalServerError)
}

func redirectPage(w http.ResponseWriter, r *http.Request, done string) {
	http.Redirect(w, r, gitHubAppSetupPath+"?"+url.Values{"done": {done}}.Encode(), http.StatusSeeOther)
}

// sameSite checks the browser's fetch metadata first, as net/http.CrossOriginProtection does.
// Without it, the Origin may name the public host even when a proxy rewrites the request Host.
func sameSite(r *http.Request, publicHost string) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin":
		return true
	case "":
		// Fall back to Origin.
	default:
		return false
	}

	origin := r.Header.Get("Origin")
	if origin == "" {
		// Like net/http.CrossOriginProtection, allow requests with neither header.
		return true
	}

	u, err := url.Parse(origin)

	return err == nil && u.Host != "" && (u.Host == r.Host || u.Host == publicHost)
}

// page is what the App's page shows.
type page struct {
	Message         string
	Problem         string
	Status          GitHubAppStatus
	RegistrationURL string
	Manifest        string
}

var pageTemplate = template.Must(template.New("github-app").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>GitHub App · Hypomnema</title>
<style>
body{font:15px/1.5 system-ui,sans-serif;max-width:46rem;margin:2rem auto;padding:0 1rem;color:#1f2328}
h1{font-size:1.4rem}h2{font-size:1.1rem;margin-top:1.6rem}code{background:#f3f4f6;padding:0 .25rem;border-radius:3px}
.note{background:#eef6ff;border:1px solid #b6d4fe;padding:.6rem .8rem;border-radius:6px}
.problem{background:#fff4e5;border:1px solid #f5c77e;padding:.6rem .8rem;border-radius:6px}
button{font:inherit;padding:.4rem .9rem;border-radius:6px;border:1px solid #1f883d;background:#1f883d;color:#fff;
cursor:pointer}
button.plain{background:#fff;color:#1f2328;border-color:#d0d7de}
form{display:inline-block;margin:.3rem .4rem .3rem 0}table{border-collapse:collapse}td{padding:.15rem .8rem .15rem 0}
</style></head><body>
<h1>GitHub App</h1>
{{with .Message}}<p class="note">{{.}}</p>{{end}}
{{if .Status.Registered}}
<p>Registered: <a href="{{.Status.HTMLURL}}">{{.Status.Slug}}</a> (App {{.Status.AppID}}, owned by {{.Status.Owner}}).
Its private key and webhook secret are in the vault and are never shown.</p>
<h2>Installations</h2>
{{if .Status.Installations}}<table>{{range .Status.Installations}}<tr><td><code>{{.Repository}}</code></td>
<td>installation {{.InstallationID}}</td><td>{{.Account}}</td></tr>{{end}}</table>
{{else}}<p>Not installed on any repository yet.</p>{{end}}
<p><a href="{{.Status.InstallURL}}">Install it on repositories, or change which</a>; GitHub sends you back here.</p>
<form method="post"><input type="hidden" name="action" value="sync">
<button class="plain">Read the installations again</button></form>
<h2>Deliveries</h2>
{{with .Status.LastDelivery}}<p>Last: <code>{{.Event}}</code>{{with .Action}} {{.}}{{end}}
{{- with .Repository}} on <code>{{.}}</code>{{end}},
{{.ReceivedAt.UTC.Format "2006-01-02 15:04:05 UTC"}}.</p>{{else}}<p>None received yet.</p>{{end}}
<form method="post"><input type="hidden" name="action" value="test">
<button class="plain">Send a test delivery</button></form>
{{end}}
{{if .Problem}}<p class="problem">{{.Problem}}</p>
{{else if .RegistrationURL}}
<h2>{{if .Status.Registered}}Register a new App{{else}}Register{{end}}</h2>
<p>{{if .Status.Registered}}A new App replaces this one in the vault.{{else}}GitHub registers a private App on your
account with the permissions and events Hypomnema uses; you choose its name there.{{end}}</p>
<form method="post" action="{{.RegistrationURL}}"><input type="hidden" name="manifest" value="{{.Manifest}}">
<button>Create GitHub App</button></form>
{{end}}
</body></html>
`))
