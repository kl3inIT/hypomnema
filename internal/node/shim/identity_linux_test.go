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

	"github.com/kl3inIT/hypomnema/internal/node/tempns"
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
	return startIdentitySpec(t, Spec{Argv: argv})
}

func startIdentitySpec(t *testing.T, spec Spec) (*os.Root, *exec.Cmd) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, root.Close()) })
	if spec.Dir == "" {
		spec.Dir = dir
	}
	require.NoError(t, WriteSpec(dir, spec))
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

func TestIsolatedWorkerExitAndOrphanedGroup(t *testing.T) {
	t.Parallel()
	isolation := tempns.Probe(t.Context(), "")
	if !isolation.On() {
		t.Skip(isolation.Reason)
	}
	for _, scenario := range []struct {
		name   string
		script string
		want   Exit
	}{
		{
			name: "exit",
			script: `test -f /tmp/fixture || exit 2
				/bin/sleep 30 >/dev/null 2>&1 </dev/null & printf '%s' "$!" > /tmp/child; exit 42`,
			want: Exit{Code: 42},
		},
		{name: "signal", script: `kill -TERM "$$"`, want: Exit{Code: -1, Signal: "terminated"}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			temp := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(temp, "fixture"), nil, 0o600))
			root, leader := startIdentitySpec(t, Spec{
				Argv: isolation.Wrap([]string{"/bin/sh", "-c", scenario.script}, temp), Dir: "/", Bwrap: true,
			})
			require.NoError(t, leader.Wait())
			exit, done, err := ReadExit(root.Name())
			require.NoError(t, err)
			require.True(t, done)
			assert.Equal(t, scenario.want, exit)
			if scenario.name == "exit" {
				raw, err := os.ReadFile(filepath.Join(temp, "child"))
				require.NoError(t, err)
				child, err := strconv.Atoi(string(raw))
				require.NoError(t, err)
				requireProcessKeepsRunning(t, child)
				require.NoError(t, KillRecorded(root.Name()))
				requireProcessStopped(t, child)
			}
		})
	}
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
	require.NoError(t, KillRecorded(dir))
	requireProcessStopped(t, worker.PID)
}

func startOrphanedGroupFixture(t *testing.T) (*os.Root, int) {
	t.Helper()
	shell, err := exec.LookPath("sh")
	require.NoError(t, err)
	root, leader := startIdentityFixture(t, shell, "-c",
		`/bin/sleep 30 >/dev/null 2>&1 </dev/null & printf '%s' "$!" > child`)
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
	return root, child
}

func TestKillRecordedSignalsBackgroundChildAfterShimAndWorkerExit(t *testing.T) {
	t.Parallel()
	root, child := startOrphanedGroupFixture(t)
	dir := root.Name()

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
	requireProcessStopped(t, child)
}

func startUnrelatedProcess(t *testing.T) int {
	t.Helper()
	unrelated := exec.CommandContext(t.Context(), "sleep", "30")
	detach(unrelated)
	require.NoError(t, unrelated.Start())
	t.Cleanup(func() {
		_ = unrelated.Process.Kill()
		_ = unrelated.Wait()
	})
	return unrelated.Process.Pid
}

func TestKillRecordedSignalsOrphanedGroupWhenWorkerPIDWasReused(t *testing.T) {
	t.Parallel()
	root, child := startOrphanedGroupFixture(t)
	pid := startUnrelatedProcess(t)
	stat, err := readProcStat(pid)
	require.NoError(t, err)
	require.Greater(t, stat.started, int64(1))
	encoded, err := json.Marshal(workerIdentity{PID: pid, Started: stat.started - 1})
	require.NoError(t, err)
	require.NoError(t, root.WriteFile(workerIdentityFile, encoded, 0o600))

	require.NoError(t, KillRecorded(root.Name()))
	requireProcessStopped(t, child)
	stat, err = readProcStat(pid)
	require.NoError(t, err)
	assert.NotEqual(t, "Z", stat.state)
}

func TestKillRecordedRejectsMatchingWorkerOutsideShimGroupAndSession(t *testing.T) {
	t.Parallel()
	root, child := startOrphanedGroupFixture(t)
	require.NoError(t, recordWorker(root, startUnrelatedProcess(t)))

	err := KillRecorded(root.Name())
	requireProcessKeepsRunning(t, child)
	require.ErrorContains(t, err, "orphaned shim group identity does not match")
}

func TestKillRecordedDoesNotSignalOrphanedLegacyGroup(t *testing.T) {
	t.Parallel()
	root, child := startOrphanedGroupFixture(t)
	require.NoError(t, root.Remove(identityFile))
	require.NoError(t, root.Remove(workerIdentityFile))

	err := KillRecorded(root.Name())
	requireProcessKeepsRunning(t, child)
	require.Error(t, err)
}

func TestMatchesSessionMember(t *testing.T) {
	t.Parallel()
	identity := processIdentity{PID: 42, Group: 42, Session: 42, Started: 100}
	for _, test := range []struct {
		name string
		stat procStat
		want bool
	}{
		{name: "same group and session at same tick", stat: procStat{group: 42, session: 42, started: 100}, want: true},
		{name: "same group and session at later tick", stat: procStat{group: 42, session: 42, started: 101}, want: true},
		{name: "different group", stat: procStat{group: 43, session: 42, started: 101}},
		{name: "different session", stat: procStat{group: 42, session: 43, started: 101}},
		{name: "older than leader", stat: procStat{group: 42, session: 42, started: 99}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, test.want, matchesSessionMember(test.stat, identity))
		})
	}
}

func requireProcessStopped(t *testing.T, pid int) {
	t.Helper()
	require.Eventually(t, func() bool {
		stat, err := readProcStat(pid)
		return os.IsNotExist(err) || (err == nil && stat.state == "Z")
	}, 5*time.Second, 10*time.Millisecond)
}

func requireProcessKeepsRunning(t *testing.T, pid int) {
	t.Helper()
	require.Never(t, func() bool {
		stat, err := readProcStat(pid)
		return err != nil || stat.state == "Z"
	}, 200*time.Millisecond, 10*time.Millisecond, "process %d stopped", pid)
}
