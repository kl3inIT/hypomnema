package diskguard

import (
	"context"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	goCacheAge   = 5*24*time.Hour + time.Hour
	goCacheBatch = 128
)

type Caches struct {
	Policy       Policy
	WorktreeRoot string
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
	if err := trimGo(ctx, volume, goCache, time.Now().Add(-goCacheAge), needsRoom); err != nil {
		c.Logger.WarnContext(ctx, "could not trim the Go build cache", slog.Any("error", err))
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

	for start := 0; start < len(candidates) && needsRoom(); start += goCacheBatch {
		for _, entry := range candidates[start:min(start+goCacheBatch, len(candidates))] {
			if ctx.Err() != nil {
				return ctx.Err()
			}

			info, err := os.Lstat(entry.path)
			if err != nil || !os.SameFile(info, entry.info) || !info.ModTime().Before(olderThan) ||
				!RealDir(filepath.Dir(entry.path)) || !SameVolume(volume, entry.path) {
				continue
			}

			if err := os.Remove(entry.path); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}

	return nil
}

func isHex(name string, size int) bool {
	if len(name) != size {
		return false
	}

	_, err := hex.DecodeString(name)
	return err == nil
}

func (c Caches) prunePnpm(ctx context.Context, volume string) error {
	exe, err := exec.LookPath(filepath.Join(c.UserHome, ".local", "bin", "pnpm"))
	if err != nil {
		exe, err = exec.LookPath("pnpm")
	}
	if err != nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	store := filepath.Join(c.UserHome, ".local", "share", "pnpm", "store")
	if !RealDir(store) || !SameVolume(volume, store) {
		return nil
	}

	probe := c.pnpm(ctx, exe, store, "path")
	raw, err := probe.Output()
	if err != nil {
		return fmt.Errorf("pnpm store path: %w", err)
	}

	versioned := strings.TrimSpace(string(raw))
	if filepath.Dir(versioned) != store || !RealDir(versioned) || !SameVolume(volume, versioned) {
		return nil
	}

	if err := CheckTree(ctx, versioned); err != nil {
		return err
	}

	if err := c.pnpm(ctx, exe, store, "prune").Run(); err != nil {
		return fmt.Errorf("pnpm store prune: %w", err)
	}

	return nil
}

func (c Caches) pnpm(ctx context.Context, exe, store, action string) *exec.Cmd {
	args := []string{"--store-dir", store, "store", action}
	cmd := exec.CommandContext(ctx, exe, args...) //nolint:gosec // the node's pnpm and its own store
	cmd.Dir = c.UserHome
	cmd.Env = []string{"HOME=" + c.UserHome, "PATH=" + c.WorkerPath(), "COREPACK_ENABLE_NETWORK=0", "CI=true"}
	return cmd
}
