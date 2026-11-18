//go:build linux || darwin

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
	"golang.org/x/sys/unix"
)

func mirrorProcessFixture(t *testing.T) (Maker, Source, *os.Root) {
	t.Helper()

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}

	dir := t.TempDir()
	origin(t, dir)
	maker := Maker{Root: filepath.Join(dir, "node")}
	src, err := SourceOf("github.com", "github", "kl3inIT/second-bite", "")
	require.NoError(t, err)
	src.URL = filepath.Join(dir, "kl3inIT", "second-bite")
	require.NoError(t, maker.Ensure(t.Context(), src, "main", "seed", filepath.Join(dir, "wt", "seed")))

	fake := []byte(`#!/bin/sh
command=$3
if [ "$1" = -c ]; then command=$5; fi
if [ "$MIRROR_TEST_ACTION" = clone-stall ] && ([ "$1" = clone ] || [ "$3" = clone ]); then
    for target do :; done
    mkdir -p "$target"
    echo "$$" > "$MIRROR_TEST_DIR/ready"
    sleep 10
    exit 1
fi
if [ "$command" != fetch ] || [ -z "$MIRROR_TEST_ACTION" ]; then
    exec "$MIRROR_TEST_REAL_GIT" "$@"
fi
if [ "$MIRROR_TEST_ACTION" = progress ]; then
    echo "$$" > "$MIRROR_TEST_DIR/ready"
    for count in 1 2 3 4 5 6 7 8 9 10; do
        echo "received $count" >&2
        sleep 0.12
    done
    exec "$MIRROR_TEST_REAL_GIT" "$@"
fi
if [ "$MIRROR_TEST_ACTION" = stale ]; then
    mirror=$2
    if [ "$1" = -c ]; then mirror=$4; fi
    touch "$mirror/refs/remotes/origin/main.lock" "$mirror/index.lock" "$mirror/shallow.lock" "$mirror/config.lock"
    trap 'echo term > "$MIRROR_TEST_DIR/term"' TERM
    echo "$$" > "$MIRROR_TEST_DIR/ready"
    while :; do sleep 0.05; done
fi
if [ "$MIRROR_TEST_ACTION" = orphan-detached ]; then
    setsid sh -c 'echo "$$" > "$MIRROR_TEST_DIR/detached"; sleep 10' </dev/null >/dev/null 2>&1 &
    trap 'exit 143' TERM
fi
if [ "$MIRROR_TEST_ACTION" = orphan ] || [ "$MIRROR_TEST_ACTION" = orphan-detached ]; then
    printf '%s\n' "$$" > "$MIRROR_TEST_DIR/ready"
    attempts=0
    until [ -e "$MIRROR_TEST_DIR/release" ] || [ "$attempts" -ge 500 ]; do
        sleep 0.02
        attempts=$((attempts + 1))
    done
    exit 0
fi
(sleep 10; echo 'child still holding stderr' >&2) &
printf '%s\n' "$$" > "$MIRROR_TEST_DIR/ready"
if [ "$MIRROR_TEST_ACTION" = leader-exit ]; then exit 1; fi
wait
`)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "git"), fake, 0o700)) //nolint:gosec // executable fixture
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MIRROR_TEST_REAL_GIT", realGit)
	t.Setenv("MIRROR_TEST_DIR", dir)
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, root.Close()) })

	return maker, src, root
}

func waitForMirrorGit(t *testing.T, root *os.Root) int {
	t.Helper()
	return waitForPID(t, root, "ready")
}

func waitForPID(t *testing.T, root *os.Root, file string) int {
	t.Helper()

	var pid int
	require.Eventually(t, func() bool {
		data, err := root.ReadFile(file)
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(data)))
		return err == nil && pid > 1
	}, 5*time.Second, 10*time.Millisecond)
	return pid
}

func TestHungFetchStopsItsChildrenAndReleasesMirror(t *testing.T) {
	for _, mode := range []string{"timeout", "cancel", "leader-exit"} {
		t.Run(mode, func(t *testing.T) {
			maker, src, root := mirrorProcessFixture(t)
			src.Env = []string{"MIRROR_TEST_ACTION=" + mode}
			maker.GitStall = time.Second
			maker.GitGrace = 150 * time.Millisecond
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			result := make(chan error, 1)
			go func() {
				result <- maker.Ensure(ctx, src, "main", mode, filepath.Join(root.Name(), "wt", mode))
			}()
			pid := waitForMirrorGit(t, root)
			if mode == "cancel" {
				cancel()
			}
			guard, stop := context.WithTimeout(t.Context(), 3*time.Second)
			defer stop()
			select {
			case err := <-result:
				switch mode {
				case "timeout":
					require.ErrorIs(t, err, context.DeadlineExceeded)
					require.NoError(t, ctx.Err())
				case "cancel":
					require.ErrorIs(t, err, context.Canceled)
				default:
					require.Error(t, err)
				}
			case <-guard.Done():
				_ = unix.Kill(-pid, unix.SIGKILL)
				t.Fatal("Git or its stderr pipe outlived the bound")
			}

			healthy := src
			healthy.Env = nil
			maker.GitStall = 0
			require.NoError(t, maker.Ensure(guard, healthy, "main", "next", filepath.Join(root.Name(), "wt", "next")))
		})
	}
}

