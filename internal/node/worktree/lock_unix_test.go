//go:build unix

package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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
if [ "$3" != fetch ] || [ -z "$MIRROR_TEST_ACTION" ]; then
    exec "$MIRROR_TEST_REAL_GIT" "$@"
fi
if [ "$MIRROR_TEST_ACTION" = orphan ]; then
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

	var pid int
	require.Eventually(t, func() bool {
		data, err := root.ReadFile("ready")
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
			maker.gitTimeout = time.Second
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
			maker.gitTimeout = 0
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

	maker.lockTimeout = 200 * time.Millisecond
	path := filepath.Join(root.Name(), "wt", "replacement")
	err = maker.Ensure(t.Context(), src, "main", "replacement", path)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.ErrorContains(t, err, "locking mirror")
	assert.NoDirExists(t, path)

	require.NoError(t, root.WriteFile("release", nil, 0o600))
	maker.lockTimeout = 5 * time.Second
	require.NoError(t, maker.Ensure(t.Context(), src, "main", "replacement", path))
	assert.Equal(t, Branch("replacement"), run(t, path, "branch", "--show-current"))
}

func TestMirrorNodeFixture(t *testing.T) {
	if os.Getenv("MIRROR_TEST_NODE") != "1" {
		return
	}

	dir := os.Getenv("MIRROR_TEST_DIR")
	maker := Maker{Root: filepath.Join(dir, "node")}
	src, err := SourceOf("github.com", "github", "kl3inIT/second-bite", "")
	require.NoError(t, err)
	src.URL = filepath.Join(dir, "kl3inIT", "second-bite")
	src.Env = []string{"MIRROR_TEST_ACTION=orphan"}
	err = maker.Ensure(t.Context(), src, "main", "orphan", filepath.Join(dir, "wt", "orphan"))
	t.Errorf("orphan fixture returned before its node was killed: %v", err)
}
