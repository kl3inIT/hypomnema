package diskguard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

const (
	// mtimeInterval is how stale an entry's mtime may be: Go refreshes it on use at most once an hour
	// (cmd/go/internal/cache/cache.go:335-370 at go1.27.4; golangci-lint's copy, internal/go/cache/cache.go:355).
	mtimeInterval = time.Hour
	// goCacheAge is Go's own cut: entries unused for 5 days, plus mtimeInterval (cmd/go/internal/cache/cache.go:426-429;
	// golangci-lint's copy, internal/go/cache/cache.go:434).
	goCacheAge = 5*24*time.Hour + mtimeInterval
	// youngCacheAge is the second pass's floor, taken only while the volume is still below its trim threshold
	// (issue #1017): an entry used in the last 2 hours is kept, with the same mtimeInterval of slack Go's trim adds,
	// so a build in progress keeps what it uses.
	youngCacheAge = 2*time.Hour + mtimeInterval
	goCacheBatch  = 128
)

// The first line of the README Go and golangci-lint write in a cache they use (cmd/go/internal/cache/default.go:28,
// 47-50 at go1.27.4; golangci-lint's internal/go/cache/default.go:26,45-48 at v2.15.0). A directory is trimmed only
// when its README starts with its kind's: one that merely has the cache's shape, named by a mistaken GOCACHE or
// GOLANGCI_LINT_CACHE, is left whole (issue #1017).
const (
	goCacheReadme   = "This directory holds cached build artifacts from the Go build system.\n"
	lintCacheReadme = "This directory holds cached build artifacts from golangci-lint.\n"
)

var errNotACache = errors.New("no README of Go's or golangci-lint's cache")

// unconfirmedCaches are the directories already logged as not trimmed for want of a cache's README, so each is
// said once.
var unconfirmedCaches sync.Map

// cacheRoot is a cache of Go's layout and the README that confirms it.
type cacheRoot struct {
	path   string
	readme string
}

type Caches struct {
	Policy       Policy
	WorktreeRoot string
	NodeHome     string
	UserHome     string
	WorkerPath   func() string
	Logger       *slog.Logger

	// measure stands for Policy.Measure of the worktree root when set: a test's free space, which a real volume
	// cannot be made to show.
	measure func(ctx context.Context) (nodeproto.DiskSpace, int64)
}

func (c Caches) space(ctx context.Context) (nodeproto.DiskSpace, int64) {
	if c.measure != nil {
		return c.measure(ctx)
	}
	return c.Policy.Measure(ctx, c.WorktreeRoot)
}

func (c Caches) worktreeVolume() string {
	path, _ := filepath.EvalSymlinks(ExistingDir(c.WorktreeRoot))
	return path
}

// Trim frees room on the worktree volume when it is below its trim threshold, from the caches it manages only: Go's
// build caches and golangci-lint's, oldest entry first, the entries unused for goCacheAge; then the pnpm store's
// unreferenced packages while the node is idle; then, only when the volume is still below its trim threshold, the
// same caches again down to youngCacheAge, until it reaches the target (issue #1017). It reports whether the volume is still below its floor after all of it.
func (c Caches) Trim(ctx context.Context, whileIdle func(func())) (blocked bool) {
	if ctx.Err() != nil {
		return false
	}

	space, _ := c.space(ctx)
	if space.FreeBytes == nil || *space.FreeBytes >= space.TrimBytes {
		return false
	}
	volume := c.worktreeVolume()
	needsRoom := func() bool {
		if !RealDir(volume) || ctx.Err() != nil {
			return false
		}
		space, target := c.space(ctx)
		return space.FreeBytes != nil && *space.FreeBytes < target &&
			SameVolume(volume, c.worktreeVolume())
	}

	// Without Go's own answer the module cache, which is never trimmed, is not known for sure, so neither cache of
	// Go's layout is trimmed.
	locations, module, err := c.goCaches(ctx)
	if err != nil {
		c.Logger.WarnContext(ctx, "could not locate the Go build cache", slog.Any("error", err))
		locations = nil
	}
	var roots []cacheRoot
	if err == nil {
		roots = c.defaultCaches(ctx, locations)
	}
	guarded := WithProtection(ctx, module)
	trim := func(age time.Duration) {
		for _, root := range roots {
			c.trimGoCache(guarded, root, volume, age, needsRoom)
		}
	}
	trim(goCacheAge)
	if RealDir(c.UserHome) {
		c.Logger.WarnContext(ctx, "worktree volume is below the disk trim threshold",
			slog.String("event", "node.disk_pressure"), slog.Int64("free_bytes", *space.FreeBytes),
			slog.Int64("floor_bytes", space.FloorBytes), slog.Int64("trim_bytes", space.TrimBytes))

		if needsRoom() {
			whileIdle(func() {
				if needsRoom() {
					if err := c.prunePnpm(ctx, volume); err != nil {
						c.Logger.WarnContext(ctx, "could not prune the pnpm store", slog.Any("error", err))
					}
				}
			})
		}
	}
	// The 2-hour pass is the low-disk trigger's own (issue #1017): it starts only while the volume is still below
	// its trim threshold, and then stops at the target as the first pass does.
	if after, _ := c.space(ctx); after.FreeBytes != nil && *after.FreeBytes < after.TrimBytes {
		trim(youngCacheAge)
	}

	current, _ := c.space(ctx)
	if ctx.Err() != nil || current.AllowsWork() {
		return false
	}
	c.Logger.WarnContext(ctx, "the worktree volume still takes no new work", slog.String("event", "node.disk_blocked"))
	return true
}

