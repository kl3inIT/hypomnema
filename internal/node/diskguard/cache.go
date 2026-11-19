package diskguard

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/disk"

	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

const (
	goCacheAge   = 5*24*time.Hour + time.Hour
	goCacheBatch = 128
)

type Caches struct {
	Policy       Policy
	WorktreeRoot string
	NodeHome     string
	UserHome     string
	WorkerPath   func() string
	Logger       *slog.Logger
}

func (c Caches) Trim(ctx context.Context, whileIdle func(func())) {
	c.trim(ctx, whileIdle, sweepSystemTemp)
}

func (c Caches) trim(ctx context.Context, whileIdle func(func()),
	sweep func(context.Context, string, string, time.Time, *slog.Logger) error,
) {
	if ctx.Err() != nil {
		return
	}

	space, _ := c.Policy.Measure(ctx, c.WorktreeRoot)
	pressure := space.FreeBytes != nil && *space.FreeBytes < space.TrimBytes
	volume := ExistingDir(c.WorktreeRoot)
	needsRoom := func() bool {
		if !pressure || !RealDir(volume) || ctx.Err() != nil {
			return false
		}
		space, target := c.Policy.Measure(ctx, c.WorktreeRoot)
		return space.FreeBytes != nil && *space.FreeBytes < target &&
			SameVolume(volume, ExistingDir(c.WorktreeRoot))
	}

	locations, module, err := c.goCaches(ctx)
	if err != nil {
		c.Logger.WarnContext(ctx, "could not locate the Go build cache", slog.Any("error", err))
	} else {
		for _, root := range locations {
			c.trimGoCache(WithProtection(ctx, module), root, volume, needsRoom)
		}
	}
	guard := WithProtection(ctx, c.WorktreeRoot)
	if module != "" {
		guard = WithProtection(guard, module)
	}
	guard = WithProtection(guard, locations...)
	before := time.Now().Add(-time.Duration(c.Policy.TempMaxAgeHours) * time.Hour)
	if err := sweep(guard, c.NodeHome, c.UserHome, before, c.Logger); err != nil {
		c.Logger.WarnContext(ctx, "could not sweep shared temporary files", slog.Any("error", err))
	}
	if !pressure || !RealDir(c.UserHome) {
		return
	}
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
	if current, _ := c.Policy.Measure(ctx, c.WorktreeRoot); ctx.Err() == nil && !current.AllowsWork() {
		c.Logger.WarnContext(ctx, "the worktree volume still takes no new work", slog.String("event", "node.disk_blocked"))
	}
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

func (c Caches) trimGoCache(ctx context.Context, root, worktreeVolume string, needsRoom func() bool) {
	if !RealDir(root) {
		return
	}
	protected, err := deletionIdentities(ctx, root, c.NodeHome, "", false)
	if err == nil {
		limit := c.Policy.GoCacheMaxBytes
		if usage, usageErr := disk.UsageWithContext(ctx, root); usageErr == nil && usage.Total > 0 {
			limit = min(limit, share(int64(min(usage.Total, 1<<63-1)), 10))
		}
		err = trimGo(ctx, root, root, time.Now().Add(-goCacheAge), func() bool {
			return SameVolume(worktreeVolume, root) && needsRoom()
		}, limit, protected)
	}
	if err != nil {
		c.Logger.WarnContext(ctx, "could not trim the Go build cache", slog.String("path", root), slog.Any("error", err))
	}
}

type cacheEntry struct {
	bucket     string
	bucketInfo os.FileInfo
	name       string
	info       fs.FileInfo
	executable os.FileInfo
}

func (entry cacheEntry) size() int64 {
	if entry.executable != nil {
		return entry.executable.Size()
	}
	return entry.info.Size()
}

func trimGo(ctx context.Context, volumePath, root string, olderThan time.Time, needsRoom func() bool,
	limit int64, protected []os.FileInfo,
) error {
	if !RealDir(root) || !SameVolume(volumePath, root) {
		return nil
	}
	pressure := needsRoom()
	if limit == 0 && !pressure {
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
	buckets, err := fs.ReadDir(cache.FS(), ".")
	if err != nil {
		return err
	}

	var candidates []cacheEntry
	var size int64
	for _, bucket := range buckets {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if !bucket.IsDir() || !isHex(bucket.Name(), 2) || checkMountAt(cache, bucket.Name(), mount) != nil {
			continue
		}
		var err error
		var bucketSize int64
		candidates, bucketSize, err = scanGoBucket(cache, mount, bucket.Name(), protected, candidates)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		size += bucketSize
	}
	if !pressure && size <= limit {
		return nil
	}

	slices.SortFunc(candidates, func(first, second cacheEntry) int {
		return first.info.ModTime().Compare(second.info.ModTime())
	})

	var removalErr error
	for start := 0; start < len(candidates); start += goCacheBatch {
		pressure = pressure && needsRoom()
		for _, entry := range candidates[start:min(start+goCacheBatch, len(candidates))] {
			if ctx.Err() != nil {
				return ctx.Err()
			}

			if (limit == 0 || size <= limit) && (!pressure || !entry.info.ModTime().Before(olderThan)) {
				return removalErr
			}
			removed, err := removeGoEntry(ctx, cache, mount, entry, protected)
			if removed || os.IsNotExist(err) {
				size -= entry.size()
			} else if err != nil && removalErr == nil {
				removalErr = err
			}
		}
	}

	return removalErr
}

func scanGoBucket(cache *os.Root, mount mountID, bucket string, protected []os.FileInfo, candidates []cacheEntry) (
	[]cacheEntry, int64, error,
) {
	dir, err := openChild(cache, bucket)
	if err != nil {
		return candidates, 0, err
	}
	defer dir.Close() //nolint:errcheck // directory handle
	info, err := dir.Stat(".")
	if err != nil || sameIdentity(info, protected) || checkMountAt(dir, ".", mount) != nil {
		return candidates, 0, err
	}
	files, err := fs.ReadDir(dir.FS(), ".")
	if err != nil {
		return candidates, 0, err
	}
	var size int64
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
		size += candidate.size()
		candidates = append(candidates, candidate)
	}
	return candidates, size, nil
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
) (bool, error) {
	root, err := openChild(cache, entry.bucket)
	if err != nil {
		return false, err
	}
	defer root.Close() //nolint:errcheck // directory handle
	if info, err := root.Stat("."); err != nil || !os.SameFile(info, entry.bucketInfo) {
		return false, err
	}
	if err := checkMountAt(root, ".", mount); err != nil {
		return false, err
	}
	info, err := root.Lstat(entry.name)
	if err != nil || !unchangedCacheEntry(info, entry.info) {
		return false, err
	}
	walk := removalWalk{mount: mount, protected: protected, guard: func(current os.FileInfo) error {
		if unchangedCacheEntry(current, entry.info) ||
			(entry.executable != nil && unchangedCacheEntry(current, entry.executable)) {
			return nil
		}
		return errors.New("cache entry changed")
	}}
	err = walk.visit(ctx, root, entry.name, info)
	return err == nil, err
}

func unchangedCacheEntry(current, expected os.FileInfo) bool {
	return os.SameFile(current, expected) && current.Mode().Type() == expected.Mode().Type() &&
		current.ModTime().Equal(expected.ModTime()) && current.Size() == expected.Size()
}

func isHex(name string, size int) bool {
	if len(name) != size {
		return false
	}

	_, err := hex.DecodeString(name)
	return err == nil
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
