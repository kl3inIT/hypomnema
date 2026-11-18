package diskguard

import (
	"context"
	"encoding/hex"
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
	if ctx.Err() != nil {
		return
	}

	space, _ := c.Policy.Measure(ctx, c.WorktreeRoot)
	if space.FreeBytes == nil || *space.FreeBytes >= space.TrimBytes {
		return
	}

	c.Logger.WarnContext(ctx, "worktree volume is below the disk trim threshold",
		slog.String("event", "node.disk_pressure"), slog.Int64("free_bytes", *space.FreeBytes),
		slog.Int64("floor_bytes", space.FloorBytes), slog.Int64("trim_bytes", space.TrimBytes))
	volume := ExistingDir(c.WorktreeRoot)
	if !RealDir(volume) || !RealDir(c.UserHome) {
		return
	}

	needsRoom := func() bool {
		space, target := c.Policy.Measure(ctx, c.WorktreeRoot)
		return ctx.Err() == nil && space.FreeBytes != nil && *space.FreeBytes < target &&
			SameVolume(volume, ExistingDir(c.WorktreeRoot))
	}

	goCache := filepath.Join(c.UserHome, ".cache", "go-build")
	if RealDir(goCache) && SameVolume(volume, goCache) {
		err := Protect(ctx, goCache, c.NodeHome, "")
		if err == nil {
			err = trimGo(ctx, volume, goCache, time.Now().Add(-goCacheAge), needsRoom)
		}
		if err != nil {
			c.Logger.WarnContext(ctx, "could not trim the Go build cache", slog.Any("error", err))
		}
	}

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

type cacheEntry struct {
	path string
	info fs.FileInfo
}

func trimGo(ctx context.Context, volume, root string, olderThan time.Time, needsRoom func() bool) error {
	if !RealDir(root) || !SameVolume(volume, root) || !needsRoom() {
		return nil
	}

	buckets, err := os.ReadDir(root)
	if err != nil {
		return err
	}

	var candidates []cacheEntry
	for _, bucket := range buckets {
		dir := filepath.Join(root, bucket.Name())
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if !bucket.IsDir() || !isHex(bucket.Name(), 2) || !RealDir(dir) || !SameVolume(volume, dir) {
			continue
		}

		files, err := os.ReadDir(dir)
		if err != nil {
			return err
		}

		for _, file := range files {
			name := file.Name()
			hash, suffix, ok := strings.Cut(name, "-")
			if !file.Type().IsRegular() || !ok || (suffix != "a" && suffix != "d") ||
				!strings.HasPrefix(hash, bucket.Name()) || !isHex(hash, 64) {
				continue
			}

			if info, err := file.Info(); err == nil && info.Mode().IsRegular() && info.ModTime().Before(olderThan) {
				candidates = append(candidates, cacheEntry{path: filepath.Join(dir, name), info: info})
			}
		}
	}

	slices.SortFunc(candidates, func(first, second cacheEntry) int {
		return first.info.ModTime().Compare(second.info.ModTime())
	})

	var removalErr error
	for start := 0; start < len(candidates) && needsRoom(); start += goCacheBatch {
		for _, entry := range candidates[start:min(start+goCacheBatch, len(candidates))] {
			if ctx.Err() != nil {
				return ctx.Err()
			}

			if err := removeGoEntry(ctx, volume, entry, olderThan); err != nil && !os.IsNotExist(err) && removalErr == nil {
				removalErr = err
			}
		}
	}

	return removalErr
}

func removeGoEntry(ctx context.Context, worktreeVolume string, entry cacheEntry, olderThan time.Time) error {
	root, err := OpenRealDir(filepath.Dir(entry.path))
	if err != nil {
		return err
	}
	defer root.Close() //nolint:errcheck // directory handle
	name := filepath.Base(entry.path)
	info, err := root.Lstat(name)
	if err != nil || !os.SameFile(info, entry.info) || !info.ModTime().Before(olderThan) {
		return err
	}
	mount, err := volumeAt(root, ".")
	if err != nil {
		return err
	}
	work, err := volume(worktreeVolume)
	if err != nil || mount != work {
		return err
	}
	return removeAt(ctx, root, name, mount, info)
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
		if err == nil && statErr == nil && os.SameFile(current, opened) && Protect(ctx, cache, c.NodeHome, "") == nil {
			_ = removeAt(ctx, root, cacheName, mount, cacheInfo)
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