// defaultCaches are the caches the guard trims: Go's and golangci-lint's, each only at the place its tool uses for
// the worker's user when nothing names another, os.UserCacheDir()/go-build (cmd/go/internal/cache/default.go:80-88
// at go1.27.4) and os.UserCacheDir()/golangci-lint (golangci-lint's internal/go/cache/default.go:78-86 at v2.15.0).
// A cache named elsewhere, by GOCACHE, Go's env file or GOLANGCI_LINT_CACHE, may be a directory of the owner's that
// a mistaken setting points at, so it is left whole and said once (issue #1017).
func (c Caches) defaultCaches(ctx context.Context, named []string) []cacheRoot {
	base, err := os.UserCacheDir()
	if err != nil || !filepath.IsAbs(base) {
		c.Logger.WarnContext(ctx, "could not locate the worker's cache directory", slog.Any("error", err))
		return nil
	}
	roots := []cacheRoot{
		{path: filepath.Join(base, "go-build"), readme: goCacheReadme},
		{path: filepath.Join(base, "golangci-lint"), readme: lintCacheReadme},
	}
	if lint := os.Getenv("GOLANGCI_LINT_CACHE"); filepath.IsAbs(lint) {
		named = append(named, filepath.Clean(lint))
	}
	for _, path := range named {
		if path == roots[0].path || path == roots[1].path {
			continue
		}
		if _, said := unconfirmedCaches.LoadOrStore(path, true); !said {
			c.Logger.WarnContext(ctx, "not trimming a cache outside its tool's default place",
				slog.String("event", "node.cache_not_default"), slog.String("path", path))
		}
	}
	return roots
}

type goCacheConfig struct {
	GOCACHE    string
	GOMODCACHE string
}

