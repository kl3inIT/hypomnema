package githubapp_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/node/githubapp"
	"github.com/kl3inIT/hypomnema/internal/node/worktree"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// helperEnv makes this test binary run as the node binary's helpers, as the scripts Prepare writes call it.
const helperEnv = "HYPOMNEMA_TEST_GITHUBAPP_HELPER"

func TestMain(m *testing.M) {
	if os.Getenv(helperEnv) == "1" {
		os.Exit(helper(os.Args[1:]))
	}

	os.Exit(m.Run())
}

func helper(args []string) int {
	if len(args) == 0 {
		return 2
	}

	var err error

	ctx := context.Background()

	switch args[0] {
	case githubapp.GHSubcommand:
		err = githubapp.MainGH(ctx, args[1:])
	case githubapp.CredentialSubcommand:
		err = githubapp.MainCredential(ctx, args[1:], os.Stdin, os.Stdout)
	case githubapp.CLISubcommand:
		err = githubapp.MainCLI(ctx, args[1:], os.Stdout, os.Stderr)
	default:
		err = errors.New("no such helper")
	}

	var exit *agenttool.ExitError
	if errors.As(err, &exit) {
		return exit.Code
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, err)

		return 1
	}

	return 0
}

// agentAPI answers the worker's asks for a fresh token of the App's with the Dispatch's key.
func agentAPI(t *testing.T, token string) (*httptest.Server, *atomic.Int32) {
	t.Helper()

	var asked atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != agenttool.PathGitHubToken ||
			r.Header.Get("Authorization") != "Bearer the-dispatch-key" {
			http.NotFound(w, r)

			return
		}

		asked.Add(1)
		_ = json.NewEncoder(w).Encode(agenttool.GitHubToken{Token: token, ExpiresAt: time.Now().Add(time.Hour)})
	}))
	t.Cleanup(srv.Close)

	return srv, &asked
}

// worker is the environment a worker on kl3inIT/hypomnema is started with when its offer is the GitHub App's: the
// offer carries no token (GitHubApp, RepoToken empty), and the node's supervisor gives it the App's commands. script
// is the fake gh first on its PATH.
func worker(t *testing.T, apiURL, script string) []string {
	t.Helper()

	fake := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(fake, "gh"), []byte(script), 0o700)) //nolint:gosec // G306: runs

	src, err := worktree.SourceOf("github.com", "github", "kl3inIT/hypomnema", "")
	require.NoError(t, err)

	app, err := githubapp.Prepare(t.TempDir(), os.Args[0], apiURL, "the-dispatch-key",
		fake+string(os.PathListSeparator)+os.Getenv("PATH"))
	require.NoError(t, err)

	env := append(githubapp.WithoutHostToken(src.WorkerEnv()), app...)

	return append(env, helperEnv+"=1", "HOME="+t.TempDir(), "GIT_TERMINAL_PROMPT=0")
}

// sayToken is a gh that says which token it was run with.
const sayToken = "#!/bin/sh\necho \"GH_TOKEN=$GH_TOKEN args=$*\"\n" //nolint:gosec // G101: a script, no credential

func run(t *testing.T, env []string, stdin string, name string, args ...string) string {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), name, args...) //nolint:gosec // G204: the test's own commands
	cmd.Env, cmd.Stdin = env, strings.NewReader(stdin)

	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))

	return string(out)
}

