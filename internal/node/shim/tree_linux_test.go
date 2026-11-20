package shim

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStopKillsWithoutWaitingForUninterruptibleChild(t *testing.T) {
	t.Parallel()
	child := exec.CommandContext(t.Context(), "sleep", "60")
	require.NoError(t, child.Start())
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	pid := child.Process.Pid
	info, ok := readProcess(pid)
	require.True(t, ok)
	tree := workerTree{root: os.Getpid(), read: readProcess, snapshot: func() (map[int]processInfo, error) {
		return map[int]processInfo{pid: info}, nil
	}}
	sent, err := tree.kill(25 * time.Millisecond)
	require.NoError(t, err)
	require.True(t, sent)
	var exit *exec.ExitError
	require.ErrorAs(t, child.Wait(), &exit)
	assert.Equal(t, syscall.SIGKILL.String(), signalOf(exit.ProcessState))
}

func TestShimReapsOrphansWhileCLIIsRunning(t *testing.T) {
	t.Parallel()
	dir, pid, child := startTestShim(t, "orphan-parent")
	require.Eventually(t, func() bool {
		info, found := readProcess(child)
		return found && info.parent == pid
	}, 5*time.Second, 20*time.Millisecond)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "exit-orphan"), nil, 0o600))
	assert.Eventually(t, func() bool { return !Alive(child) }, 4*time.Second, 20*time.Millisecond)
	assert.True(t, Alive(pid))
	assert.NoFileExists(t, dir+"/"+ExitFile)
}
