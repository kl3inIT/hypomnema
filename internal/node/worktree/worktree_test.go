package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"

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

func TestConcurrentEnsuresOnOneMirror(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}

	for _, count := range []int{2, 5} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			origin(t, dir)
			src, err := SourceOf("github.com", "github", "kl3inIT/second-bite", "")
			require.NoError(t, err)
			src.URL = filepath.Join(dir, filepath.FromSlash(src.Repo.Path))
			maker := Maker{Root: filepath.Join(dir, "node")}
			if count > 2 {
				require.NoError(t, maker.Ensure(t.Context(), src, "main", "seed", filepath.Join(dir, "wt", "seed")))
			}

			run(t, src.URL, "commit", "--allow-empty", "-qm", "base "+t.Name())
			want := run(t, src.URL, "rev-parse", "HEAD")
			start := make(chan struct{})
			var group errgroup.Group
			for index := range count {
				task := "task-" + strconv.Itoa(index)
				group.Go(func() error {
					<-start
					return maker.Ensure(t.Context(), src, "main", task, filepath.Join(dir, "wt", task))
				})
			}
			close(start)

			require.NoError(t, group.Wait())
			for index := range count {
				task := "task-" + strconv.Itoa(index)
				path := filepath.Join(dir, "wt", task)
				assert.Equal(t, Branch(task), run(t, path, "branch", "--show-current"))
				assert.Equal(t, want, run(t, path, "rev-parse", "HEAD"))
			}
		})
	}
}

func TestMirrorLockDoesNotBlockAnotherRepository(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}

	dir := t.TempDir()
	origin(t, dir)
	src, err := SourceOf("github.com", "github", "kl3inIT/second-bite", "")
	require.NoError(t, err)
	other, err := SourceOf("github.com", "github", "kl3inIT/other", "")
	require.NoError(t, err)
	src.URL = filepath.Join(dir, filepath.FromSlash(src.Repo.Path))
	other.URL = src.URL
	maker := Maker{Root: filepath.Join(dir, "node")}

	mirror := maker.mirrorPath(src.Repo)
	require.NoError(t, os.MkdirAll(filepath.Dir(mirror), 0o700))
	lock, err := maker.lockMirror(t.Context(), mirror)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, lock.Close()) })

	blocked, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	err = maker.Ensure(blocked, src, "main", "blocked", filepath.Join(dir, "wt", "blocked"))
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.ErrorContains(t, err, "locking mirror")
	assert.NoDirExists(t, mirror)

	ctx, cancelOther := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancelOther()
	path := filepath.Join(dir, "wt", "other")
	require.NoError(t, maker.Ensure(ctx, other, "main", "other", path))
	assert.Equal(t, Branch("other"), run(t, path, "branch", "--show-current"))
}

func TestMirrorLockCannotCollideWithGitLabSubgroup(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}

	for _, order := range []struct{ name, first, second string }{
		{"project-first", "team/project", "team/project.git.lock/other"},
		{"subgroup-first", "team/project.git.lock/other", "team/project"},
	} {
		t.Run(order.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			origin(t, dir)
			maker := Maker{Root: filepath.Join(dir, "node")}
			for index, repo := range []string{order.first, order.second} {
				src, err := SourceOf("gitlab.com", "gitlab", repo, "")
				require.NoError(t, err)
				src.URL = filepath.Join(dir, "kl3inIT", "second-bite")
				task := strconv.Itoa(index)
				path := filepath.Join(dir, "wt", task)
				require.NoError(t, maker.Ensure(t.Context(), src, "main", task, path))
				assert.Equal(t, Branch(task), run(t, path, "branch", "--show-current"))
			}
		})
	}
}

func TestMirrorFetchDoesNotStartAutomaticMaintenance(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}

	dir := t.TempDir()
	origin(t, dir)
	maker := Maker{Root: filepath.Join(dir, "node")}
	src, err := SourceOf("github.com", "github", "kl3inIT/second-bite", "")
	require.NoError(t, err)
	src.URL = filepath.Join(dir, "kl3inIT", "second-bite")
	require.NoError(t, maker.Ensure(t.Context(), src, "main", "seed", filepath.Join(dir, "wt", "seed")))

	src.Env = []string{
		"GIT_TRACE2_EVENT=" + filepath.Join(dir, "trace"),
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=maintenance.auto", "GIT_CONFIG_VALUE_0=true",
	}
	require.NoError(t, maker.Ensure(t.Context(), src, "main", "next", filepath.Join(dir, "wt", "next")))
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, root.Close()) })
	trace, err := root.ReadFile("trace")
	require.NoError(t, err)
	assert.Contains(t, string(trace), `"name":"fetch"`)
	assert.NotContains(t, string(trace), `"name":"maintenance"`)
	assert.NotContains(t, string(trace), `"name":"gc"`)
}