func TestMirrorLockOutlivesTheNodeThatStartedGit(t *testing.T) {
	maker, src, root := mirrorProcessFixture(t)
	exe, err := os.Executable()
	require.NoError(t, err)
	child := exec.CommandContext(t.Context(), exe, "-test.run=^TestMirrorNodeFixture$") //nolint:gosec // this test binary
	child.Env = append(os.Environ(), "MIRROR_TEST_NODE=1")
	require.NoError(t, child.Start())
	t.Cleanup(func() {
		if child.ProcessState == nil {
			_ = child.Process.Kill()
			_ = child.Wait()
		}
	})
	pid := waitForMirrorGit(t, root)
	t.Cleanup(func() { _ = unix.Kill(-pid, unix.SIGKILL) })
	require.NoError(t, child.Process.Kill())
	require.Error(t, child.Wait())

	path := filepath.Join(root.Name(), "wt", "replacement")
	blocked, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	err = maker.Ensure(blocked, src, "main", "replacement", path)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.ErrorContains(t, err, "locking mirror")
	assert.NoDirExists(t, path)

	require.NoError(t, root.WriteFile("release", nil, 0o600))
	replacement, stop := context.WithTimeout(t.Context(), 5*time.Second)
	defer stop()
	require.NoError(t, maker.Ensure(replacement, src, "main", "replacement", path))
	assert.Equal(t, Branch("replacement"), run(t, path, "branch", "--show-current"))
}

func TestInterruptedFetchRecoversGitLocks(t *testing.T) {
	maker, src, root := mirrorProcessFixture(t)
	maker.GitStall, maker.GitGrace = 400*time.Millisecond, 200*time.Millisecond
	interrupted := src
	interrupted.Env = []string{"MIRROR_TEST_ACTION=stale"}
	err := maker.Ensure(t.Context(), interrupted, "main", "stale", filepath.Join(root.Name(), "wt", "stale"))
	require.ErrorIs(t, err, context.DeadlineExceeded)
	mirror := maker.mirrorPath(src.Repo)
	for _, name := range []string{"refs/remotes/origin/main.lock", "index.lock", "shallow.lock", "config.lock"} {
		require.FileExists(t, filepath.Join(mirror, name))
	}
	run(t, src.URL, "commit", "--allow-empty", "-qm", "advance the ref")
	want := run(t, src.URL, "rev-parse", "HEAD")
	planned := filepath.Join(root.Name(), "wt", "next")
	require.NoError(t, maker.Ensure(t.Context(), src, "main", "next", planned))
	assert.Equal(t, want, run(t, planned, "rev-parse", "HEAD"))
	assert.FileExists(t, filepath.Join(root.Name(), "term"), "SIGTERM arrived before the forced stop")
}

func TestProgressingFetchAndItsWaiterOutliveStallInterval(t *testing.T) {
	maker, src, root := mirrorProcessFixture(t)
	maker.GitStall, maker.GitGrace = 500*time.Millisecond, 150*time.Millisecond
	progressing := src
	progressing.Env = []string{"MIRROR_TEST_ACTION=progress"}
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- maker.Ensure(ctx, progressing, "main", "long", filepath.Join(root.Name(), "wt", "long"))
	}()
	waitForMirrorGit(t, root)
	path := filepath.Join(root.Name(), "wt", "waiter")
	started := time.Now()
	waitErr := maker.Ensure(ctx, src, "main", "waiter", path)
	holderErr := <-result
	require.NoError(t, holderErr)
	require.NoError(t, waitErr)
	assert.Greater(t, time.Since(started), maker.GitStall)
	assert.Equal(t, Branch("waiter"), run(t, path, "branch", "--show-current"))
}

func TestInterruptedCloneIsNotPublished(t *testing.T) {
	maker, src, root := mirrorProcessFixture(t)
	other, err := SourceOf("github.com", "github", "kl3inIT/clone-recovery", "")
	require.NoError(t, err)
	other.URL = src.URL
	other.Env = []string{"MIRROR_TEST_ACTION=clone-stall"}
	maker.GitStall, maker.GitGrace = 400*time.Millisecond, 150*time.Millisecond
	path := filepath.Join(root.Name(), "wt", "clone")
	err = maker.Ensure(t.Context(), other, "main", "clone", path)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NoDirExists(t, maker.mirrorPath(other.Repo))
	other.Env = nil
	require.NoError(t, maker.Ensure(t.Context(), other, "main", "clone", path))
	assert.Equal(t, run(t, src.URL, "rev-parse", "main"), run(t, maker.mirrorPath(other.Repo), "rev-parse", "main"))
}