func (c Caches) goCaches(ctx context.Context) ([]string, string, error) {
	if c.UserHome == "" {
		return nil, "", errors.New("the worker's home directory is unknown")
	}
	paths := []string{filepath.Join(c.UserHome, ".cache", "go-build")}
	if configured := os.Getenv("GOCACHE"); filepath.IsAbs(configured) {
		paths = append(paths, filepath.Clean(configured))
	}
	goBin, err := exec.LookPath(filepath.Join(c.UserHome, ".local", "bin", "go"))
	if err != nil {
		goBin, err = exec.LookPath("go")
		if err != nil {
			return c.fallbackGoCaches(paths, nil)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, goBin, "env", "-json", "GOCACHE", "GOMODCACHE") //nolint:gosec // node's installed Go
	cmd.Dir = c.UserHome
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local")
	output, err := cmd.Output()
	if err != nil {
		return c.fallbackGoCaches(paths, err)
	}
	var config goCacheConfig
	if err := json.Unmarshal(output, &config); err != nil {
		return c.fallbackGoCaches(paths, err)
	}
	if filepath.IsAbs(config.GOCACHE) && !slices.Contains(paths, config.GOCACHE) {
		paths = append(paths, config.GOCACHE)
	}
	return slices.Compact(paths), config.GOMODCACHE, nil
}

func (c Caches) fallbackGoCaches(paths []string, cause error) ([]string, string, error) {
	if dir, err := os.UserCacheDir(); err == nil && filepath.IsAbs(dir) {
		paths = append(paths, filepath.Join(dir, "go-build"))
	}
	settings := make(map[string]string)
	file := os.Getenv("GOENV")
	if file == "" {
		if dir, err := os.UserConfigDir(); err == nil {
			file = filepath.Join(dir, "go", "env")
		}
	}
	if file != "" && file != "off" {
		if !filepath.IsAbs(file) {
			file = filepath.Join(c.UserHome, file)
		}
		info, err := os.Stat(file) //nolint:gosec // the worker's Go configuration
		if err == nil && info.Mode().IsRegular() {
			data, readErr := os.ReadFile(file) //nolint:gosec // the worker's Go configuration
			if readErr == nil {
				for line := range strings.SplitSeq(string(data), "\n") {
					if key, value, ok := strings.Cut(line, "="); ok {
						settings[key] = strings.TrimRight(value, "\r")
					}
				}
			}
		}
	}
	if build := settings["GOCACHE"]; filepath.IsAbs(build) {
		paths = append(paths, filepath.Clean(build))
	}
	if module := os.Getenv("GOMODCACHE"); filepath.IsAbs(module) {
		return slices.Compact(paths), filepath.Clean(module), cause
	}
	if module := settings["GOMODCACHE"]; filepath.IsAbs(module) {
		return slices.Compact(paths), filepath.Clean(module), cause
	}
	gopath := os.Getenv("GOPATH")
	if gopath == "" {
		gopath = settings["GOPATH"]
	}
	if dirs := filepath.SplitList(gopath); len(dirs) > 0 && filepath.IsAbs(dirs[0]) {
		return slices.Compact(paths), filepath.Join(dirs[0], "pkg", "mod"), cause
	}
	return slices.Compact(paths), filepath.Join(c.UserHome, "go", "pkg", "mod"), cause
}

func (c Caches) trimGoCache(ctx context.Context, root cacheRoot, worktreeVolume string, age time.Duration,
	needsRoom func() bool,
) {
	if !RealDir(root.path) || !SameVolume(worktreeVolume, root.path) {
		return
	}
	protected, err := deletionIdentities(ctx, root.path, c.NodeHome, "", false)
	if err == nil {
		err = trimGo(ctx, worktreeVolume, root.path, root.readme, time.Now().Add(-age), needsRoom, protected)
	}
	if errors.Is(err, errNotACache) {
		if _, said := unconfirmedCaches.LoadOrStore(root.path, true); !said {
			c.Logger.WarnContext(ctx, "not trimming a directory that is not a confirmed cache",
				slog.String("event", "node.cache_unconfirmed"), slog.String("path", root.path), slog.Any("error", err))
		}
		return
	}
	if err != nil {
		c.Logger.WarnContext(ctx, "could not trim a Go build cache", slog.String("path", root.path), slog.Any("error", err))
	}
}

// confirmedCache checks, through the pinned handle of the cache's directory, that Go or golangci-lint wrote its
// README there: a regular file whose text starts with readme.
func confirmedCache(cache *os.Root, readme string) error {
	info, err := cache.Lstat("README")
	if err != nil || !info.Mode().IsRegular() {
		return errNotACache
	}
	file, err := cache.Open("README")
	if err != nil {
		return errNotACache
	}
	defer file.Close() //nolint:errcheck // read only
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return errNotACache
	}
	head := make([]byte, len(readme))
	if _, err := io.ReadFull(file, head); err != nil || string(head) != readme {
		return errNotACache
	}
	return nil
}

type cacheEntry struct {
	bucket     string
	bucketInfo os.FileInfo
	name       string
	info       fs.FileInfo
	executable os.FileInfo
}

func trimGo(ctx context.Context, volumePath, root, readme string, olderThan time.Time, needsRoom func() bool,
	protected []os.FileInfo,
) error {
	if !RealDir(root) || !SameVolume(volumePath, root) || !needsRoom() {
		return nil
	}

	cache, err := OpenRealDir(root)
	if err != nil {
		return err
	}
	defer cache.Close() //nolint:errcheck // directory handle
	mount, err := volumeAt(cache, ".")
	if err != nil {
		return err
	}
	if measured, err := volume(volumePath); err != nil || measured != mount {
		return err
	}
	if err := confirmedCache(cache, readme); err != nil {
		return err
	}
	buckets, err := fs.ReadDir(cache.FS(), ".")
	if err != nil {
		return err
	}

	var candidates []cacheEntry
	for _, bucket := range buckets {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if !bucket.IsDir() || !isHex(bucket.Name(), 2) || checkMountAt(cache, bucket.Name(), mount) != nil {
			continue
		}
		var err error
		candidates, err = scanGoBucket(cache, mount, bucket.Name(), protected, candidates)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
	}

	slices.SortFunc(candidates, func(first, second cacheEntry) int {
		return first.info.ModTime().Compare(second.info.ModTime())
	})

	var removalErr error
	for start := 0; start < len(candidates); start += goCacheBatch {
		if !needsRoom() {
			break
		}
		for _, entry := range candidates[start:min(start+goCacheBatch, len(candidates))] {
			if ctx.Err() != nil {
				return ctx.Err()
			}

			if !entry.info.ModTime().Before(olderThan) {
				return removalErr
			}
			err := removeGoEntry(ctx, cache, mount, entry, protected)
			if err != nil && !os.IsNotExist(err) && removalErr == nil {
				removalErr = err
			}
		}
	}

	return removalErr
}

