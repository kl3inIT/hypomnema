package memlimit

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeSystemd puts systemd-run and systemctl on PATH, which the probe looks for; the commands themselves are run
// through the probe's run.
func fakeSystemd(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	for _, name := range []string{"systemd-run", "systemctl"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), 0o700)) //nolint:gosec // fixture
	}
	t.Setenv("PATH", dir)
}

// cgroupTree is a cgroup v2 root whose user manager for uid 1003 has these controllers.
func cgroupTree(t *testing.T, controllers string) string {
	t.Helper()

	root := t.TempDir()
	manager := filepath.Join(root, "user.slice", "user-1003.slice", "user@1003.service")
	require.NoError(t, os.MkdirAll(manager, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "cgroup.controllers"), []byte("cpu io memory pids\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(manager, "cgroup.controllers"), []byte(controllers+"\n"), 0o600))

	return root
}

func noRun(t *testing.T) func(context.Context, ...string) ([]byte, error) {
	t.Helper()

	return func(context.Context, ...string) ([]byte, error) {
		t.Fatal("nothing is started when the probe already knows")

		return nil, nil
	}
}

// Scenario: a node whose machine cannot limit its workers' memory runs them as before, and says why: no systemd,
// no cgroup v2, no user manager, a user manager that does not delegate memory, or a scope that cannot start
// (issue #1313 row 6).
func TestProbeSaysWhyMemoryCannotBeLimited(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	l := probe(t.Context(), cgroupTree(t, "cpu memory pids"), 1003, noRun(t))
	assert.False(t, l.On())
	assert.Contains(t, l.Reason, "no systemd")

	fakeSystemd(t)

	l = probe(t.Context(), t.TempDir(), 1003, noRun(t))
	assert.False(t, l.On())
	assert.Contains(t, l.Reason, "cgroup v2 is not mounted")

	l = probe(t.Context(), cgroupTree(t, "cpu memory pids"), 1002, noRun(t))
	assert.False(t, l.On())
	assert.Contains(t, l.Reason, "no systemd user manager runs for this user (user@1002.service)")

	l = probe(t.Context(), cgroupTree(t, "cpu pids"), 1003, noRun(t))
	assert.False(t, l.On())
	assert.Equal(t, "the systemd user manager does not delegate the memory controller (user@1003.service has: cpu pids)",
		l.Reason)

	l = probe(t.Context(), cgroupTree(t, "cpu memory pids"), 1003, func(context.Context, ...string) ([]byte, error) {
		return []byte("Failed to connect to bus: No medium found\n"), errors.New("exit status 1")
	})
	assert.False(t, l.On())
	assert.Equal(t, "no scope could be started in hypomnema-workers.slice: Failed to connect to bus: No medium found",
		l.Reason)
}

// Scenario: where the user manager delegates memory and a scope starts, the workers run in the slice; a manager
// before systemd 253, which refuses OOMPolicy= for a scope, runs them without it.
func TestProbeFindsTheLimitAndTheOOMPolicy(t *testing.T) {
	fakeSystemd(t)

	var tried [][]string

	record := func(_ context.Context, argv ...string) ([]byte, error) {
		tried = append(tried, argv)

		return nil, nil
	}

	l := probe(t.Context(), cgroupTree(t, "cpu memory pids"), 1003, record)
	require.True(t, l.On(), l.Reason)
	assert.True(t, l.OOMPolicy)
	require.Len(t, tried, 1)
	assert.Contains(t, tried[0], "--slice=hypomnema-workers.slice")
	assert.Equal(t, "true", tried[0][len(tried[0])-1])

	l = probe(t.Context(), cgroupTree(t, "memory pids"), 1003, func(_ context.Context, argv ...string) ([]byte, error) {
		if strings.Contains(strings.Join(argv, " "), "--property=OOMPolicy") {
			return []byte("Unknown assignment: OOMPolicy=continue\n"), errors.New("exit status 1")
		}

		return nil, nil
	})
	require.True(t, l.On(), l.Reason)
	assert.False(t, l.OOMPolicy)
}

// Scenario: the shim finds its own cgroup in /proc/self/cgroup's cgroup v2 line.
func TestUnifiedPathIsTheCgroupV2Line(t *testing.T) {
	t.Parallel()

	path, err := unifiedPath([]byte(
		"0::/user.slice/user-1003.slice/user@1003.service/hypomnema.slice/hypomnema-workers.slice/" +
			"hypomnema-worker-dsp_1.scope\n"))
	require.NoError(t, err)
	assert.Equal(t, "/user.slice/user-1003.slice/user@1003.service/hypomnema.slice/hypomnema-workers.slice/"+
		"hypomnema-worker-dsp_1.scope", path)

	_, err = unifiedPath([]byte("12:memory:/user.slice\n"))
	require.Error(t, err, "cgroup v1 only")
}

// Scenario: on a machine whose user manager delegates memory, a real scope in a slice of 32 MiB keeps its shell
// after the kernel kills a process that allocates past it, and the shell reads the kill in its own memory.events.
// It runs only when HYPOMNEMA_TEST_SYSTEMD_SCOPE is set: it starts a transient unit of the user's systemd.
func TestARealScopeCountsAnOOMKill(t *testing.T) {
	if os.Getenv("HYPOMNEMA_TEST_SYSTEMD_SCOPE") == "" {
		t.Skip("set HYPOMNEMA_TEST_SYSTEMD_SCOPE=1 to start a real systemd scope")
	}

	l := Probe(t.Context())
	require.True(t, l.On(), l.Reason)

	exe, err := os.Executable()
	require.NoError(t, err)

	argv := append(l.Scope("test-"+filepath.Base(t.TempDir()), 32<<20), exe, "-test.run=^TestOOMKillHelper$")
	cmd := exec.CommandContext(t.Context(), argv[0], argv[1:]...) //nolint:gosec // the test binary
	cmd.Env = append(os.Environ(), "HYPOMNEMA_OOM_HELPER=1")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	assert.Contains(t, string(out), "oom_kills=1")
}

// TestOOMKillHelper runs inside the scope: a child allocates past the scope's 32 MiB, and this process, which
// survives it, reads its scope's count.
func TestOOMKillHelper(t *testing.T) {
	if os.Getenv("HYPOMNEMA_OOM_HELPER") == "" {
		t.Skip("run by TestARealScopeCountsAnOOMKill")
	}

	if os.Getenv("HYPOMNEMA_OOM_HELPER") == "child" {
		b := make([]byte, 256<<20)
		for i := range b {
			b[i] = 1
		}

		return
	}

	exe, err := os.Executable()
	require.NoError(t, err)

	child := exec.CommandContext(t.Context(), exe, "-test.run=^TestOOMKillHelper$") //nolint:gosec // the test binary
	child.Env = append(os.Environ(), "HYPOMNEMA_OOM_HELPER=child")
	require.Error(t, child.Run(), "the child is killed")

	n, err := OwnOOMKills()
	require.NoError(t, err)
	t.Logf("oom_kills=%d", n)
	_, _ = os.Stdout.WriteString("oom_kills=" + strconv.FormatInt(n, 10) + "\n") // read by the parent test
}
