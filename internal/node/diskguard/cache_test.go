//go:build linux || darwin

package diskguard

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMain(tests *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == ScopedSubcommand {
		if err := ScopedExec(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	os.Exit(tests.Run())
}

func realTempDir(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	return path
}

func TestNativePruneCannotMutateThroughALateSymlink(t *testing.T) {
	t.Parallel()
	if !ScopeAvailable() {
		t.Skip("Landlock is unavailable")
	}
	root := realTempDir(t)
	store, outside := filepath.Join(root, "store"), filepath.Join(root, "open-run")
	for _, dir := range []string{store, outside} {
		require.NoError(t, os.Mkdir(dir, 0o700))
	}
	keep := filepath.Join(outside, "state.db")
	require.NoError(t, os.WriteFile(keep, nil, 0o600))
	handle, err := OpenDirFile(store)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, handle.Close()) })
	require.NoError(t, os.Symlink(outside, filepath.Join(store, "swapped")))
	cmd, err := ScopedCommand(t.Context(), []Scope{{Dir: handle}}, "/bin/sh", "-ec",
		`touch "$1/allowed"; rm "$1/swapped/state.db"`, "prune", store)
	require.NoError(t, err)
	require.Error(t, cmd.Run())
	assert.FileExists(t, filepath.Join(store, "allowed"))
	assert.FileExists(t, keep)
}

func TestRemoveTreeCannotFollowSwappedParent(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"tmp", "worktree"} {
		root := realTempDir(t)
		parent, outside := filepath.Join(root, "parent"), filepath.Join(root, "outside")
		for _, dir := range []string{parent, outside} {
			require.NoError(t, os.MkdirAll(filepath.Join(dir, name), 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(dir, name, "state.db"), nil, 0o600))
		}
		stopped := func() bool {
			require.NoError(t, os.Rename(parent, parent+"-old"))
			require.NoError(t, os.Symlink(outside, parent))
			return true
		}
		require.Error(t, RemoveTree(t.Context(), filepath.Join(parent, name), stopped))
		require.FileExists(t, filepath.Join(outside, name, "state.db"))
	}
}

func TestPnpmRejectsStoreSymlinkAndLeavesDefaultDLXCacheAlone(t *testing.T) {
	t.Parallel()
	home := realTempDir(t)
	bin := filepath.Join(home, ".local", "bin")
	store := pnpmStore(home)
	version := filepath.Join(store, "v11")
	outside := filepath.Join(home, "open-run")
	dlx := filepath.Join(home, ".cache", "pnpm", "dlx", "old")
	for _, dir := range []string{bin, version, outside, filepath.Dir(dlx)} {
		require.NoError(t, os.MkdirAll(dir, 0o700))
	}
	require.NoError(t, os.WriteFile(dlx, nil, 0o600))
	script := []byte(`#!/bin/sh
if [ "$4" = path ]; then
  printf '%s/v11\n' "$2"
elif [ "$4" = prune ]; then
  case "$XDG_CACHE_HOME" in "$2"/*) ;; *) exit 2 ;; esac
  [ "$XDG_RUNTIME_DIR" = "$XDG_CACHE_HOME" ] || exit 3
  : > "$XDG_RUNTIME_DIR/lock"
  : > "$2/pruned"
else
  exit 4
fi
`)
	require.NoError(t, os.WriteFile(filepath.Join(bin, "pnpm"), script, 0o700)) //nolint:gosec // executable fixture
	caches := Caches{UserHome: home, WorkerPath: func() string { return bin }}
	link := filepath.Join(version, "files")
	require.NoError(t, os.Symlink(outside, link))
	require.ErrorContains(t, caches.prunePnpm(context.Background(), home), "symlink")
	assert.NoFileExists(t, filepath.Join(store, "pruned"))
	require.NoError(t, os.Remove(link))
	if runtime.GOOS == "darwin" {
		require.True(t, ScopeAvailable(), "the released macOS node can run native pnpm prune")
	}
	if !ScopeAvailable() {
		t.Skip("Landlock is not available for the native command")
	}
	require.NoError(t, caches.prunePnpm(t.Context(), home))
	assert.FileExists(t, filepath.Join(store, "pruned"))
	assert.FileExists(t, dlx)
	assert.Len(t, mustReadDir(t, store), 2, "the native command's isolated cache and locks are reclaimed")
}

