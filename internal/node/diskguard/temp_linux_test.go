package diskguard

import (
	"context"
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
	outside := realTempDir(t)
	link := filepath.Join(root, "symlink")
	require.NoError(t, os.Symlink(outside, link))
	ctx := WithProtection(t.Context(), protected)
	require.NoError(t, sweepTemp(ctx, root, "", before, func(context.Context) (processFiles, error) {
		open := make(processFiles)
		if err := open.addProcess("/proc/self"); err != nil {
			return nil, err
		}
		return open, open.addProcess(filepath.Join("/proc", strconv.Itoa(cmd.Process.Pid)))
	}))
	assert.NoDirExists(t, stale)
	assert.DirExists(t, protected)
	assert.FileExists(t, fresh)
	assert.FileExists(t, opened)
	assert.FileExists(t, mapped)
	assert.DirExists(t, cwd)
	_, err = os.Lstat(link)
	require.NoError(t, err)
	assert.DirExists(t, outside)
}

func TestTempSweepKeepsStaleEntriesWhenProcIsUnreadable(t *testing.T) {
	t.Parallel()
	root := realTempDir(t)
	stale := filepath.Join(root, "stale")
	old := time.Now().Add(-7 * time.Hour)
	require.NoError(t, os.WriteFile(stale, nil, 0o600))
	require.NoError(t, os.Chtimes(stale, old, old))
	err := sweepTemp(t.Context(), root, "", time.Now().Add(-6*time.Hour), func(context.Context) (processFiles, error) {
		return nil, os.ErrPermission
	})
	require.ErrorIs(t, err, os.ErrPermission)
	assert.FileExists(t, stale)
}

func TestProcessScanSkipsUnreadablePIDsAndKeepsReadableReferences(t *testing.T) {
	t.Parallel()
	proc := t.TempDir()
	for _, pid := range []string{"1", "2"} {
		require.NoError(t, os.Mkdir(filepath.Join(proc, pid), 0o700))
	}
	identity := fileIdentity{device: 12, inode: 34}
	open, err := readProcessFiles(t.Context(), proc, os.Geteuid(), func(open processFiles, path string) error {
		if filepath.Base(path) == "1" {
			return &os.PathError{Op: "open", Path: filepath.Join(path, "maps"), Err: os.ErrPermission}
		}
		open[identity] = struct{}{}
		return nil
	})
	require.NoError(t, err)
	assert.Contains(t, open, identity)

	open, err = readProcessFiles(t.Context(), proc, os.Geteuid()+1, func(processFiles, string) error {
		t.Error("a PID of another UID should not be inspected")
		return nil
	})
	require.NoError(t, err)
	assert.Empty(t, open)
}
