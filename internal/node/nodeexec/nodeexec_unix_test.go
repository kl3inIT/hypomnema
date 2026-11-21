//go:build unix

package nodeexec

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// A command runs as `sh -c` where it is asked to, and its answer is its exit
// code and its output, stdout and stderr together; one too long keeps its
// start and its end.
func TestACommandAnswersWithItsExitCodeAndOutput(t *testing.T) {
	t.Parallel()

	m := New()
	t.Cleanup(m.Close)

	dir := t.TempDir()

	got := m.Run(t.Context(), Request{Command: "pwd; echo oops >&2; exit 3", Cwd: dir})
	require.Empty(t, got.Error)
	require.NotNil(t, got.ExitCode)
	assert.Equal(t, 3, *got.ExitCode)
	assert.False(t, got.Running)

	resolved, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	assert.Equal(t, resolved+"\noops\n", got.Output)

	long := m.Run(t.Context(), Request{Command: "seq 1 100000"})
	require.NotNil(t, long.ExitCode)
	assert.Equal(t, 0, *long.ExitCode)
	assert.Positive(t, long.CutBytes)
	assert.True(t, strings.HasPrefix(long.Output, "1\n2\n3\n"), "the start is kept")
	assert.True(t, strings.HasSuffix(long.Output, "99999\n100000\n"), "the end is kept")
}

// A command whose time runs out is killed with what it started, and says so.
func TestACommandWhoseTimeRunsOutIsKilledWithItsChildren(t *testing.T) {
	t.Parallel()

	m := New()
	t.Cleanup(m.Close)

	marker := filepath.Join(t.TempDir(), "child-ran")

	started := time.Now()
	got := m.Run(t.Context(), Request{
		Command: "echo begun; (sleep 2; touch " + marker + ") & sleep 30", Timeout: 500 * time.Millisecond,
	})

	assert.Less(t, time.Since(started), 10*time.Second)
	assert.True(t, got.TimedOut)
	assert.Equal(t, "begun\n", got.Output)

	time.Sleep(3 * time.Second)
	assert.NoFileExists(t, marker, "the child the command started was killed with it")
}

// Scenario: a long command runs in the background (issue #543, Coder's
// processes). It answers at once with its id; its output is read while it
// runs and once it has ended; another is killed with what it started; an id
// the node does not know says so.
func TestABackgroundCommandIsReadWhileItRunsAndKilled(t *testing.T) {
	t.Parallel()

	m := New()
	t.Cleanup(m.Close)

	started := m.Start(Request{Command: "echo first; sleep 1; echo second"})
	require.Empty(t, started.Error)
	assert.True(t, started.Running)
	require.NotEmpty(t, started.ProcessID)

	now := m.Output(t.Context(), "", started.ProcessID, 0)
	assert.True(t, now.Running)

	ended := m.Output(t.Context(), "", started.ProcessID, 30*time.Second)
	assert.False(t, ended.Running)
	require.NotNil(t, ended.ExitCode)
	assert.Equal(t, 0, *ended.ExitCode)
	assert.Equal(t, "first\nsecond\n", ended.Output)
	assert.Equal(t, started.ProcessID, ended.ProcessID)

	endless := m.Start(Request{Command: "echo up; sleep 300"})
	require.Eventually(t, func() bool {
		return m.Output(t.Context(), "", endless.ProcessID, 0).Output == "up\n"
	}, 10*time.Second, 20*time.Millisecond)

	killed := m.Kill("", endless.ProcessID)
	assert.False(t, killed.Running)
	assert.Equal(t, "up\n", killed.Output)

	assert.Contains(t, m.Output(t.Context(), "", "proc_unknown", 0).Error, "no process")
}

// sudo is given the node's password through its helper, which is gone once
// the command ends, and the password never comes back in the output; a
// command without the password gets no helper. The sudo here is a stand-in
// on PATH that prints what its helper gives.
func TestSudoGetsTheNodesPasswordFromItsHelper(t *testing.T) {
	bin := t.TempDir()
	fake := "#!/bin/sh\n[ \"$1\" = -A ] || { echo 'no -A'; exit 1; }\nshift\necho \"helper says $(\"$SUDO_ASKPASS\")\"\n" +
		"echo \"$SUDO_ASKPASS\" > " + filepath.Join(bin, "helper-was") + "\n\"$@\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "sudo"), []byte(fake), 0o700)) //nolint:gosec // the stand-in runs
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	m := New()
	t.Cleanup(m.Close)

	got := m.Run(t.Context(), Request{Command: "sudo echo done", SudoPassword: "s3cret pass"})
	require.NotNil(t, got.ExitCode, got.Error)
	assert.Equal(t, 0, *got.ExitCode, got.Output)
	assert.Equal(t, "helper says [sudo password hidden]\ndone\n", got.Output)

	helper, err := os.ReadFile(filepath.Join(bin, "helper-was")) //nolint:gosec // G304: the test's own file
	require.NoError(t, err)
	assert.NoFileExists(t, strings.TrimSpace(string(helper)), "the helper is removed when the command ends")

	none := m.Run(t.Context(), Request{Command: "sudo echo done"})
	assert.Equal(t, "no -A\n", none.Output)
}