func TestMirrorCleanWaitsForTheSameLock(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	if !diskguard.ScopeAvailable() {
		t.Skip("filesystem confinement is unavailable")
	}

	dir := realTempDir(t)
	origin(t, dir)
	maker := Maker{Root: filepath.Join(dir, "node")}
	src, err := SourceOf("github.com", "github", "kl3inIT/second-bite", "")
	require.NoError(t, err)
	src.URL = filepath.Join(dir, "kl3inIT", "second-bite")
	path := filepath.Join(maker.Root, "worktrees", "run_1", "task")
	require.NoError(t, maker.Ensure(t.Context(), src, "main", "task", path))
	stopped := func() bool { return true }

	for _, held := range []string{"worktree", "mirror"} {
		var lock *mirrorLock
		if held == "worktree" {
			state, err := maker.lockPath("worktree", path)
			require.NoError(t, err)
			lock, err = openMirrorLock(t.Context(), state)
			require.NoError(t, err)
		} else {
			lock, err = maker.lockMirror(t.Context(), maker.mirrorPath(src.Repo))
			require.NoError(t, err)
		}

		blocked, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
		err = maker.Clean(blocked, path, true, stopped)
		cancel()
		require.ErrorIs(t, err, context.DeadlineExceeded, "the %s lock is held", held)
		assert.FileExists(t, filepath.Join(path, ".git"))
		require.NoError(t, lock.Close())
	}

	require.NoError(t, maker.Clean(t.Context(), path, true, stopped))
	assert.NoDirExists(t, path)
}

func TestFetchRetriesARefLockErrorOnlyOnce(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the test uses a shell executable in PATH")
	}

	dir := t.TempDir()
	fakeGit := []byte(`#!/bin/sh
if { [ "$GIT_TEST_MODE" = transient ] || [ "$GIT_TEST_MODE" = buried ]; } && [ -s "$GIT_TEST_ATTEMPTS" ]; then
    echo fetch >> "$GIT_TEST_ATTEMPTS"
    exit 0
fi
echo fetch >> "$GIT_TEST_ATTEMPTS"
if [ "$GIT_TEST_MODE" = unrelated ]; then
    echo 'fatal: repository not found' >&2
else
    echo "error: cannot lock ref 'refs/remotes/origin/main': is at new but expected old" >&2
fi
if [ "$GIT_TEST_MODE" = buried ] || [ "$GIT_TEST_MODE" = buried-persistent ]; then
    # Git goes on to report every other branch it fetched, past what the error keeps.
    count=0
    while [ $count -lt 160 ]; do
        echo " * [new branch]      feature/branch-number-$count -> origin/feature/branch-number-$count" >&2
        count=$((count + 1))
    done
fi
exit 1
`)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "git"), fakeGit, 0o700)) //nolint:gosec // executable fixture
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	for _, trial := range []struct {
		mode     string
		attempts int
		failure  string
	}{
		{"transient", 2, ""},
		{"persistent", 2, "cannot lock ref"},
		{"unrelated", 1, "repository not found"},
		{"buried", 2, ""},
		{"buried-persistent", 2, "cannot lock ref"},
	} {
		t.Run(trial.mode, func(t *testing.T) {
			trialDir := t.TempDir()
			root, err := os.OpenRoot(trialDir)
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, root.Close()) })
			err = (mirrorGit{}).fetch(t.Context(), dir, "origin", []string{
				"GIT_TEST_MODE=" + trial.mode, "GIT_TEST_ATTEMPTS=" + filepath.Join(trialDir, "attempts"),
			})
			if trial.failure == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, trial.failure)
			}
			log, err := root.ReadFile("attempts")
			require.NoError(t, err)
			assert.Equal(t, strings.Repeat("fetch\n", trial.attempts), string(log))
		})
	}
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

func TestOutputTailNotesARefLockSplitAcrossWritesAndCutAway(t *testing.T) {
	t.Parallel()
	var output outputTail
	for _, part := range []string{"error: cannot lo", "ck ref 'refs/remotes/origin/main'\n"} {
		_, err := output.Write([]byte(part))
		require.NoError(t, err)
	}
	_, err := output.Write([]byte(strings.Repeat("x", 2*setupOutputKept)))
	require.NoError(t, err)
	assert.True(t, output.refLock)
	assert.Len(t, output.String(), setupOutputKept)
	assert.NotContains(t, output.String(), "cannot lock ref")

	var other outputTail
	_, err = other.Write([]byte("fatal: repository not found\n"))
	require.NoError(t, err)
	assert.False(t, other.refLock)
}