func mustReadDir(t *testing.T, path string) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(path)
	require.NoError(t, err)
	return entries
}

func TestGoCacheTrimStopsAtTargetAndKeepsNewAndUnknownFiles(t *testing.T) {
	t.Parallel()
	root := realTempDir(t)
	bucket := filepath.Join(root, "01")
	require.NoError(t, os.Mkdir(bucket, 0o700))
	now := time.Now()
	var paths []string
	for index := range goCacheBatch + 1 {
		path := filepath.Join(bucket, fmt.Sprintf("01%062x-d", index))
		date := now.Add(-goCacheAge - time.Duration(goCacheBatch+1-index)*time.Hour)
		require.NoError(t, os.WriteFile(path, nil, 0o600))
		require.NoError(t, os.Chtimes(path, date, date))
		paths = append(paths, path)
	}
	fresh := filepath.Join(bucket, fmt.Sprintf("01%062x-a", 0))
	unknown := filepath.Join(bucket, "README")
	require.NoError(t, os.WriteFile(fresh, nil, 0o600))
	require.NoError(t, os.WriteFile(unknown, nil, 0o600))
	old := now.Add(-goCacheAge - time.Hour)
	require.NoError(t, os.Chtimes(unknown, old, old))
	fuzz := filepath.Join(root, "fuzz")
	require.NoError(t, os.Mkdir(fuzz, 0o700))
	checks := 0
	require.NoError(t, trimGo(t.Context(), root, root, now.Add(-goCacheAge), func() bool {
		checks++
		return checks <= 2
	}, 0, nil))
	assert.NoFileExists(t, paths[0])
	assert.NoFileExists(t, paths[goCacheBatch-1])
	assert.FileExists(t, paths[goCacheBatch])
	assert.FileExists(t, fresh)
	assert.FileExists(t, unknown)
	assert.DirExists(t, fuzz)
}

func TestGoCacheOnAnotherMountIsNeverTrimmed(t *testing.T) {
	t.Parallel()
	volume := realTempDir(t)
	if !RealDir("/dev/shm") || SameVolume(volume, "/dev/shm") {
		t.Skip("a separate /dev/shm mount is needed")
	}
	require.NoError(t, trimGo(t.Context(), volume, "/dev/shm", time.Now(), func() bool {
		t.Error("a cache on a different mount must not be inspected or trimmed")
		return false
	}, 0, nil))
}

func TestGoCachePressureTrimsWhenWorktreeRootIsASymlink(t *testing.T) {
	t.Parallel()
	home := realTempDir(t)
	worktrees := filepath.Join(home, "worktrees")
	link := filepath.Join(home, "linked-worktrees")
	require.NoError(t, os.Mkdir(worktrees, 0o700))
	require.NoError(t, os.Symlink(worktrees, link))
	cache := filepath.Join(home, "go-build")
	bucket := filepath.Join(cache, "01")
	require.NoError(t, os.MkdirAll(bucket, 0o700))
	entry := filepath.Join(bucket, fmt.Sprintf("01%062x-d", 0))
	require.NoError(t, os.WriteFile(entry, []byte("bytes"), 0o600))
	old := time.Now().Add(-goCacheAge - time.Hour)
	require.NoError(t, os.Chtimes(entry, old, old))

	caches := Caches{WorktreeRoot: link, Logger: slog.New(slog.DiscardHandler)}
	caches.trimGoCache(t.Context(), cache, caches.worktreeVolume(), func() bool { return true })
	assert.NoFileExists(t, entry)
}