// A command run in the foreground under the id its caller chose (issue #1060) is kept by that id once it ends, so
// that its result can be read when the answer that would have carried it was lost, as when the control plane
// restarts; an id the node did not give the shape of its own, or one another process holds, is refused.
func TestAForegroundCommandIsReadByTheIDItsCallerChose(t *testing.T) {
	t.Parallel()

	m := New()
	t.Cleanup(m.Close)

	id := nodeproto.NewProcessID()

	got := m.Run(t.Context(), Request{ID: id, Command: "echo built"})
	require.Empty(t, got.Error)

	again := m.Output(t.Context(), "", id, 0)
	require.Empty(t, again.Error)
	assert.False(t, again.Running)
	require.NotNil(t, again.ExitCode)
	assert.Equal(t, 0, *again.ExitCode)
	assert.Equal(t, "built\n", again.Output)
	assert.Equal(t, id, again.ProcessID)

	assert.Contains(t, m.Run(t.Context(), Request{ID: id, Command: "echo twice"}).Error, "already runs",
		"an id is one process's")
	assert.Contains(t, m.Start(Request{ID: "../etc", Command: "id"}).Error, "not one this node takes")
	assert.Contains(t, m.Output(t.Context(), "", nodeproto.NewProcessID(), 0).Error, "never started")
}

// A process is the agent's that ran it, a chat's or a worker's (issue #1060, review round 1): another agent that
// knows its id is told there is none, cannot read it and cannot kill it, as Coder keys a process by its chat
// (agent/agentproc/process.go get).
func TestAProcessIsReadOnlyByTheAgentThatRanIt(t *testing.T) {
	t.Parallel()

	m := New()
	t.Cleanup(m.Close)

	id := nodeproto.NewProcessID()

	got := m.Run(t.Context(), Request{ID: id, Agent: "b-cht_a", Command: "echo foreground-only"})
	require.Empty(t, got.Error)

	assert.Equal(t, "foreground-only\n", m.Output(t.Context(), "b-cht_a", id, 0).Output)
	assert.Contains(t, m.Output(t.Context(), "b-cht_b", id, 0).Error, "no process", "another chat")
	assert.Contains(t, m.Output(t.Context(), "d-dsp_1", id, 0).Error, "no process", "a worker")
	assert.Empty(t, m.Output(t.Context(), "d-dsp_1", id, 0).Output)

	endless := m.Start(Request{Agent: "b-cht_a", Command: "sleep 300"})
	assert.Contains(t, m.Kill("b-cht_b", endless.ProcessID).Error, "no process")
	assert.True(t, m.Output(t.Context(), "b-cht_a", endless.ProcessID, 0).Running, "another's kill leaves it")
	assert.False(t, m.Kill("b-cht_a", endless.ProcessID).Running)
}

// At most keepEndedMost ended processes are kept, the oldest dropped first, whether or not anyone asks meanwhile
// (issue #1060, review round 1).
func TestEndedProcessesAreKeptUpToABound(t *testing.T) {
	t.Parallel()

	m := New()
	t.Cleanup(m.Close)

	first := nodeproto.NewProcessID()
	require.Empty(t, m.Run(t.Context(), Request{ID: first, Command: "true"}).Error)

	for range keepEndedMost + 5 {
		require.Empty(t, m.Run(t.Context(), Request{Command: "true"}).Error)
	}

	require.Eventually(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()

		return len(m.procs) == keepEndedMost
	}, 10*time.Second, 10*time.Millisecond)
	assert.Contains(t, m.Output(t.Context(), "", first, 0).Error, "no process", "the oldest went first")
}

// Starts under one id at once run one command: the id is taken before the command starts.
func TestOneIDStartsOneCommand(t *testing.T) {
	t.Parallel()

	m := New()
	t.Cleanup(m.Close)

	marker := filepath.Join(t.TempDir(), "ran")
	id := nodeproto.NewProcessID()

	var wg sync.WaitGroup

	for range 24 {
		wg.Go(func() { m.Start(Request{ID: id, Command: "echo x >> " + marker}) })
	}

	wg.Wait()

	require.Eventually(t, func() bool { return !m.Output(t.Context(), "", id, 0).Running }, 10*time.Second,
		10*time.Millisecond)

	ran, err := os.ReadFile(marker) //nolint:gosec // G304: the test's own file
	require.NoError(t, err)
	assert.Equal(t, "x\n", string(ran))
}

