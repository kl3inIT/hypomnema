//go:build linux || darwin

package diskguard

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
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
	markCache(t, root, testGoReadme)
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
	require.NoError(t, trimGo(t.Context(), root, root, goCacheReadme, now.Add(-goCacheAge), func() bool {
		checks++
		return checks <= 2
	}, nil))
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
	require.NoError(t, trimGo(t.Context(), volume, "/dev/shm", goCacheReadme, time.Now(), func() bool {
		t.Error("a cache on a different mount must not be inspected or trimmed")
		return false
	}, nil))
}

func TestGoCachePressureTrimsWhenWorktreeRootIsASymlink(t *testing.T) {
	home := realTempDir(t)
	worktrees := filepath.Join(home, "worktrees")
	link := filepath.Join(home, "linked-worktrees")
	require.NoError(t, os.Mkdir(worktrees, 0o700))
	require.NoError(t, os.Symlink(worktrees, link))
	cache := filepath.Join(home, "go-build")
	markCache(t, cache, testGoReadme)
	bucket := filepath.Join(cache, "01")
	require.NoError(t, os.MkdirAll(bucket, 0o700))
	entry := filepath.Join(bucket, fmt.Sprintf("01%062x-d", 0))
	require.NoError(t, os.WriteFile(entry, []byte("bytes"), 0o600))
	old := time.Now().Add(-goCacheAge - time.Hour)
	require.NoError(t, os.Chtimes(entry, old, old))

	t.Setenv("GOCACHE", cache)
	t.Setenv("GOMODCACHE", filepath.Join(home, "modules"))
	t.Setenv("GOENV", "off")
	caches := Caches{
		Policy: Policy{FloorBytes: 1}, WorktreeRoot: link, UserHome: home, Logger: slog.New(slog.DiscardHandler),
	}
	caches.Trim(t.Context(), func(func()) { t.Error("must not prune without disk pressure") })
	require.FileExists(t, entry)
	caches.Policy.FloorBytes = 1<<63 - 1
	caches.Trim(t.Context(), func(func()) {})
	assert.NoFileExists(t, entry)
}

func TestConfiguredGoCacheTrimKeepsModuleCache(t *testing.T) {
	home := realTempDir(t)
	custom := filepath.Join(home, "custom")
	module := filepath.Join(home, "modules")
	t.Setenv("GOCACHE", custom)
	t.Setenv("GOMODCACHE", module)
	t.Setenv("GOENV", "off")
	caches := Caches{UserHome: home, Policy: DefaultPolicy(), Logger: slog.New(slog.DiscardHandler)}
	roots, mod, err := caches.goCaches(t.Context())
	require.NoError(t, err)
	require.Contains(t, roots, custom)
	require.Contains(t, roots, filepath.Join(home, ".cache", "go-build"))
	require.Equal(t, module, mod)
	markCache(t, custom, testGoReadme)
	for _, root := range []string{custom, module} {
		bucket := filepath.Join(root, "01")
		require.NoError(t, os.MkdirAll(bucket, 0o700))
		entry := filepath.Join(bucket, fmt.Sprintf("01%062x-d", 0))
		require.NoError(t, os.WriteFile(entry, []byte("bytes"), 0o600))
		old := time.Now().Add(-goCacheAge - time.Hour)
		require.NoError(t, os.Chtimes(entry, old, old))
		caches.trimGoCache(WithProtection(t.Context(), mod), cacheRoot{path: root, readme: goCacheReadme}, home,
			goCacheAge, func() bool { return true })
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
	markCache(t, root, testGoReadme)
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
	err := trimGo(t.Context(), root, root, goCacheReadme, now.Add(-goCacheAge), func() bool { return true }, nil)
	require.ErrorIs(t, err, os.ErrPermission)
	assert.FileExists(t, entries[0])
	assert.NoFileExists(t, entries[1])
}

// The READMEs Go and golangci-lint write in a cache they use (cmd/go/internal/cache/default.go:28-32,47-50 at
// go1.27.4; golangci-lint's internal/go/cache/default.go:26-27,45-48 at v2.15.0).
const (
	testGoReadme = "This directory holds cached build artifacts from the Go build system.\n" +
		"Run \"go clean -cache\" if the directory is getting too large.\n" +
		"Run \"go clean -fuzzcache\" to delete the fuzz cache.\nSee go.dev to learn more about Go.\n"
	testLintReadme = "This directory holds cached build artifacts from golangci-lint.\n"
)

func markCache(t *testing.T, root, readme string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(root, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "README"), []byte(readme), 0o600))
}

