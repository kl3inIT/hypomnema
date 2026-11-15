//go:build unix

package nodeexec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

	now := m.Output(t.Context(), started.ProcessID, 0)
	assert.True(t, now.Running)

	ended := m.Output(t.Context(), started.ProcessID, 30*time.Second)
	assert.False(t, ended.Running)
	require.NotNil(t, ended.ExitCode)
	assert.Equal(t, 0, *ended.ExitCode)
	assert.Equal(t, "first\nsecond\n", ended.Output)
	assert.Equal(t, started.ProcessID, ended.ProcessID)

	endless := m.Start(Request{Command: "echo up; sleep 300"})

	killed := m.Kill(endless.ProcessID)
	assert.False(t, killed.Running)
	assert.Equal(t, "up\n", killed.Output)

	assert.Contains(t, m.Output(t.Context(), "proc_unknown", 0).Error, "no process")
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
