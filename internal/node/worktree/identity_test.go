package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/githost"
)

// Scenario (issue #991): the node user's ~/.gitconfig names someone else, as
// on the node where a worker committed as its user. An offer whose token's
// account is octo makes the repository's setup script and the worker commit
// as octo, author and committer; an offer that names no account sets nothing,
// and git keeps what the machine says.
//
// Not parallel: the test gives the node a HOME of its own.
func TestCommitsNameTheTokensAccountOverTheNodeUsers(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}

	m, repo, path, _ := declaring(t, "scripts:\n  setup: |\n    git commit -q --allow-empty -m setup\n")

	home := realTempDir(t)
	require.NoError(t, os.WriteFile(filepath.Join(home, ".gitconfig"),
		[]byte("[user]\n\tname = tinzisreal\n\temail = 163243197+tinzisreal@users.noreply.github.com\n"), 0o600))
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	for _, name := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(name, "")
		require.NoError(t, os.Unsetenv(name))
	}

	octo := githost.Identity{Name: "octo", Email: "1+octo@users.noreply.github.com"}

	// What the worker's git does: the node's environment, then what its offer adds.
	commit := func(src Source, dir, message string) {
		//nolint:gosec // the test's own git command
		cmd := exec.CommandContext(t.Context(), "git", "commit", "-q", "--allow-empty", "-m", message)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), src.WorkerEnv()...)

		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	who := func(dir, rev string) string {
		return run(t, dir, "log", "-1", "--format=%an <%ae> / %cn <%ce>", rev)
	}

	repo.Identity = octo
	require.NoError(t, m.Ensure(t.Context(), repo, "main", "tsk_1", path))
	commit(repo, path, "worker")

	const asOcto = "octo <1+octo@users.noreply.github.com> / octo <1+octo@users.noreply.github.com>"

	assert.Equal(t, asOcto, who(path, "HEAD~1"), "the setup script commits as the token's account")
	assert.Equal(t, asOcto, who(path, "HEAD"), "so does the worker")

	repo.Identity = githost.Identity{}
	assert.Empty(t, repo.WorkerEnv(), "no token and no account: nothing is set")

	commit(repo, path, "without an account")
	assert.True(t, strings.HasPrefix(who(path, "HEAD"), "tinzisreal <"),
		"git keeps what the machine says when no account is known")
}
