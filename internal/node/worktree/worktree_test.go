package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/node/diskguard"
)

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), "git", args...) //nolint:gosec // the test's own git commands
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")

	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))

	return strings.TrimSpace(string(out))
}

// origin makes a repository owner/name under dir with one commit on main.
func origin(t *testing.T, dir string) {
	t.Helper()

	src := filepath.Join(dir, "kl3inIT", "second-bite")
	require.NoError(t, os.MkdirAll(src, 0o700))
	run(t, src, "init", "-q", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(src, "README.md"), []byte("second-bite\n"), 0o600))
	run(t, src, "add", "README.md")
	run(t, src, "commit", "-q", "-m", "first")
}

// Scenario: the first Dispatch of a Task makes a worktree on its own branch
// from the base; a continued one reuses it; a retry after the worktree was
// removed gets the branch back with what was committed on it.
func TestWorktreeIsMadeReusedAndRemade(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}

	dir := realTempDir(t)
	origin(t, filepath.Join(dir, "origin"))

	m := Maker{Root: filepath.Join(dir, "node"), URL: filepath.ToSlash(filepath.Join(dir, "origin")) + "/%s"}
	path := filepath.Join(dir, "wt", "run_1", "tsk_1")

	repo, err := SourceOf("github.com", "github", "kl3inIT/second-bite", "")
	require.NoError(t, err)

	require.NoError(t, m.Ensure(t.Context(), repo, "main", "tsk_1", path))
	assert.Equal(t, "hyp/tsk_1", run(t, path, "rev-parse", "--abbrev-ref", "HEAD"))
	assert.FileExists(t, filepath.Join(path, "README.md"))

	require.NoError(t, os.WriteFile(filepath.Join(path, "login.go"), []byte("package login\n"), 0o600))
	run(t, path, "add", "login.go")
	run(t, path, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "login")

	require.NoError(t, m.Ensure(t.Context(), repo, "main", "tsk_1", path), "a continued Conversation")
	assert.FileExists(t, filepath.Join(path, "login.go"))

	if !diskguard.ScopeAvailable() {
		t.Skip("Landlock is not available for the native command")
	}
	require.NoError(t, m.Clean(t.Context(), path, true, func() bool { return true }))
	assert.NoDirExists(t, path)

	require.NoError(t, m.Ensure(t.Context(), repo, "main", "tsk_1", path), "a retry")
	assert.FileExists(t, filepath.Join(path, "login.go"), "the branch keeps the earlier commit")

	assert.DirExists(t, filepath.Join(dir, "node", "repos", "kl3inIT", "second-bite.git"),
		"a GitHub mirror stays where it was before hosts existed")

	require.ErrorContains(t, m.Ensure(t.Context(), Source{}, "main", "tsk_2", filepath.Join(dir, "x")),
		"names no repository")
}

// An offer names its repository's host and kind. Each kind is cloned from its
// own address; what cannot be a repository, or is of a kind this node does not
// know, never reaches git.
func TestAnOffersRepositoryIsCheckedBeforeGitSeesIt(t *testing.T) {
	t.Parallel()

	hub, err := SourceOf("github.com", "github", "kl3inIT/hypomnema", "")
	require.NoError(t, err)
	assert.Equal(t, "https://github.com/kl3inIT/hypomnema.git", hub.URL)

	own, err := SourceOf("gitlab.company.example:8443", "gitlab", "team/platform/api", "")
	require.NoError(t, err)
	assert.Equal(t, "https://gitlab.company.example:8443/team/platform/api.git", own.URL)

	m := Maker{Root: "/srv/node"}
	assert.Equal(t, filepath.Join("/srv/node", "repos", "kl3inIT", "hypomnema.git"), m.mirrorPath(hub.Repo))
	assert.Equal(t,
		filepath.Join("/srv/node", "repos", "_hosts", "gitlab.company.example_8443", "team", "platform", "api.git"),
		m.mirrorPath(own.Repo))

	assert.Empty(t, own.Env, "without a token the node user's own logins are used")

	with, err := SourceOf("gitlab.company.example", "gitlab", "team/app", "glpat-x")
	require.NoError(t, err)
	assert.Contains(t, with.Env, "GITLAB_TOKEN=glpat-x")
	assert.Contains(t, with.Env, "GIT_CONFIG_KEY_0=http.https://gitlab.company.example/.extraheader")
	assert.NotContains(t, with.URL, "glpat-x", "the token is never part of the address")

	for name, bad := range map[string][3]string{
		"a kind this node does not know": {"bitbucket.org", "bitbucket", "team/app"},
		"a path that is an option":       {"gitlab.com", "gitlab", "team/--upload-pack=sh"},
		"a path that climbs out":         {"gitlab.com", "gitlab", "../../etc"},
		"a host that is not one":         {"gitlab.com/x?y", "gitlab", "team/app"},
		"no path":                        {"gitlab.com", "gitlab", ""},
		"no host":                        {"", "github", "kl3inIT/hypomnema"},
		"no kind":                        {"github.com", "", "kl3inIT/hypomnema"},
	} {
		_, err := SourceOf(bad[0], bad[1], bad[2], "")
		assert.Error(t, err, name)
	}
}

// Scenario: a repository registered with an SSH address (issue #625). The
// mirror is cloned from that address, with git's ssh being the command the
// environment names (the node's helper; here a script that writes down what
// git asked of it), at the address's port. The host's token stays for its
// CLI. An address that is not one never reaches git.
func TestARepositoryWithAnSSHAddressIsClonedOverSSH(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}

	src, err := SourceOf("gitlab.company.example", "gitlab", "mobility/platform", "glpat-x")
	require.NoError(t, err)

	_, err = src.OverSSH("ssh://git@-oProxyCommand=x/mobility/platform.git", nil)
	require.Error(t, err)

	dir := t.TempDir()
	asked := filepath.ToSlash(filepath.Join(dir, "asked"))

	ssh, err := src.OverSSH("ssh://git@gitlab.company.example:222/mobility/platform.git", []string{
		"GIT_SSH_COMMAND=printf '%s\\n' \"$@\" > '" + asked + "'; false", "GIT_SSH_VARIANT=ssh",
	})
	require.NoError(t, err)
	assert.Equal(t, "ssh://git@gitlab.company.example:222/mobility/platform.git", ssh.URL)
	assert.Contains(t, ssh.Env, "GITLAB_TOKEN=glpat-x")
	assert.Equal(t, "https://gitlab.company.example/mobility/platform.git", src.URL, "the source over HTTPS is unchanged")

	m := Maker{Root: filepath.Join(dir, "node")}
	require.Error(t, m.Ensure(t.Context(), ssh, "main", "tsk_1", filepath.Join(dir, "wt", "tsk_1")))

	raw, err := os.ReadFile(filepath.FromSlash(asked))
	require.NoError(t, err, "git ran the command it was given as ssh")
	// Git may ask ssh to pass on its protocol version first (-o SendEnv=GIT_PROTOCOL).
	assert.True(t, strings.HasSuffix(strings.ReplaceAll(string(raw), "\r\n", "\n"),
		"-p\n222\ngit@gitlab.company.example\ngit-upload-pack '/mobility/platform.git'\n"), string(raw))
}