func TestInterruptedCheckoutIsRecreatedBeforeSetup(t *testing.T) {
	maker, src, root := mirrorProcessFixture(t)
	for name, data := range map[string]string{
		".gitattributes": "README.md filter=hold\n",
		SetupFile:        "scripts:\n  setup: echo ready > setup.log\n",
	} {
		require.NoError(t, os.WriteFile(filepath.Join(src.URL, name), []byte(data), 0o600))
	}
	run(t, src.URL, "add", ".")
	run(t, src.URL, "commit", "-qm", "declare checkout filter and setup")
	mirror := maker.mirrorPath(src.Repo)
	require.NoError(t, root.WriteFile("block-checkout", nil, 0o600))
	run(t, mirror, "config", "filter.hold.smudge", `if test -f "$MIRROR_TEST_DIR/block-checkout"; then
        echo "$$" > "$MIRROR_TEST_DIR/ready"; sleep 10; fi; cat`)
	path := filepath.Join(root.Name(), "wt", "checkout")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	maker.GitGrace = 150 * time.Millisecond
	result := make(chan error, 1)
	go func() { result <- maker.Ensure(ctx, src, "main", "checkout", path) }()
	waitForMirrorGit(t, root)
	require.FileExists(t, filepath.Join(path, ".git"))
	cancel()
	require.ErrorIs(t, <-result, context.Canceled)
	require.NoFileExists(t, filepath.Join(path, "README.md"))
	require.NoError(t, root.Remove("block-checkout"))
	require.NoError(t, maker.Ensure(t.Context(), src, "main", "checkout", path))
	assert.FileExists(t, filepath.Join(path, "README.md"))
	assert.FileExists(t, filepath.Join(path, "setup.log"))
}

func TestWorktreeCommandsDisableHooks(t *testing.T) {
	maker, src, root := mirrorProcessFixture(t)
	mirror := maker.mirrorPath(src.Repo)
	hooks := filepath.Join(root.Name(), "hooks")
	require.NoError(t, os.Mkdir(hooks, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(hooks, "post-checkout"), //nolint:gosec // executable test hook
		[]byte("#!/bin/sh\necho executed > \"$MIRROR_TEST_DIR/hook-ran\"\n"), 0o700))
	run(t, mirror, "config", "core.hooksPath", hooks)
	require.NoError(t, maker.Ensure(t.Context(), src, "main", "hooks", filepath.Join(root.Name(), "wt", "hooks")))
	assert.NoFileExists(t, filepath.Join(root.Name(), "hook-ran"))
}

func TestDetachedGitDescendantDoesNotKeepLockAfterNodeDeath(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the fixture uses Linux setsid")
	}
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid is not installed")
	}
	maker, src, root := mirrorProcessFixture(t)
	exe, err := os.Executable()
	require.NoError(t, err)
	child := exec.CommandContext(t.Context(), exe, "-test.run=^TestMirrorNodeFixture$") //nolint:gosec // this test binary
	child.Env = append(os.Environ(), "MIRROR_TEST_NODE=orphan-detached")
	require.NoError(t, child.Start())
	t.Cleanup(func() {
		if child.ProcessState == nil {
			_ = child.Process.Kill()
			_ = child.Wait()
		}
	})
	waitForMirrorGit(t, root)
	detached := waitForPID(t, root, "detached")
	t.Cleanup(func() { _ = unix.Kill(-detached, unix.SIGKILL) })
	require.NoError(t, child.Process.Kill())
	require.Error(t, child.Wait())
	require.NoError(t, root.WriteFile("release", nil, 0o600))
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	path := filepath.Join(root.Name(), "wt", "replacement")
	require.NoError(t, maker.Ensure(ctx, src, "main", "replacement", path))
	require.NoError(t, unix.Kill(detached, 0), "the descendant is alive but not holding the mirror lock")
	assert.Equal(t, Branch("replacement"), run(t, path, "branch", "--show-current"))
}

func TestMirrorNodeFixture(t *testing.T) {
	mode := os.Getenv("MIRROR_TEST_NODE")
	if mode == "" {
		return
	}
	if mode == "1" {
		mode = "orphan"
	}

	dir := os.Getenv("MIRROR_TEST_DIR")
	maker := Maker{Root: filepath.Join(dir, "node")}
	src, err := SourceOf("github.com", "github", "kl3inIT/second-bite", "")
	require.NoError(t, err)
	src.URL = filepath.Join(dir, "kl3inIT", "second-bite")
	src.Env = []string{"MIRROR_TEST_ACTION=" + mode}
	err = maker.Ensure(t.Context(), src, "main", "orphan", filepath.Join(dir, "wt", "orphan"))
	t.Errorf("orphan fixture returned before its node was killed: %v", err)
}