func writeCacheEntry(t *testing.T, root string, index int, age time.Duration) string {
	t.Helper()
	bucket := filepath.Join(root, "01")
	require.NoError(t, os.MkdirAll(bucket, 0o700))
	path := filepath.Join(bucket, fmt.Sprintf("01%062x-a", index))
	require.NoError(t, os.WriteFile(path, []byte("bytes"), 0o600))
	date := time.Now().Add(-age)
	require.NoError(t, os.Chtimes(path, date, date))
	return path
}

// Issue #1017: a cache of entries all younger than Go's 5 days, on a volume that still needs room, is trimmed
// oldest-first until it has the room, and an entry used in the last 2 hours is never taken.
func TestYoungGoCacheIsTrimmedOldestFirstToTargetAndKeepsRecentEntries(t *testing.T) {
	t.Parallel()
	root := realTempDir(t)
	markCache(t, root, testGoReadme)
	var paths []string
	for index := range goCacheBatch + 1 {
		paths = append(paths, writeCacheEntry(t, root, index, 4*24*time.Hour-time.Duration(index)*time.Minute*20))
	}
	recent := writeCacheEntry(t, root, goCacheBatch+1, youngCacheAge-10*time.Minute)
	checks := 0
	require.NoError(t, trimGo(t.Context(), root, root, goCacheReadme, time.Now().Add(-youngCacheAge), func() bool {
		checks++
		return checks <= 2
	}, nil))
	assert.NoFileExists(t, paths[0], "the oldest entry goes first")
	assert.NoFileExists(t, paths[goCacheBatch-1])
	assert.FileExists(t, paths[goCacheBatch], "nothing more is taken once the volume has room")
	assert.FileExists(t, recent)

	require.NoError(t, trimGo(t.Context(), root, root, goCacheReadme, time.Now().Add(-youngCacheAge),
		func() bool { return true }, nil))
	assert.NoFileExists(t, paths[goCacheBatch])
	assert.FileExists(t, recent, "an entry used in the last 2 hours is kept however low the volume is")
}

func TestTrimTakesYoungEntriesOfGoAndLintCachesOnlyWhileStillLow(t *testing.T) {
	home := realTempDir(t)
	build := filepath.Join(home, "go-build")
	lint := filepath.Join(home, ".cache", "golangci-lint")
	t.Setenv("GOCACHE", build)
	t.Setenv("GOMODCACHE", filepath.Join(home, "modules"))
	t.Setenv("GOENV", "off")
	t.Setenv("GOLANGCI_LINT_CACHE", "")
	markCache(t, build, testGoReadme)
	markCache(t, lint, testLintReadme)
	var young, recent []string
	for index, root := range []string{build, lint} {
		young = append(young, writeCacheEntry(t, root, 2*index, 24*time.Hour))
		recent = append(recent, writeCacheEntry(t, root, 2*index+1, time.Hour))
	}
	caches := Caches{
		Policy: Policy{FloorBytes: 1}, WorktreeRoot: home, UserHome: home, Logger: slog.New(slog.DiscardHandler),
	}
	assert.False(t, caches.Trim(t.Context(), func(func()) { t.Error("must not prune without disk pressure") }))
	for _, path := range append(slices.Clone(young), recent...) {
		require.FileExists(t, path)
	}

	caches.Policy.FloorBytes = 1<<63 - 1
	assert.True(t, caches.Trim(t.Context(), func(func()) {}), "a volume still below its floor is reported")
	for _, path := range young {
		assert.NoFileExists(t, path)
	}
	for _, path := range recent {
		assert.FileExists(t, path)
	}
}