// scanGoBucket lists what Go itself writes in one subdirectory 00..ff of a cache and trims (cmd/go/internal/cache/
// cache.go:117-119,439-467,598-620 at go1.27.4; golangci-lint's copy, internal/go/cache/cache.go:121-123,446-472 at
// v2.15.0): a regular file named <64 lowercase hex>-a or -d whose hash starts with the subdirectory's name, or a -d
// directory holding the one executable Go put there. Nothing else in a cache is ever a candidate.
func scanGoBucket(cache *os.Root, mount mountID, bucket string, protected []os.FileInfo, candidates []cacheEntry) (
	[]cacheEntry, error,
) {
	dir, err := openChild(cache, bucket)
	if err != nil {
		return candidates, err
	}
	defer dir.Close() //nolint:errcheck // directory handle
	info, err := dir.Stat(".")
	if err != nil || sameIdentity(info, protected) || checkMountAt(dir, ".", mount) != nil {
		return candidates, err
	}
	files, err := fs.ReadDir(dir.FS(), ".")
	if err != nil {
		return candidates, err
	}
	for _, file := range files {
		name := file.Name()
		hash, suffix, ok := strings.Cut(name, "-")
		if !ok || (suffix != "a" && suffix != "d") ||
			!strings.HasPrefix(hash, bucket) || !isHex(hash, 64) {
			continue
		}
		entry, err := file.Info()
		if err != nil {
			continue
		}
		candidate := cacheEntry{bucket: bucket, bucketInfo: info, name: name, info: entry}
		if !entry.Mode().IsRegular() {
			if suffix != "d" || !entry.IsDir() || sameIdentity(entry, protected) {
				continue
			}
			candidate.executable = goExecutable(dir, mount, name, entry)
			if candidate.executable == nil {
				continue
			}
		}
		candidates = append(candidates, candidate)
	}
	return candidates, nil
}

func goExecutable(bucket *os.Root, mount mountID, name string, expected os.FileInfo) os.FileInfo {
	if checkMountAt(bucket, name, mount) != nil {
		return nil
	}
	dir, err := openChild(bucket, name)
	if err != nil {
		return nil
	}
	defer dir.Close() //nolint:errcheck // directory handle
	if info, err := dir.Stat("."); err != nil || !os.SameFile(info, expected) || checkMountAt(dir, ".", mount) != nil {
		return nil
	}
	files, err := fs.ReadDir(dir.FS(), ".")
	if err != nil || len(files) != 1 || checkMountAt(dir, files[0].Name(), mount) != nil {
		return nil
	}
	file, err := files[0].Info()
	if err != nil || !file.Mode().IsRegular() {
		return nil
	}
	return file
}

func removeGoEntry(ctx context.Context, cache *os.Root, mount mountID, entry cacheEntry,
	protected []os.FileInfo,
) error {
	root, err := openChild(cache, entry.bucket)
	if err != nil {
		return err
	}
	defer root.Close() //nolint:errcheck // directory handle
	if info, err := root.Stat("."); err != nil || !os.SameFile(info, entry.bucketInfo) {
		return err
	}
	if err := checkMountAt(root, ".", mount); err != nil {
		return err
	}
	info, err := root.Lstat(entry.name)
	if err != nil || !unchangedCacheEntry(info, entry.info) {
		return err
	}
	walk := removalWalk{mount: mount, protected: protected, guard: func(current os.FileInfo) error {
		if unchangedCacheEntry(current, entry.info) ||
			(entry.executable != nil && unchangedCacheEntry(current, entry.executable)) {
			return nil
		}
		return errors.New("cache entry changed")
	}}
	return walk.visit(ctx, root, entry.name, info)
}

func unchangedCacheEntry(current, expected os.FileInfo) bool {
	return os.SameFile(current, expected) && current.Mode().Type() == expected.Mode().Type() &&
		current.ModTime().Equal(expected.ModTime()) && current.Size() == expected.Size()
}

