package diskguard

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestTempSweepKeepsWholeTreesWithFreshProtectedOrOpenEntries(t *testing.T) {
	t.Parallel()
	root := realTempDir(t)
	before := time.Now().Add(-6 * time.Hour)
	old := before.Add(-time.Hour)
	makeTree := func(name string) (dir, child string) {
		dir = filepath.Join(root, name)
		require.NoError(t, os.Mkdir(dir, 0o700))
		child = filepath.Join(dir, "file")
		require.NoError(t, os.WriteFile(child, []byte("data"), 0o600))
		require.NoError(t, os.Chtimes(child, old, old))
		require.NoError(t, os.Chtimes(dir, old, old))
		return dir, child
	}
	stale, _ := makeTree("stale")
	protected, _ := makeTree("protected")
	_, fresh := makeTree("fresh-child")
	require.NoError(t, os.Chtimes(fresh, time.Now(), time.Now()))
	_, opened := makeTree("open-fd")
	fd, err := os.Open(opened) //nolint:gosec // test fixture
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, fd.Close()) })
	_, mapped := makeTree("mapped")
	f, err := os.Open(mapped) //nolint:gosec // test fixture
	require.NoError(t, err)
	mapping, err := unix.Mmap(int(f.Fd()), 0, 4, unix.PROT_READ, unix.MAP_SHARED)
	require.NoError(t, err)
	require.NoError(t, f.Close())
	t.Cleanup(func() { assert.NoError(t, unix.Munmap(mapping)) })
	cwd, _ := makeTree("cwd")
	cmd := exec.CommandContext(t.Context(), "sleep", "30")
	cmd.Dir = cwd
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	proc := realTempDir(t)
	for _, pid := range []int{os.Getpid(), cmd.Process.Pid} {
		name := strconv.Itoa(pid)
		require.NoError(t, os.Symlink(filepath.Join("/proc", name), filepath.Join(proc, name)))
	}
	ctx := WithProtection(t.Context(), protected)
	scans := 0
	require.NoError(t, sweepTemp(ctx, root, "", before, func() (processFiles, error) {
		scans++
		open, skipped, err := allProcessFiles(ctx, proc, os.Geteuid())
		assert.Zero(t, skipped)
		return open, err
	}))
	assert.Equal(t, 1, scans)
	assert.NoDirExists(t, stale)
	assert.DirExists(t, protected)
	assert.FileExists(t, fresh)
	assert.FileExists(t, opened)
	assert.FileExists(t, mapped)
	assert.DirExists(t, cwd)
}

func TestTempSweepSkipsForeignAndOpaqueProcesses(t *testing.T) {
	t.Parallel()
	root := realTempDir(t)
	old := time.Now().Add(-7 * time.Hour)
	proc := realTempDir(t)
	stale := filepath.Join(root, "stale")
	observed := filepath.Join(root, "observed")
	agent := filepath.Join(root, "agent")
	for _, dir := range []string{stale, observed, agent} {
		require.NoError(t, os.Mkdir(dir, 0o700))
	}
	socket := filepath.Join(agent, "sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, listener.Close()) })
	for _, path := range []string{stale, observed, agent, socket} {
		require.NoError(t, os.Chtimes(path, old, old))
	}
	uid := os.Geteuid()
	for _, process := range []struct {
		pid string
		uid int
	}{{"1", uid + 1}, {"2", uid}} {
		path := filepath.Join(proc, process.pid)
		task := filepath.Join(path, "task", process.pid)
		require.NoError(t, os.MkdirAll(task, 0o700))
		status := fmt.Sprintf("Name:\ttest\nUid:\t%d\t%d\t%d\t%d\n", process.uid, process.uid, process.uid, process.uid)
		require.NoError(t, os.WriteFile(filepath.Join(path, "status"), []byte(status), 0o600))
		require.NoError(t, os.Symlink("maps", filepath.Join(path, "maps")))
		if process.uid == uid {
			require.NoError(t, os.Symlink(observed, filepath.Join(task, "cwd")))
		}
	}
	require.NoError(t, sweepTemp(t.Context(), root, "", time.Now().Add(-6*time.Hour), func() (processFiles, error) {
		open, skipped, err := allProcessFiles(t.Context(), proc, uid)
		assert.Equal(t, 1, skipped)
		return open, err
	}))
	assert.NoDirExists(t, stale)
	assert.DirExists(t, observed)
	_, err = os.Lstat(socket)
	require.NoError(t, err)
}

func TestTempSweepNeverFollowsSymlinks(t *testing.T) {
	t.Parallel()
	root := realTempDir(t)
	outside := realTempDir(t)
	data := filepath.Join(outside, "data")
	require.NoError(t, os.WriteFile(data, []byte("keep"), 0o600))
	old := time.Now().Add(-7 * time.Hour)
	require.NoError(t, os.Chtimes(data, old, old))
	require.NoError(t, os.Chtimes(outside, old, old))
	stale := filepath.Join(root, "stale")
	require.NoError(t, os.Mkdir(stale, 0o700))
	top := filepath.Join(root, "top-link")
	inner := filepath.Join(stale, "inner-link")
	for _, link := range []string{top, inner} {
		require.NoError(t, os.Symlink(outside, link))
		stamp := unix.NsecToTimespec(old.UnixNano())
		require.NoError(t, unix.UtimesNanoAt(unix.AT_FDCWD, link, []unix.Timespec{stamp, stamp}, unix.AT_SYMLINK_NOFOLLOW))
	}
	require.NoError(t, os.Chtimes(stale, old, old))
	require.NoError(t, sweepTemp(t.Context(), root, "", time.Now().Add(-6*time.Hour), func() (processFiles, error) {
		return make(processFiles), nil
	}))
	assert.NoDirExists(t, stale)
	target, err := os.Readlink(top)
	require.NoError(t, err)
	assert.Equal(t, outside, target)
	content, err := os.ReadFile(data) //nolint:gosec // test fixture
	require.NoError(t, err)
	assert.Equal(t, "keep", string(content))
}