// Issue #1017, review round 1: the 2-hour pass runs only while the volume is still below its trim threshold after
// the 5-day pass and the pnpm prune; above it, even short of the target, it deletes nothing.
func TestYoungPassWaitsForTheTrimThresholdNotTheTarget(t *testing.T) {
	home := realTempDir(t)
	build := filepath.Join(home, "go-build")
	t.Setenv("GOCACHE", build)
	t.Setenv("GOMODCACHE", filepath.Join(home, "modules"))
	t.Setenv("GOENV", "off")
	t.Setenv("GOLANGCI_LINT_CACHE", "")
	markCache(t, build, testGoReadme)
	old := writeCacheEntry(t, build, 0, goCacheAge+time.Hour)
	young := writeCacheEntry(t, build, 1, 24*time.Hour)
	const floor, threshold, target = 50, 100, 200
	free := int64(10)
	caches := Caches{
		WorktreeRoot: home, UserHome: home, Logger: slog.New(slog.DiscardHandler),
		measure: func(context.Context) (nodeproto.DiskSpace, int64) {
			measured := free
			return nodeproto.DiskSpace{FreeBytes: &measured, FloorBytes: floor, TrimBytes: threshold}, target
		},
	}
	pruned := false
	assert.False(t, caches.Trim(t.Context(), func(prune func()) {
		pruned = true
		free = (threshold + target) / 2
	}))
	require.True(t, pruned, "the volume still needed room after the 5-day pass")
	assert.NoFileExists(t, old)
	assert.FileExists(t, young, "above the trim threshold the 2-hour pass deletes nothing")
}

// Review round 2 of #1017: a directory is trimmed only when Go or golangci-lint wrote its README there. A
// GOLANGCI_LINT_CACHE, or GOCACHE, naming a directory of the same shape that no cache wrote, is left whole.
func TestLookAlikeOfACacheIsNeverTrimmed(t *testing.T) {
	home := realTempDir(t)
	build := filepath.Join(home, "go-build")
	lookAlike := filepath.Join(home, "project-data")
	markCache(t, build, testGoReadme)
	t.Setenv("GOCACHE", build)
	t.Setenv("GOMODCACHE", filepath.Join(home, "modules"))
	t.Setenv("GOENV", "off")
	t.Setenv("GOLANGCI_LINT_CACHE", lookAlike)
	cached := writeCacheEntry(t, build, 0, 24*time.Hour)
	kept := writeCacheEntry(t, lookAlike, 1, 24*time.Hour)
	wrongReadme := filepath.Join(home, ".cache", "golangci-lint")
	markCache(t, wrongReadme, "notes\n")
	alsoKept := writeCacheEntry(t, wrongReadme, 2, 24*time.Hour)
	caches := Caches{
		Policy: Policy{FloorBytes: 1<<63 - 1}, WorktreeRoot: home, UserHome: home, Logger: slog.New(slog.DiscardHandler),
	}
	caches.Trim(t.Context(), func(func()) {})
	assert.NoFileExists(t, cached, "Go's own cache is trimmed")
	assert.FileExists(t, kept, "a directory without a cache's README is not a cache")
	assert.FileExists(t, alsoKept, "nor one whose README is not a cache's")
}

// Review round 2 of #1017: Go refreshes an entry's mtime at most once an hour, so an entry used 1h30m ago may show
// an mtime of 2h15m; the 2-hour pass keeps it, as Go's own trim adds that hour (cache.go:426-429 at go1.27.4).
func TestYoungPassKeepsAnEntryUsedInTheLastTwoHoursWhoseMtimeIsOlder(t *testing.T) {
	home := realTempDir(t)
	build := filepath.Join(home, "go-build")
	markCache(t, build, testGoReadme)
	t.Setenv("GOCACHE", build)
	t.Setenv("GOMODCACHE", filepath.Join(home, "modules"))
	t.Setenv("GOENV", "off")
	t.Setenv("GOLANGCI_LINT_CACHE", "")
	usedRecently := writeCacheEntry(t, build, 0, 2*time.Hour+15*time.Minute)
	unused := writeCacheEntry(t, build, 1, 3*time.Hour+time.Minute)
	caches := Caches{
		Policy: Policy{FloorBytes: 1<<63 - 1}, WorktreeRoot: home, UserHome: home, Logger: slog.New(slog.DiscardHandler),
	}
	caches.Trim(t.Context(), func(func()) {})
	assert.FileExists(t, usedRecently)
	assert.NoFileExists(t, unused)
}

