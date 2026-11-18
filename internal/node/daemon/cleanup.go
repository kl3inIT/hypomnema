package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/node/diskguard"
	"github.com/kl3inIT/hypomnema/internal/node/statedb"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

const cleanupEvery = 5 * time.Minute

type Maintenance struct {
	ShimRoot      string
	Stopped       func(id string, missingOK bool) bool
	CleanTemp     func(ctx context.Context, id string) error
	CleanWorktree func(ctx context.Context, path string, hasRepo bool, stopped func() bool) error
	TrimCaches    func(ctx context.Context, whileIdle func(func()))
}

func (d *Daemon) maintain(ctx context.Context, out chan<- nodeproto.Message, acks <-chan nodeproto.CleanupAck) {
	ticker := time.NewTicker(cleanupEvery)
	defer ticker.Stop()

	for ctx.Err() == nil {
		d.cleanWorktrees(ctx, out, acks)
		d.cleanTemps(ctx)
		d.cfg.Maintenance.TrimCaches(ctx, func(prune func()) { d.whileIdle(ctx, prune) })

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (d *Daemon) cleanWorktrees(ctx context.Context, out chan<- nodeproto.Message, acks <-chan nodeproto.CleanupAck) {
	root := filepath.Clean(d.cfg.WorktreeRoot)
	if !diskguard.RealDir(root) {
		return
	}

	var batch []nodeproto.WorktreePath
	for _, run := range listedDirs(root, "run") {
		runDir := filepath.Join(root, run)
		if !diskguard.SameVolume(root, runDir) {
			continue
		}

		for _, task := range listedDirs(runDir, "tsk") {
			batch = append(batch, nodeproto.WorktreePath{RunID: run, TaskID: task, Path: filepath.Join(runDir, task)})
			if len(batch) == nodeproto.CleanupBatchSize {
				if err := d.cleanBatch(ctx, out, acks, batch); err != nil {
					d.cleanupWarning(ctx, root, err)
					return
				}
				batch = batch[:0]
			}
		}
	}

	if len(batch) > 0 {
		if err := d.cleanBatch(ctx, out, acks, batch); err != nil {
			d.cleanupWarning(ctx, root, err)
		}
	}
}

func (d *Daemon) cleanBatch(ctx context.Context, out chan<- nodeproto.Message, acks <-chan nodeproto.CleanupAck,
	paths []nodeproto.WorktreePath,
) error {
	check := nodeproto.CleanupCheck{ID: d.newID("cln"), Paths: paths}
	msg, err := nodeproto.Encode(nodeproto.TypeCleanupCheck, check)
	if err != nil {
		return err
	}

	ack, err := awaitCleanup(ctx, out, acks, msg, check.ID)
	if err != nil {
		return err
	}

	for _, tree := range ack.Worktrees {
		if ctx.Err() != nil || len(tree.Accepted) == 0 || !slices.Contains(paths, tree.WorktreePath) {
			continue
		}

		stopped := func() bool { return d.worktreeStopped(ctx, tree) }
		if !stopped() {
			continue
		}
		if err := d.cfg.Maintenance.CleanWorktree(ctx, tree.Path, tree.HasRepo, stopped); err != nil {
			d.cleanupWarning(ctx, tree.Path, err)
			continue
		}
		runDir := filepath.Dir(tree.Path)
		if ctx.Err() == nil && diskguard.RealDir(runDir) && diskguard.SameVolume(filepath.Dir(runDir), runDir) {
			_ = os.Remove(runDir)
		}
	}

	return nil
}

func awaitCleanup(ctx context.Context, out chan<- nodeproto.Message, acks <-chan nodeproto.CleanupAck,
	msg nodeproto.Message, id string,
) (nodeproto.CleanupAck, error) {
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()

	select {
	case out <- msg:
	case <-ctx.Done():
		return nodeproto.CleanupAck{}, ctx.Err()
	}

	for {
		select {
		case ack := <-acks:
			if ack.ID == id {
				return ack, nil
			}
		case <-ctx.Done():
			return nodeproto.CleanupAck{}, ctx.Err()
		}
	}
}

func (d *Daemon) worktreeStopped(ctx context.Context, tree nodeproto.CleanupWorktree) bool {
	if ctx.Err() != nil {
		return false
	}
	held, err := d.db.List(ctx)
	if err != nil {
		return false
	}
	for _, h := range held {
		if h.State == statedb.StateEnded {
			continue
		}
		worktree := filepath.Clean(h.Offer.Worktree)
		if slices.Contains(tree.Dispatches, h.ID) || overlap(worktree, tree.Path) {
			return false
		}
		if resolved, err := filepath.EvalSymlinks(worktree); err == nil && overlap(resolved, tree.Path) {
			return false
		}
	}
	for _, id := range tree.Dispatches {
		if !platform.IsID("dsp", id) || !d.cfg.Maintenance.Stopped(id, !slices.Contains(tree.Accepted, id)) {
			return false
		}
	}

	return ctx.Err() == nil
}

func overlap(first, second string) bool {
	return first == second || strings.HasPrefix(first, second+string(filepath.Separator)) ||
		strings.HasPrefix(second, first+string(filepath.Separator))
}

func (d *Daemon) cleanTemps(ctx context.Context) {
	held, err := d.db.List(ctx)
	if err != nil {
		d.cleanupWarning(ctx, d.cfg.Maintenance.ShimRoot, err)
		return
	}
	for _, id := range listedDirs(d.cfg.Maintenance.ShimRoot, "dsp") {
		if ctx.Err() != nil {
			return
		}
		if slices.ContainsFunc(held, func(h statedb.Dispatch) bool { return h.ID == id && h.State != statedb.StateEnded }) ||
			!d.cfg.Maintenance.Stopped(id, false) {
			continue
		}
		if err := d.cfg.Maintenance.CleanTemp(ctx, id); err != nil {
			d.cleanupWarning(ctx, id, err)
		}
	}
}

func (d *Daemon) whileIdle(ctx context.Context, prune func()) {
	if !d.diskWorkers.TryLock() {
		return
	}
	defer d.diskWorkers.Unlock()

	held, err := d.db.List(ctx)
	if err != nil || slices.ContainsFunc(held, func(h statedb.Dispatch) bool { return h.State != statedb.StateEnded }) {
		return
	}
	root := d.cfg.Maintenance.ShimRoot
	if _, err := os.Lstat(root); !os.IsNotExist(err) && !diskguard.RealDir(root) {
		return
	}
	entries, err := os.ReadDir(root)
	if err != nil && !os.IsNotExist(err) {
		return
	}
	for _, entry := range entries {
		if platform.IsID("dsp", entry.Name()) && !d.cfg.Maintenance.Stopped(entry.Name(), false) {
			return
		}
	}
	if ctx.Err() == nil {
		prune()
	}
}

func listedDirs(root, prefix string) []string {
	if !diskguard.RealDir(root) {
		return nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var dirs []string
	for _, entry := range entries {
		if entry.IsDir() && platform.IsID(prefix, entry.Name()) {
			dirs = append(dirs, entry.Name())
		}
	}
	return dirs
}

func (d *Daemon) cleanupWarning(ctx context.Context, path string, err error) {
	if ctx.Err() == nil {
		d.logger.WarnContext(ctx, "keeping a managed directory", slog.String("event", "node.cleanup_kept"),
			slog.String("path", path), slog.String("reason", err.Error()))
	}
}

func deliverCleanup(ctx context.Context, payload json.RawMessage, acks chan<- nodeproto.CleanupAck) error {
	if acks == nil {
		return nil
	}
	var ack nodeproto.CleanupAck
	if err := json.Unmarshal(payload, &ack); err != nil {
		return fmt.Errorf("decoding cleanup confirmation: %w", err)
	}
	select {
	case acks <- ack:
	case <-ctx.Done():
	default:
	}
	return nil
}