// Scenario (issue #1262): a worker on a repository the owner's GitHub App is installed on runs gh and git; each call
// carries a fresh token of the App's installation, asked of the control plane with the Dispatch's key, and never the
// owner's token nor the one its offer carried an hour ago.
func TestAWorkersGhAndGitCarryTheAppsFreshTokenNotTheOwners(t *testing.T) {
	t.Parallel()

	api, asked := agentAPI(t, "ghs_a_fresh_installation_token")
	env := worker(t, api.URL, sayToken)

	// No token of the App's is in what the worker starts with, which the shim's spec file keeps: gh and git ask for
	// one at each call (review round 1).
	for _, kv := range env {
		assert.NotContains(t, kv, "ghs_", "no token in the worker's environment")
		assert.False(t, strings.HasPrefix(kv, "GH_TOKEN="), "gh gets its token at each call")
		assert.False(t, strings.HasPrefix(kv, "HYPOMNEMA_GITHUB_OFFER_TOKEN="), "no offer's token is kept for it")
	}

	// The worker runs gh as it does, from its PATH.
	out := run(t, env, "", "sh", "-c", "gh pr checks https://github.com/kl3inIT/hypomnema/pull/1")
	assert.Equal(t, "GH_TOKEN=ghs_a_fresh_installation_token args=pr checks https://github.com/kl3inIT/hypomnema/pull/1\n",
		out)

	// git asks its credential helper for github.com.
	out = run(t, env, "protocol=https\nhost=github.com\n\n", "git", "credential", "fill")
	assert.Contains(t, out, "username=x-access-token\n")
	assert.Contains(t, out, "password=ghs_a_fresh_installation_token\n")

	assert.Equal(t, int32(2), asked.Load(), "one ask a call")
}

// Scenario (issue #1262, review rounds 1 and 2): the node reconnected to a control plane of an earlier release, which
// has no route for a fresh token, or the App was uninstalled while the worker ran. No token is kept on the node to
// fall back on, and the machine's own login may reach more than the owner's Connection: the worker's gh fails
// closed, saying why, and does not run, as Omnara's RunGitHubCLI returns its credential error.
func TestWithoutAnAppTokenTheWorkersGhFailsClosed(t *testing.T) {
	t.Parallel()

	for _, api := range []http.Handler{
		http.NotFoundHandler(),
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"message":"the GitHub App is not installed on this worker's repository"}`))
		}),
	} {
		srv := httptest.NewServer(api)
		t.Cleanup(srv.Close)

		cmd := exec.CommandContext(t.Context(), "sh", "-c", "gh api user")
		cmd.Env = worker(t, srv.URL, sayToken)

		var stdout, stderr strings.Builder
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		require.Error(t, cmd.Run())

		assert.Empty(t, stdout.String(), "gh did not run")
		assert.Contains(t, stderr.String(), "gh does not run with this machine's own login")
	}
}

// Scenario (issue #1262, review round 1): the App does not cover the pull request's repository, so the worker's
// `hypomnema ci wait` waits with gh, polling once a minute (--interval 60). Its --timeout still bounds the wait: a
// gh that does not answer is stopped, and the command says the checks are pending (exit 8).
func TestTheWorkersGhFallbackOfCIWaitHonoursItsTimeout(t *testing.T) {
	t.Parallel()

	// The App covers the worker's repository (its gh gets a token) but not the pull request's, which the wait asks of.
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == agenttool.PathGitHubToken {
			_ = json.NewEncoder(w).Encode(agenttool.GitHubToken{Token: "ghs_x", ExpiresAt: time.Now().Add(time.Hour)})

			return
		}

		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"message":"the GitHub App is not installed on this pull request's repository"}`))
	}))
	t.Cleanup(api.Close)

	args := filepath.Join(t.TempDir(), "args")
	env := worker(t, api.URL, "#!/bin/sh\necho \"$*\" > '"+args+"'\nexec sleep 30\n")

	start := time.Now()
	cmd := exec.CommandContext(t.Context(), "sh", "-c",
		"hypomnema ci wait https://github.com/kl3inIT/hypomnema/pull/1 --timeout 1s")
	cmd.Env = env

	out, err := cmd.CombinedOutput()

	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit, string(out))
	assert.Equal(t, 8, exit.ExitCode(), string(out))
	assert.Less(t, time.Since(start), 10*time.Second, "the timeout stopped gh")

	sent, err := os.ReadFile(args) //nolint:gosec // the test's own file
	require.NoError(t, err)
	assert.Equal(t, "pr checks https://github.com/kl3inIT/hypomnema/pull/1 --required --watch --interval 60\n",
		string(sent))
}