// isHex reports whether name is size lowercase hex digits, as Go and golangci-lint name a cache's subdirectories
// and entries (fmt "%02x" and "%x": cmd/go/internal/cache/cache.go:117-119 at go1.27.4; golangci-lint's
// internal/go/cache/cache.go:121-123 at v2.15.0). A name in upper case is none they write, so it is not deleted.
func isHex(name string, size int) bool {
	if len(name) != size {
		return false
	}

	for _, r := range name {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func (c Caches) prunePnpm(ctx context.Context, worktreeVolume string) error {
	exe, err := exec.LookPath(filepath.Join(c.UserHome, ".local", "bin", "pnpm"))
	if err != nil {
		exe, err = exec.LookPath("pnpm")
	}
	if err != nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	store := pnpmStore(c.UserHome)
	if !RealDir(store) || !SameVolume(worktreeVolume, store) {
		return nil
	}
	if err := Protect(ctx, store, c.NodeHome, ""); err != nil {
		return err
	}

	if err := CheckCacheTree(ctx, store); err != nil {
		return err
	}
	if !ScopeAvailable() {
		return errors.New("pnpm pruning needs filesystem confinement")
	}
	root, err := OpenRealDir(store)
	if err != nil {
		return err
	}
	defer root.Close() //nolint:errcheck // directory handle
	mount, err := volumeAt(root, ".")
	if err != nil {
		return err
	}
	if work, err := volume(worktreeVolume); err != nil || mount != work {
		return err
	}
	handle, err := root.Open(".")
	if err != nil {
		return err
	}
	defer handle.Close() //nolint:errcheck // directory handle
	cacheName := "." + platform.NewID("prune")
	if err := root.Mkdir(cacheName, 0o700); err != nil {
		return err
	}
	cacheInfo, err := root.Lstat(cacheName)
	if err != nil {
		return err
	}
	cache := filepath.Join(store, cacheName)
	defer func() {
		current, err := os.Lstat(store)
		opened, statErr := handle.Stat()
		if err == nil && statErr == nil && os.SameFile(current, opened) {
			if retained, err := deletionProtection(ctx, cache, c.NodeHome, ""); err == nil {
				_ = removeAt(ctx, root, cacheName, mount, cacheInfo, retained)
			}
		}
	}()
	if !RealDir(cache) || !SameVolume(worktreeVolume, cache) {
		return errors.New("the isolated pnpm cache is not on the worktree volume")
	}
	probe, err := c.pnpm(ctx, handle, exe, store, cache, "path")
	if err != nil {
		return err
	}
	raw, err := probe.Output()
	if err != nil {
		return fmt.Errorf("pnpm store path: %w", err)
	}

	versioned := strings.TrimSpace(string(raw))
	if filepath.Dir(versioned) != store || !RealDir(versioned) || !SameVolume(worktreeVolume, versioned) {
		return nil
	}

	if err := CheckCacheTree(ctx, store); err != nil {
		return err
	}
	if err := Protect(ctx, store, c.NodeHome, ""); err != nil {
		return err
	}
	current, err := os.Lstat(store)
	opened, statErr := handle.Stat()
	if err != nil || statErr != nil || !os.SameFile(current, opened) {
		return errors.New("the pnpm store changed")
	}

	prune, err := c.pnpm(ctx, handle, exe, store, cache, "prune")
	if err != nil {
		return err
	}
	if out, err := prune.CombinedOutput(); err != nil {
		return fmt.Errorf("pnpm store prune: %w: %s", err, out)
	}

	return nil
}

func pnpmStore(home string) string {
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "pnpm", "store")
	}
	return filepath.Join(home, ".local", "share", "pnpm", "store")
}

func (c Caches) pnpm(ctx context.Context, handle *os.File, exe, store, cache, action string) (*exec.Cmd, error) {
	args := []string{"--store-dir", store, "store", action}
	cmd, err := ScopedCommand(ctx, []Scope{{Dir: handle}}, exe, args...)
	if err != nil {
		return nil, err
	}
	cmd.Dir = c.UserHome
	corepack := os.Getenv("COREPACK_HOME")
	if corepack == "" {
		corepack = filepath.Join(c.UserHome, ".cache", "node", "corepack")
	}
	cmd.Env = []string{
		"HOME=" + c.UserHome, "PATH=" + c.WorkerPath(), "COREPACK_HOME=" + corepack,
		"COREPACK_ENABLE_NETWORK=0", "CI=true", "NODE_DISABLE_COMPILE_CACHE=1",
		"XDG_CACHE_HOME=" + cache, "XDG_STATE_HOME=" + cache, "XDG_RUNTIME_DIR=" + cache,
		"TMPDIR=" + cache, "TEMP=" + cache, "TMP=" + cache,
	}
	return cmd, nil
}