func TestGoCacheCapTrimsRecentEntriesOldestFirstWithoutPressure(t *testing.T) {
	t.Parallel()
	root := realTempDir(t)
	bucket := filepath.Join(root, "01")
	require.NoError(t, os.Mkdir(bucket, 0o700))
	now := time.Now()
	var entries []string
	for index := range 3 {
		entry := filepath.Join(bucket, fmt.Sprintf("01%062x-d", index))
		require.NoError(t, os.WriteFile(entry, []byte("data"), 0o600))
		lastUse := now.Add(-time.Duration(3-index) * time.Minute)
		require.NoError(t, os.Chtimes(entry, lastUse, lastUse))
		entries = append(entries, entry)
	}
	require.NoError(t, trimGo(t.Context(), root, root, now.Add(-goCacheAge), func() bool { return false }, 8, nil))
	assert.NoFileExists(t, entries[0])
	assert.FileExists(t, entries[1])
	assert.FileExists(t, entries[2])
}

func TestGoCacheCapIncludesExecutableOutputs(t *testing.T) {
	t.Parallel()
	root := realTempDir(t)
	bucket := filepath.Join(root, "01")
	output := filepath.Join(bucket, fmt.Sprintf("01%062x-d", 0))
	require.NoError(t, os.MkdirAll(output, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(output, "tool"), []byte("executable"), 0o600))
	require.NoError(t, trimGo(t.Context(), root, root, time.Time{}, func() bool { return false }, 4, nil))
	assert.NoDirExists(t, output)
}

func TestConfiguredGoCacheTrimKeepsModuleCache(t *testing.T) {
	home := realTempDir(t)
	custom := filepath.Join(home, "custom")
	module := filepath.Join(home, "modules")
	t.Setenv("GOCACHE", custom)
	t.Setenv("GOMODCACHE", module)
	t.Setenv("GOENV", "off")
	caches := Caches{UserHome: home, Policy: DefaultPolicy(), Logger: slog.New(slog.DiscardHandler)}
	caches.Policy.GoCacheMaxBytes = 1
	roots, mod, err := caches.goCaches(t.Context())
	require.NoError(t, err)
	require.Contains(t, roots, custom)
	require.Contains(t, roots, filepath.Join(home, ".cache", "go-build"))
	require.Equal(t, module, mod)
	for _, root := range []string{custom, module} {
		bucket := filepath.Join(root, "01")
		require.NoError(t, os.MkdirAll(bucket, 0o700))
		entry := filepath.Join(bucket, fmt.Sprintf("01%062x-d", 0))
		require.NoError(t, os.WriteFile(entry, []byte("bytes"), 0o600))
		caches.trimGoCache(WithProtection(t.Context(), mod), root, home, func() bool { return false })
		if root == module {
			assert.FileExists(t, entry)
		} else {
			assert.NoFileExists(t, entry)
		}
	}
}

func TestGoCacheContinuesAfterAnUndeletableEntry(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("unlink permission must not be bypassed by root")
	}
	root := realTempDir(t)
	blocked, later := filepath.Join(root, "01"), filepath.Join(root, "02")
	now := time.Now()
	var entries []string
	for index, bucket := range []string{blocked, later} {
		require.NoError(t, os.Mkdir(bucket, 0o700))
		file := filepath.Join(bucket, fmt.Sprintf("%s%062x-d", filepath.Base(bucket), index))
		require.NoError(t, os.WriteFile(file, nil, 0o600))
		old := now.Add(-goCacheAge - time.Duration(2-index)*time.Hour)
		require.NoError(t, os.Chtimes(file, old, old))
		entries = append(entries, file)
	}
	require.NoError(t, os.Chmod(blocked, 0o500))                      //nolint:gosec // directory needs to be searchable
	t.Cleanup(func() { assert.NoError(t, os.Chmod(blocked, 0o700)) }) //nolint:gosec // restore the test directory
	err := trimGo(t.Context(), root, root, now.Add(-goCacheAge), func() bool { return true }, 0, nil)
	require.ErrorIs(t, err, os.ErrPermission)
	assert.FileExists(t, entries[0])
	assert.NoFileExists(t, entries[1])
}
