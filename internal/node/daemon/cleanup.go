package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/kl3inIT/hypomnema/internal/node/diskguard"
	"github.com/kl3inIT/hypomnema/internal/node/statedb"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

const cleanupEvery = 5 * time.Minute

type Maintenance struct {
	NodeHome      string
	Binary        string
	ShimRoot      string
	Stopped       func(id string, missingOK bool) bool
	CleanTemp     func(ctx context.Context, id string) error
	CleanWorktree func(ctx context.Context, path string, hasRepo bool, stopped func() bool) error
	// TrimCaches frees room on the worktree volume and reports whether it is still below its floor.
	TrimCaches func(ctx context.Context, whileIdle func(func())) (blocked bool)
}

func (d *Daemon) maintain(ctx context.Context, out chan<- nodeproto.Message, acks <-chan nodeproto.CleanupAck) {
	select {
	case d.maintenance <- struct{}{}:
		defer func() { <-d.maintenance }()
	case <-ctx.Done():
		return
	}

	ticker := time.NewTicker(cleanupEvery)
	defer ticker.Stop()

	for ctx.Err() == nil {
		d.cleanWorktrees(ctx, out, acks)
		ack, err := d.requestCleanup(ctx, out, acks, nil)
		if err == nil {
			var guarded context.Context
			guarded, err = d.cleanupProtection(ctx, ack)
			if err == nil {
				d.cleanTemps(guarded)
				d.trimCaches(guarded)
			}
		}
		if err != nil {
			d.cleanupWarning(ctx, d.cfg.WorktreeRoot, err)
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// trimCaches trims the caches and keeps whether the volume is still below its floor. A recovery a heartbeat saw while
// the trim ran clears what it says: a later drop is reported blocked only by a trim that ran after it (issue #1017).
func (d *Daemon) trimCaches(ctx context.Context) {
	d.diskMu.Lock()
	before := d.diskRecoveries
	d.diskMu.Unlock()

	blocked := d.cfg.Maintenance.TrimCaches(ctx, func(prune func()) { d.whileIdle(ctx, prune) })

	d.diskMu.Lock()
	d.diskBlocked = blocked && d.diskRecoveries == before
	d.diskMu.Unlock()
}

func (d *Daemon) cleanWorktrees(ctx context.Context, out chan<- nodeproto.Message, acks <-chan nodeproto.CleanupAck) {
	root := d.worktreeScanRoot()
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
			batch = append(batch, nodeproto.WorktreePath{
				RunID: run, TaskID: task, Path: filepath.Join(d.cfg.WorktreeRoot, run, task),
			})
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

func (d *Daemon) worktreeScanRoot() string {
	root := filepath.Clean(d.cfg.WorktreeRoot)
	if filepath.Base(root) == "worktrees" {
		if parent, err := filepath.EvalSymlinks(filepath.Dir(root)); err == nil && parent == d.cfg.Maintenance.NodeHome {
			return filepath.Join(parent, "worktrees")
		}
	}
	return root
}

func (d *Daemon) cleanBatch(ctx context.Context, out chan<- nodeproto.Message, acks <-chan nodeproto.CleanupAck,
	paths []nodeproto.WorktreePath,
) error {
	if len(paths) == 0 {
		return nil
	}
	ack, err := d.requestCleanup(ctx, out, acks, paths)
	if err != nil {
		return err
	}
	if ack.Split {
		if len(paths) == 1 {
			d.cleanupWarning(ctx, paths[0].Path, errors.New("the cleanup confirmation exceeds the node message limit"))
			return nil
		}
		middle := len(paths) / 2
		return errors.Join(d.cleanBatch(ctx, out, acks, paths[:middle]), d.cleanBatch(ctx, out, acks, paths[middle:]))
	}
	ctx, err = d.cleanupProtection(ctx, ack)
	if err != nil {
		return err
	}
	home := d.cfg.Maintenance.NodeHome
	allowed := filepath.Join(home, "worktrees")
	root := d.worktreeScanRoot()
	if !diskguard.RealDir(root) {
		return nil
	}
	for _, tree := range ack.Worktrees {
		if ctx.Err() != nil || len(tree.Accepted) == 0 || !slices.Contains(paths, tree.WorktreePath) {
			continue
		}
		tree.Path = filepath.Join(root, tree.RunID, tree.TaskID)

		stopped := func() bool {
			return d.worktreeStopped(ctx, tree)
		}
		if !stopped() {
			continue
		}
		if err := d.cfg.Maintenance.CleanWorktree(ctx, tree.Path, tree.HasRepo, stopped); err != nil {
			d.cleanupWarning(ctx, tree.Path, err)
			continue
		}
		runDir := filepath.Dir(tree.Path)
		if ctx.Err() == nil {
			_ = diskguard.RemoveEmptyChild(ctx, runDir, home, allowed)
		}
	}

	return nil
}

func (d *Daemon) requestCleanup(ctx context.Context, out chan<- nodeproto.Message, acks <-chan nodeproto.CleanupAck,
	paths []nodeproto.WorktreePath,
) (nodeproto.CleanupAck, error) {
	check := nodeproto.CleanupCheck{ID: d.newID("cln"), Paths: paths}
	msg, err := nodeproto.Encode(nodeproto.TypeCleanupCheck, check)
	if err != nil {
		return nodeproto.CleanupAck{}, err
	}
	return awaitCleanup(ctx, out, acks, msg, check.ID)
}

func (d *Daemon) cleanupProtection(ctx context.Context, ack nodeproto.CleanupAck) (context.Context, error) {
	if ack.Protected == nil {
		return ctx, fmt.Errorf("the control plane did not identify protected worktrees")
	}
	paths := slices.Clone(*ack.Protected)
	if d.cfg.Maintenance.Binary != "" {
		paths = append(paths, d.cfg.Maintenance.Binary)
	}
	held, err := d.db.List(ctx)
	if err != nil {
		return ctx, err
	}
	for _, dispatch := range held {
		if dispatch.State != statedb.StateEnded && dispatch.Offer.Worktree != "" {
			paths = append(paths, dispatch.Offer.Worktree)
		}
	}
	return diskguard.WithProtection(ctx, paths...), nil
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
	var paths []string
	for _, h := range held {
		if h.State == statedb.StateEnded {
			continue
		}
		if slices.Contains(tree.Dispatches, h.ID) {
			return false
		}
		if h.Offer.Worktree != "" {
			paths = append(paths, h.Offer.Worktree)
		}
	}
	if len(paths) > 0 && diskguard.Protect(diskguard.WithProtection(ctx, paths...), tree.Path, "", "") != nil {
		return false
	}
	for _, id := range tree.Dispatches {
		if !platform.IsID("dsp", id) || !d.cfg.Maintenance.Stopped(id, !slices.Contains(tree.Accepted, id)) {
			return false
		}
	}

	return ctx.Err() == nil
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
		if slices.ContainsFunc(held, func(h statedb.Dispatch) bool { return h.ID == id && h.State != statedb.StateEnded }) {
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
