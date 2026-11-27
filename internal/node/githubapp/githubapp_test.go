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

// worker is the environment a worker on kl3inIT/hypomnema is started with when its offer carries the owner's token
// as RepoToken and, being the App's, GitHubApp: what the node's supervisor gives it.
func worker(t *testing.T, apiURL string) []string {
	t.Helper()

	// A gh that says which token it was run with.
	fake := t.TempDir()
	script := []byte("#!/bin/sh\necho \"GH_TOKEN=$GH_TOKEN args=$*\"\n")
	require.NoError(t, os.WriteFile(filepath.Join(fake, "gh"), script, 0o700)) //nolint:gosec // G306: a script runs

	src, err := worktree.SourceOf("github.com", "github", "kl3inIT/hypomnema", "ghs_the_offers_token")
	require.NoError(t, err)

	app, err := githubapp.Prepare(t.TempDir(), os.Args[0], apiURL, "the-dispatch-key", "ghs_the_offers_token",
		fake+string(os.PathListSeparator)+os.Getenv("PATH"))
	require.NoError(t, err)

	env := append(githubapp.WithoutHostToken(append(src.WorkerEnv(), "GH_TOKEN=the-owners-token")), app...)

	return append(env, helperEnv+"=1", "HOME="+t.TempDir(), "GIT_TERMINAL_PROMPT=0")
}

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
	env := worker(t, api.URL)

	for _, kv := range env {
		assert.NotContains(t, kv, "the-owners-token", "the owner's token is not in the worker's environment")
		assert.False(t, strings.HasPrefix(kv, "GH_TOKEN="), "gh gets its token at each call")
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

// Scenario (issue #1262, a skew): the node reconnected to a control plane of an earlier release, which has no route
// for a fresh token. The worker's gh goes on with the token its offer carried, rather than failing.
func TestAControlPlaneWithoutTheRouteLeavesTheOffersToken(t *testing.T) {
	t.Parallel()

	old := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(old.Close)

	out := run(t, worker(t, old.URL), "", "sh", "-c", "gh api user")
	assert.Equal(t, "GH_TOKEN=ghs_the_offers_token args=api user\n", out)
}
