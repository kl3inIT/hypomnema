package agenttool

import "time"

// The GitHub App's routes of the agent API (issue #1262), which a worker calls with its Dispatch's key.
const (
	// PathGitHubToken gives a worker a token of the GitHub App's installation on its Run's repository, asked at each
	// gh and git call (`hypomnema-node gh`, `hypomnema-node git-credential`), as Omnara's agents ask their daemon
	// (machinedaemon/git_credential_helper.go).
	PathGitHubToken = "/agent/v1/github/token" //nolint:gosec // a route, not a credential
	// PathGitHubPRCreateToken gives the App-backed worker its repository's owner Connection for gh pr create.
	PathGitHubPRCreateToken = "/agent/v1/github/pr-create-token" //nolint:gosec // a route, not a credential
	// PathCIWait waits a while for the CI of a pull request to end (`hypomnema ci wait`), without the worker calling
	// GitHub: the App's events end it. Its parameters are QueryPullRequest and QueryWait.
	PathCIWait = "/agent/v1/ci/wait"
)

// The parameters of PathCIWait, and of the client API's route of the same wait.
const (
	QueryPullRequest = "pull_request"
	QueryWait        = "wait"
)

// GitHubToken answers PathGitHubToken.
type GitHubToken struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// String keeps a token out of logs.
func (t GitHubToken) String() string { return "GitHub token until " + t.ExpiresAt.Format(time.RFC3339) }
