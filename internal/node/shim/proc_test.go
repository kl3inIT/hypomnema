package shim

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMain(tests *testing.M) {
	if len(os.Args) == 4 && os.Args[1] == "shim" && os.Args[2] == "--dir" {
		if err := Main(context.Background(), os.Args[3]); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
	if len(os.Args) == 3 && os.Args[1] == "shim-test-worker" {
		os.Exit(testWorker(os.Args[2]))
	}
	os.Exit(tests.Run())
}

func testWorker(mode string) int {
	switch mode {
	case "detached":
		time.Sleep(3 * time.Second)
		_ = os.WriteFile("survived", nil, 0o600)
		return 0
	case "orphan":
		deadline := time.Now().Add(12 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat("exit-orphan"); err == nil {
				return 0
			}
			time.Sleep(20 * time.Millisecond)
		}
		return 1
	case "parent", "orphan-parent", "launcher":
		exe, err := os.Executable()
		if err != nil {
			return 1
		}
		childMode := "detached"
		if mode == "orphan-parent" {
			childMode = "launcher"
		} else if mode == "launcher" {
			childMode = "orphan"
		}
		//nolint:noctx,gosec // this test launches itself to exercise the shim
		child := exec.Command(exe, "shim-test-worker", childMode)
		detach(child)
		if err := child.Start(); err != nil {
			return 1
		}
		if mode != "orphan-parent" {
			if err := os.WriteFile("child.pid", []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
				return 1
			}
		}
		if mode == "orphan-parent" {
			_ = child.Wait()
		}
		if mode == "launcher" {
			return 0
		}
		time.Sleep(15 * time.Second)
		return 0
	default:
		return 1
	}
}

func startTestShim(t *testing.T, mode string) (string, int, int) {
	t.Helper()
	dir, err := os.MkdirTemp("", "hyp-shim-") //nolint:usetesting // Unix socket paths are limited to about 100 bytes.
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	exe, err := os.Executable()
	require.NoError(t, err)
	require.NoError(t, WriteSpec(dir, Spec{Argv: []string{exe, "shim-test-worker", mode}, Dir: dir, Env: os.Environ()}))
	pid, err := Start(exe, dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = Stop(t.Context(), dir, "test cleanup") })
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = root.Close() })
	var child int
	require.Eventually(t, func() bool {
		raw, err := root.ReadFile("child.pid")
		if err != nil {
			return false
		}
		child, err = strconv.Atoi(string(raw))
		return err == nil
	}, 4*time.Second, 20*time.Millisecond)
	return dir, pid, child
}

func TestStopKillsDetachedDescendant(t *testing.T) {
	t.Parallel()
	dir, _, child := startTestShim(t, "parent")
	require.NoError(t, Stop(t.Context(), dir, "test timeout"))
	exit, found, err := ReadExit(dir)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, killSignal(), exit.Signal)
	if runtime.GOOS == "windows" {
		assert.Equal(t, 1, exit.Code)
	}
	assert.Eventually(t, func() bool { return !Alive(child) }, 3*time.Second, 20*time.Millisecond,
		"detached child %d survived", child)
	assert.NoFileExists(t, filepath.Join(dir, "survived"))
}

func TestAnAbsentShimIsNotReportedAsSignaled(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, Stop(t.Context(), dir, "shim missing"))
	exit, found, err := ReadExit(dir)
	require.NoError(t, err)
	require.True(t, found)
	assert.Empty(t, exit.Signal)
	assert.True(t, exit.Unconfirmed)
	assert.Equal(t, -1, exit.Code)
}

func TestStopDoesNotTrustAReusedPID(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("Unix PID reuse regression")
	}
	other := exec.CommandContext(t.Context(), "sleep", "60")
	detach(other)
	require.NoError(t, other.Start())
	done := make(chan struct{})
	go func() { _ = other.Wait(); close(done) }()
	t.Cleanup(func() { _ = other.Process.Kill(); <-done })
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, PIDFile), []byte(strconv.Itoa(other.Process.Pid)), 0o600))
	require.NoError(t, Stop(t.Context(), dir, "stale dispatch"))
	select {
	case <-done:
		t.Error("an unrelated process was stopped using the stale PID file")
	case <-time.After(150 * time.Millisecond):
	}
}
