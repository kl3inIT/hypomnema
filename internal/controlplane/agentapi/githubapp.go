package agentapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
	"github.com/kl3inIT/hypomnema/internal/shared/ciwait"
)

// GitHubApp is the owner's GitHub App as a worker reaches it (issue #1262): a token of the App's installation on its
// Dispatch's repository, and the wait for a pull request's CI. It is the control plane's own wiring over the vault:
// this package never opens a secret.
type GitHubApp interface {
	// TokenOfDispatch is a fresh token of the App's installation on the Dispatch's repository, and false when the
	// App does not cover it.
	TokenOfDispatch(ctx context.Context, dispatchID string) (agenttool.GitHubToken, bool, error)
	// WaitCI waits at most hold for the CI of a pull request and returns it.
	WaitCI(ctx context.Context, ref string, hold time.Duration) (ciwait.State, error)
}

// WithGitHubApp gives workers the owner's GitHub App's tokens and CI waits. Without it both routes answer that there
// is none.
func (s *Server) WithGitHubApp(app GitHubApp) *Server {
	s.githubApp = app

	return s
}

// githubToken gives a worker a token of the App's installation on its Run's repository, and no other agent: as the
// host's token goes only with the offer of a Dispatch on that host (nodeapi), as the SSH key does (gitSSHKeyFor).
// `hypomnema-node gh` and `hypomnema-node git-credential` ask it at each call, as Omnara's agents ask their daemon
// for a fresh installation token (machinedaemon/git_credential_helper.go).
func (s *Server) githubToken(w http.ResponseWriter, r *http.Request) {
	a, err := s.authenticate(r)
	if err != nil {
		refuse(w, http.StatusUnauthorized, err.Error())

		return
	}

	if s.githubApp == nil {
		refuse(w, http.StatusServiceUnavailable, "this control plane has no GitHub App")

		return
	}

	if a.dispatchID == "" {
		refuse(w, http.StatusForbidden, "only a worker on a repository the GitHub App is installed on gets its token")

		return
	}

	token, ok, err := s.githubApp.TokenOfDispatch(r.Context(), a.dispatchID)
	if err != nil {
		s.fail(w, r, err)

		return
	}

	if !ok {
		refuse(w, http.StatusConflict, "the GitHub App is not installed on this worker's repository")

		return
	}

	// Who got a token, and never the token.
	s.logger.DebugContext(r.Context(), "a worker got a token of the GitHub App",
		slog.String("event", "agentapi.github_token"), slog.String("agent", a.name))

	w.Header().Set("Cache-Control", "no-store")
	answer(w, token)
}

// ciWait holds a worker's ask for a pull request's CI until it changes or QueryWait seconds pass (at most
// ciwait.Hold), and answers it as it is then: `hypomnema ci wait` asks again until it is done.
func (s *Server) ciWait(w http.ResponseWriter, r *http.Request) {
	a, err := s.authenticate(r)
	if err != nil {
		refuse(w, http.StatusUnauthorized, err.Error())

		return
	}

	if s.githubApp == nil {
		refuse(w, http.StatusServiceUnavailable, "this control plane has no GitHub App")

		return
	}

	if a.dispatchID == "" {
		refuse(w, http.StatusForbidden, "a coordinator waits with the client API's `hypomnema ci wait`")

		return
	}

	hold := ciwait.Hold
	if raw := r.URL.Query().Get(agenttool.QueryWait); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			refuse(w, http.StatusBadRequest, "wait is a number of seconds")

			return
		}

		hold = min(time.Duration(n)*time.Second, ciwait.Hold)
	}

	// The wait outlives the server's write timeout.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})

	st, err := s.githubApp.WaitCI(r.Context(), r.URL.Query().Get(agenttool.QueryPullRequest), hold)
	if errors.Is(err, orchestration.ErrInvalid) {
		refuse(w, http.StatusBadRequest, err.Error())

		return
	}

	if err != nil {
		s.fail(w, r, err)

		return
	}

	answer(w, st)
}