// Scenario (issue #1060, review round 1): a Bot's turn is cut by an update while its command runs, and the id its
// tools server chose went with it. The continued turn lists its own processes on the node, running first, then the
// newest, with each one's id, command and exit code, as Coder's process list does; another agent's are not listed.
func TestAnAgentListsItsOwnProcesses(t *testing.T) {
	t.Parallel()

	m := New()
	t.Cleanup(m.Close)

	require.Empty(t, m.Run(t.Context(), Request{Agent: "b-cht_a", Command: "exit 3"}).Error)
	time.Sleep(5 * time.Millisecond)

	running := m.Start(Request{Agent: "b-cht_a", Command: "sleep 300"})
	require.Empty(t, running.Error)
	require.Empty(t, m.Run(t.Context(), Request{Agent: "b-cht_b", Command: "true"}).Error)

	list := m.List("b-cht_a")
	require.Len(t, list, 2)
	assert.Equal(t, running.ProcessID, list[0].ProcessID, "running first")
	assert.True(t, list[0].Running)
	assert.Equal(t, "sleep 300", list[0].Command)
	assert.Equal(t, "exit 3", list[1].Command)
	require.NotNil(t, list[1].ExitCode)
	assert.Equal(t, 3, *list[1].ExitCode)
	assert.Positive(t, list[1].StartedAt)
	assert.Empty(t, m.List("d-dsp_1"))
}

// The list fits a node's answer whatever the commands were (issue #1060, review round 2): each entry's command is
// cut short, and every listed process's id, state and start come back. A command of 90,000 `<` is half a megabyte
// as JSON.
func TestAListFitsItsAnswerWhateverTheCommands(t *testing.T) {
	t.Parallel()

	m := New()
	t.Cleanup(m.Close)

	long := "true # " + strings.Repeat("<", 90_000)

	for range nodeproto.ExecListMost {
		require.Empty(t, m.Run(t.Context(), Request{Agent: "b-cht_a", Command: long}).Error)
	}

	list := m.List("b-cht_a")
	require.Len(t, list, nodeproto.ExecListMost)

	for _, p := range list {
		assert.True(t, nodeproto.IsProcessID(p.ProcessID))
		require.NotNil(t, p.ExitCode)
		assert.Positive(t, p.StartedAt)
		assert.LessOrEqual(t, len(p.Command), listCommandBytes+len("…"))
		assert.True(t, strings.HasPrefix(p.Command, "true # <<<"))
	}

	raw, err := json.Marshal(nodeproto.ExecAnswer{Processes: list})
	require.NoError(t, err)
	assert.Less(t, len(raw), nodeproto.MaxExecAnswerBytes/10, "far within the answer's bound")
}

// The newest is listed first even when many started in the same millisecond (issue #1060, review round 2).
func TestTheListKeepsTheNewestWhenStartsShareAMillisecond(t *testing.T) {
	t.Parallel()

	m := New()
	t.Cleanup(m.Close)

	at := time.UnixMilli(1_795_000_000_000)
	m.now = func() time.Time { return at }

	var last string

	for range nodeproto.ExecListMost + 1 {
		started := m.Start(Request{Agent: "b-cht_a", Command: "sleep 300"})
		require.Empty(t, started.Error)
		last = started.ProcessID
	}

	list := m.List("b-cht_a")
	require.Len(t, list, nodeproto.ExecListMost)
	assert.Equal(t, last, list[0].ProcessID)
}

// The bound of kept ended processes holds when a command ends before it is kept (issue #1060, review round 2): the
// end's own pruning ran when it was not in the map yet.
func TestTheBoundHoldsForACommandThatEndedBeforeItWasKept(t *testing.T) {
	t.Parallel()

	m := New()
	t.Cleanup(m.Close)

	ended := func(n int) *process {
		code := 0
		p := &process{key: procKey{id: fmt.Sprintf("proc_%012d", n)}, exitCode: &code, done: make(chan struct{}),
			endedAt: m.now().Add(time.Duration(n) * time.Millisecond)}
		p.id = p.key.id
		close(p.done)

		return p
	}

	m.mu.Lock()
	for n := range keepEndedMost {
		m.procs[ended(n).key] = ended(n)
	}
	m.mu.Unlock()

	require.NoError(t, m.keep(ended(keepEndedMost)))

	m.mu.Lock()
	defer m.mu.Unlock()

	assert.Len(t, m.procs, keepEndedMost)
}
