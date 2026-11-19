package shim

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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

func startIdentityFixture(t *testing.T, argv ...string) (*os.Root, *exec.Cmd) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, root.Close()) })
	require.NoError(t, WriteSpec(dir, Spec{Argv: argv, Dir: dir}))
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
	return root, leader
}

func TestKillRecordedSignalsVerifiedGroupAfterLeaderExits(t *testing.T) {
	t.Parallel()
	sleep, err := exec.LookPath("sleep")
	require.NoError(t, err)
	root, leader := startIdentityFixture(t, sleep, "30")
	dir := root.Name()
	group := leader.Process.Pid

	var worker workerIdentity
	require.Eventually(t, func() bool {
		raw, err := root.ReadFile(workerIdentityFile)
		return err == nil && json.Unmarshal(raw, &worker) == nil
	}, 5*time.Second, 10*time.Millisecond)
	require.Positive(t, worker.PID)

	boot, err := bootID()
	require.NoError(t, err)
	stat, err := readProcStat(group)
	require.NoError(t, err)
	raw, err := root.ReadFile(identityFile)
	require.NoError(t, err)
	var recorded processIdentity
	require.NoError(t, json.Unmarshal(raw, &recorded))
	assert.Equal(t, processIdentity{
		PID: group, Started: stat.started, BootID: boot, Dir: dir, Group: int64(group), Session: int64(group),
	}, recorded)

	wrongBoot := recorded
	wrongBoot.BootID += "-different"
	encoded, err := json.Marshal(wrongBoot)
	require.NoError(t, err)
	require.NoError(t, root.WriteFile(identityFile, encoded, 0o600))
	require.ErrorContains(t, KillRecorded(dir), "another boot")
	assert.True(t, Alive(group))
	require.NoError(t, root.WriteFile(identityFile, raw, 0o600))

	require.NoError(t, leader.Process.Kill())
	require.Error(t, leader.Wait())
	require.False(t, Alive(group))
	require.False(t, GroupGone(group))
	bad, err := json.Marshal(workerIdentity{PID: worker.PID, Started: worker.Started + 1})
	require.NoError(t, err)
	require.NoError(t, root.WriteFile(workerIdentityFile, bad, 0o600))
	require.ErrorContains(t, KillRecorded(dir), "identity does not match")
	stat, err = readProcStat(worker.PID)
	require.NoError(t, err)
	require.NotEqual(t, "Z", stat.state)

	good, err := json.Marshal(worker)
	require.NoError(t, err)
	require.NoError(t, root.WriteFile(workerIdentityFile, good, 0o600))
	require.NoError(t, KillRecorded(dir))
	require.Eventually(t, func() bool {
		stat, err := readProcStat(worker.PID)
		return os.IsNotExist(err) || (err == nil && stat.state == "Z")
	}, 5*time.Second, 10*time.Millisecond)
}

func TestKillRecordedSignalsBackgroundChildAfterShimAndWorkerExit(t *testing.T) {
	t.Parallel()
	shell, err := exec.LookPath("sh")
	require.NoError(t, err)
	root, leader := startIdentityFixture(t, shell, "-c",
		`/bin/sleep 30 >/dev/null 2>&1 </dev/null & printf '%s' "$!" > child`)
	dir := root.Name()
	require.NoError(t, leader.Wait())
	group := leader.Process.Pid
	require.False(t, Alive(group))

	raw, err := root.ReadFile(workerIdentityFile)
	require.NoError(t, err)
	var worker workerIdentity
	require.NoError(t, json.Unmarshal(raw, &worker))
	_, err = readProcStat(worker.PID)
	require.ErrorIs(t, err, os.ErrNotExist, "the shim already reaped the worker")

	raw, err = root.ReadFile("child")
	require.NoError(t, err)
	child, err := strconv.Atoi(string(raw))
	require.NoError(t, err)
	stat, err := readProcStat(child)
	require.NoError(t, err)
	require.Equal(t, int64(group), stat.group)
	require.Equal(t, int64(group), stat.session)
	require.NotEqual(t, "Z", stat.state)

	recorded, err := root.ReadFile(identityFile)
	require.NoError(t, err)
	var wrongSession processIdentity
	require.NoError(t, json.Unmarshal(recorded, &wrongSession))
	wrongSession.Session++
	encoded, err := json.Marshal(wrongSession)
	require.NoError(t, err)
	require.NoError(t, root.WriteFile(identityFile, encoded, 0o600))
	require.ErrorContains(t, KillRecorded(dir), "not recorded as the group and session leader")
	require.NoError(t, root.WriteFile(identityFile, recorded, 0o600))

	require.NoError(t, KillRecorded(dir))
	require.Eventually(t, func() bool {
		stat, err := readProcStat(child)
		return os.IsNotExist(err) || (err == nil && stat.state == "Z")
	}, 5*time.Second, 10*time.Millisecond)
}
