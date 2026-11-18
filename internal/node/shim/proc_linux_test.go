package shim

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTimeoutKillsBashInAnotherSession(t *testing.T) {
	t.Parallel()

	shell := exec.CommandContext(t.Context(), "bash", "-c", "setsid bash -c 'sleep 60; :' & echo $!; wait")
	detach(shell)
	stdout, err := shell.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, shell.Start())
	t.Cleanup(func() {
		_ = shell.Process.Kill()
		_ = shell.Wait()
	})

	line := bufio.NewScanner(stdout)
	require.True(t, line.Scan())
	child, err := strconv.Atoi(line.Text())
	require.NoError(t, err)
	t.Cleanup(func() { _ = syscall.Kill(child, syscall.SIGKILL) })
	require.Eventually(t, func() bool {
		group, err := syscall.Getpgid(child)
		return err == nil && child == group
	}, time.Second, 10*time.Millisecond)

	require.NoError(t, KillAndRecordExit(t.TempDir(), shell.Process.Pid, "test timeout"))
	assert.Eventually(t, func() bool {
		raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(child), "stat"))
		return os.IsNotExist(err) || strings.HasPrefix(string(raw[strings.LastIndexByte(string(raw), ')')+1:]), " Z ")
	}, time.Second, 10*time.Millisecond, "the background Bash job survived the shim")
}
