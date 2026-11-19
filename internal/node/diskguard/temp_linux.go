package diskguard

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type fileIdentity struct {
	device uint64
	inode  uint64
}

type processFiles map[fileIdentity]struct{}

func sweepSystemTemp(ctx context.Context, home, userHome string, before time.Time, logger *slog.Logger) error {
	const shared = "/tmp"
	snapshot := sync.OnceValues(func() (processFiles, error) {
		open, skipped, err := allProcessFiles(ctx, "/proc", os.Geteuid())
		if skipped > 0 {
			logger.WarnContext(ctx, "could not fully inspect some processes during shared temp cleanup",
				slog.Int("skipped_processes", skipped))
		}
		return open, err
	})
	alternate := os.TempDir()
	sharedGuard := ctx
	if userHome != "" {
		sharedGuard = WithProtection(ctx, userHome)
	}
	if alternate == shared || containsPath(alternate, userHome) || !RealDir(alternate) {
		return sweepTemp(sharedGuard, shared, home, before, snapshot)
	}
	return errors.Join(sweepTemp(WithProtection(sharedGuard, alternate), shared, home, before, snapshot),
		sweepTemp(ctx, alternate, home, before, snapshot))
}

func sweepTemp(ctx context.Context, path, home string, before time.Time,
	openFiles func() (processFiles, error),
) error {
	root, err := OpenRealDir(path)
	if err != nil {
		return err
	}
	defer root.Close() //nolint:errcheck // directory handle
	mount, err := volumeAt(root, ".")
	if err != nil {
		return err
	}
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return err
	}
	var open processFiles
	var removalErr error
	guardEntry := func(info os.FileInfo) error {
		identity, ok := info.Sys().(*syscall.Stat_t)
		if !ok || int(identity.Uid) != os.Geteuid() || !info.ModTime().Before(before) {
			return errors.New("temporary entry is not old and owned by the worker")
		}
		if !info.IsDir() && !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
			return errors.New("temporary entry is a special file")
		}
		if _, used := open[fileIdentity{device: identity.Dev, inode: identity.Ino}]; used {
			return errors.New("temporary entry is in use")
		}
		return nil
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := entry.Name()
		info, err := root.Lstat(name)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || guardEntry(info) != nil {
			continue
		}
		protected, err := deletionIdentities(ctx, filepath.Join(path, name), home, "", false)
		if err != nil {
			continue
		}
		walk := removalWalk{mount: mount, protected: protected, guard: guardEntry, inspect: true}
		if err := walk.visit(ctx, root, name, info); err != nil {
			continue
		}
		if open == nil {
			open, err = openFiles()
			if err != nil {
				return err
			}
			if err := walk.visit(ctx, root, name, info); err != nil {
				continue
			}
		}
		walk.inspect = false
		if err := walk.visit(ctx, root, name, info); err != nil && !os.IsNotExist(err) && removalErr == nil {
			removalErr = err
		}
	}
	return removalErr
}

func allProcessFiles(ctx context.Context, proc string, uid int) (processFiles, int, error) {
	entries, err := os.ReadDir(proc)
	if err != nil {
		return nil, 0, err
	}
	open := make(processFiles)
	skipped := 0
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, skipped, err
		}
		if _, err := strconv.ParseUint(entry.Name(), 10, 32); err != nil {
			continue
		}
		path := filepath.Join(proc, entry.Name())
		owned, err := processHasUID(path, uid)
		switch {
		case processGone(err):
			continue
		case errors.Is(err, fs.ErrPermission):
			skipped++
		case err != nil:
			return nil, skipped, fmt.Errorf("cannot identify process %s: %w", entry.Name(), err)
		case owned:
			if !open.addProcess(path) {
				skipped++
			}
		}
	}
	return open, skipped, nil
}

func processHasUID(path string, uid int) (bool, error) {
	status, err := os.ReadFile(filepath.Join(path, "status")) //nolint:gosec // status of a process enumerated in procfs
	if err != nil {
		return false, err
	}
	for line := range strings.SplitSeq(string(status), "\n") {
		if fields, ok := strings.CutPrefix(line, "Uid:"); ok {
			return slices.Contains(strings.Fields(fields), strconv.Itoa(uid)), nil
		}
	}
	return false, errors.New("proc status has no Uid field")
}

func (open processFiles) addProcess(path string) bool {
	complete := processReadable(open.addMaps(filepath.Join(path, "maps")))
	tasks, err := os.ReadDir(filepath.Join(path, "task"))
	if err != nil {
		return complete && processReadable(err)
	}
	for _, task := range tasks {
		complete = open.addTask(filepath.Join(path, "task", task.Name())) && complete
	}
	return complete
}

func (open processFiles) addTask(path string) bool {
	complete := true
	for _, name := range []string{"cwd", "root"} {
		complete = processReadable(open.addStat(filepath.Join(path, name))) && complete
	}
	dir := filepath.Join(path, "fd")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return complete && processReadable(err)
	}
	for _, entry := range entries {
		complete = processReadable(open.addStat(filepath.Join(dir, entry.Name()))) && complete
	}
	return complete
}

func processReadable(err error) bool { return err == nil || processGone(err) }

func (open processFiles) addStat(path string) error {
	info, err := os.Stat(path)
	if processGone(err) {
		return nil
	}
	if err != nil {
		return err
	}
	identity, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cannot identify the process reference %s", path)
	}
	open[fileIdentity{device: identity.Dev, inode: identity.Ino}] = struct{}{}
	return nil
}

func (open processFiles) addMaps(path string) error {
	f, err := os.Open(path) //nolint:gosec // maps of a process enumerated in procfs
	if err != nil {
		return err
	}
	defer f.Close() //nolint:errcheck // read-only
	lines := bufio.NewScanner(f)
	for lines.Scan() {
		fields := strings.Fields(lines.Text())
		if len(fields) < 5 {
			return errors.New("invalid proc maps record")
		}
		inode, err := strconv.ParseUint(fields[4], 10, 64)
		if err != nil {
			return err
		}
		if inode == 0 {
			continue
		}
		major, minor, _ := strings.Cut(fields[3], ":")
		upper, upperErr := strconv.ParseUint(major, 16, 32)
		lower, lowerErr := strconv.ParseUint(minor, 16, 32)
		if err := errors.Join(upperErr, lowerErr); err != nil {
			return err
		}
		open[fileIdentity{device: unix.Mkdev(uint32(upper), uint32(lower)), inode: inode}] = struct{}{}
	}
	return lines.Err()
}

func processGone(err error) bool {
	return os.IsNotExist(err) || errors.Is(err, syscall.ESRCH)
}
