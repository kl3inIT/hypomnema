package shim

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProcStatIdentityUsesStartTicks(t *testing.T) {
	t.Parallel()
	stat, err := parseProcStat("90 (worker ) (name) S 1 42 42 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 12345 0")
	require.NoError(t, err)
	assert.Equal(t, procStat{state: "S", group: 42, session: 42, started: 12345}, stat)
}

func TestShimIdentityHelper(t *testing.T) {
	if dir := os.Getenv("HYPOMNEMA_TEST_SHIM_DIR"); dir != "" {
		require.NoError(t, Main(t.Context(), dir))
	}
}

func TestKillRecordedSignalsVerifiedGroupAfterLeaderExits(t *testing.T) {
	t.Parallel()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	sleep, err := exec.LookPath("sleep")
	require.NoError(t, err)
	require.NoError(t, WriteSpec(dir, Spec{Argv: []string{sleep, "30"}}))
	exe, err := os.Executable()
	require.NoError(t, err)
	leader := exec.CommandContext(t.Context(), exe, "-test.run=^TestShimIdentityHelper$") //nolint:gosec // the test binary
	leader.Env = append(os.Environ(), "HYPOMNEMA_TEST_SHIM_DIR="+dir)
	detach(leader)
	require.NoError(t, leader.Start())
	group := leader.Process.Pid
	t.Cleanup(func() {
		_ = Kill(group)
		_ = leader.Wait()
	})

	var worker workerIdentity
	require.Eventually(t, func() bool {
		raw, err := os.ReadFile(filepath.Join(dir, workerIdentityFile))
		return err == nil && json.Unmarshal(raw, &worker) == nil
	}, 5*time.Second, 10*time.Millisecond)
	require.Positive(t, worker.PID)

	boot, err := bootID()
	require.NoError(t, err)
	stat, err := readProcStat(group)
	require.NoError(t, err)
	raw, err := os.ReadFile(filepath.Join(dir, identityFile))
	require.NoError(t, err)
	var recorded processIdentity
	require.NoError(t, json.Unmarshal(raw, &recorded))
	assert.Equal(t, processIdentity{PID: group, Started: stat.started, BootID: boot, Dir: dir}, recorded)

	require.NoError(t, leader.Process.Kill())
	require.Error(t, leader.Wait())
	require.False(t, Alive(group))
	require.False(t, GroupGone(group))
	workerPath := filepath.Join(dir, workerIdentityFile)
	bad, err := json.Marshal(workerIdentity{PID: worker.PID, Started: worker.Started + 1})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(workerPath, bad, 0o600))
	require.ErrorContains(t, KillRecorded(dir), "identity does not match")
	stat, err = readProcStat(worker.PID)
	require.NoError(t, err)
	require.NotEqual(t, "Z", stat.state)

	good, err := json.Marshal(worker)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(workerPath, good, 0o600))
	require.NoError(t, KillRecorded(dir))
	require.Eventually(t, func() bool {
		stat, err := readProcStat(worker.PID)
		return os.IsNotExist(err) || (err == nil && stat.state == "Z")
	}, 5*time.Second, 10*time.Millisecond)
}