// Fix round 4 of #1017: in a cache-shaped directory, only what Go writes there is deleted: a file named <64
// lowercase hex>-a or -d in the subdirectory 00..ff of its first byte, or a -d directory holding one executable
// (cmd/go/internal/cache/cache.go:117-119,439-467,598-620 at go1.27.4; golangci-lint's copy,
// internal/go/cache/cache.go:121-123,446-472 at v2.15.0). Data placed there under any other name is left whole.
func TestOnlyGoCacheEntriesAreDeletedFromACacheShapedDirectory(t *testing.T) {
	t.Parallel()
	root := realTempDir(t)
	markCache(t, root, testGoReadme)
	hash := fmt.Sprintf("ab%062x", 7)
	entry := writeCacheEntry(t, root, 0, 30*24*time.Hour)
	exe := filepath.Join(root, "ab", hash+"-d")
	require.NoError(t, os.MkdirAll(exe, 0o700))
	data := []string{
		filepath.Join(root, "01", "notes.txt"),
		filepath.Join(root, "01", fmt.Sprintf("01%062x.json", 1)),
		filepath.Join(root, "01", fmt.Sprintf("01%062X-a", 0xABC)),
		filepath.Join(root, "CD", "CD"+strings.Repeat("E", 62)+"-a"),
		filepath.Join(root, "Ef", "ef"+strings.Repeat("0", 62)+"-d"),
		filepath.Join(root, "01", fmt.Sprintf("02%062x-a", 2)),
		filepath.Join(root, "01", fmt.Sprintf("01%062x-x", 3)),
		filepath.Join(root, "01", "sub", fmt.Sprintf("01%062x-a", 4)),
		filepath.Join(root, "ab", fmt.Sprintf("ab%062x-d", 5), "one"),
		filepath.Join(root, "ab", fmt.Sprintf("ab%062x-d", 5), "two"),
		filepath.Join(root, "ab", fmt.Sprintf("ab%062x-d", 6), "nested", "file"),
		filepath.Join(root, "fuzz", fmt.Sprintf("01%062x-a", 8)),
		filepath.Join(root, "trim.txt"),
	}
	old := time.Now().Add(-30 * 24 * time.Hour)
	for _, path := range append(slices.Clone(data), filepath.Join(exe, "tool")) {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte("data"), 0o600))
		require.NoError(t, os.Chtimes(path, old, old))
	}
	for _, dir := range []string{exe, filepath.Join(root, "ab", fmt.Sprintf("ab%062x-d", 5)),
		filepath.Join(root, "ab", fmt.Sprintf("ab%062x-d", 6))} {
		require.NoError(t, os.Chtimes(dir, old, old))
	}

	require.NoError(t, trimGo(t.Context(), root, root, goCacheReadme, time.Now().Add(-youngCacheAge),
		func() bool { return true }, nil))
	assert.NoFileExists(t, entry, "a Go cache entry is deleted")
	assert.NoDirExists(t, exe, "a Go executable entry is deleted")
	for _, path := range data {
		assert.FileExists(t, path, "data under a name Go does not write survives")
	}
}

// defaultCacheDir points the worker's user cache directory into home and returns where a tool keeps its cache there
// by default, as Go and golangci-lint find it (os.UserCacheDir: $XDG_CACHE_HOME, else ~/.cache, on Linux;
// ~/Library/Caches on macOS).
func defaultCacheDir(t *testing.T, home, tool string) string {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	dir, err := os.UserCacheDir()
	require.NoError(t, err)
	return filepath.Join(dir, tool)
}

// Fix round 5 of #1017: a cache is trimmed only at its tool's default place for the worker's user. A GOCACHE or
// GOLANGCI_LINT_CACHE naming any other directory, even one holding the cache's README and entries of its shape, is
// left whole.
func TestCacheOutsideItsDefaultPlaceIsNeverTrimmed(t *testing.T) {
	home := realTempDir(t)
	build := defaultCacheDir(t, home, "go-build")
	data := filepath.Join(home, "data")
	lintData := filepath.Join(home, "lint-data")
	markCache(t, build, testGoReadme)
	markCache(t, data, testGoReadme)
	markCache(t, lintData, testLintReadme)
	t.Setenv("GOCACHE", data)
	t.Setenv("GOMODCACHE", filepath.Join(home, "modules"))
	t.Setenv("GOENV", "off")
	t.Setenv("GOLANGCI_LINT_CACHE", lintData)
	trimmed := writeCacheEntry(t, build, 0, 30*24*time.Hour)
	kept := []string{writeCacheEntry(t, data, 1, 30*24*time.Hour), writeCacheEntry(t, lintData, 2, 30*24*time.Hour)}
	caches := Caches{
		Policy: Policy{FloorBytes: 1<<63 - 1}, WorktreeRoot: home, UserHome: home, Logger: slog.New(slog.DiscardHandler),
	}
	caches.Trim(t.Context(), func(func()) {})
	assert.NoFileExists(t, trimmed, "Go's cache at its default place is trimmed")
	for _, path := range kept {
		assert.FileExists(t, path, "a cache named by the environment elsewhere is left whole")
	}
}
