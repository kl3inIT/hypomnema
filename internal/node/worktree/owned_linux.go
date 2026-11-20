package worktree

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

type ownedGitLock struct {
	path string
	file *os.File
}

// ownedGitLocks snapshots only open mirror lockfiles of the Git process we are about to stop.
// An open handle pins the inode so a subsequent writer's replacement cannot match it.
func ownedGitLocks(pid int, mirror string) func() {
	if mirror == "" {
		return nil
	}
	mirror, err := filepath.EvalSymlinks(mirror)
	if err != nil {
		return nil
	}
	proc := filepath.Join("/proc", strconv.Itoa(pid))
	dir := filepath.Join(proc, "fd")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var locks []ownedGitLock
	for _, entry := range entries {
		descriptor := filepath.Join(dir, entry.Name())
		path, err := os.Readlink(descriptor)
		if err != nil || !isMirrorGitLock(mirror, path) {
			continue
		}
		flags, inode := gitLockFDInfo(filepath.Join(proc, "fdinfo", entry.Name()))
		if inode == 0 || flags&unix.O_ACCMODE == unix.O_RDONLY {
			continue
		}
		fd, err := unix.Open(descriptor, unix.O_PATH|unix.O_CLOEXEC, 0)
		if err != nil {
			continue
		}
		var stat unix.Stat_t
		if err := unix.Fstat(fd, &stat); err != nil || stat.Ino != inode || stat.Mode&unix.S_IFMT != unix.S_IFREG {
			_ = unix.Close(fd)
			continue
		}
		locks = append(locks, ownedGitLock{path: path, file: os.NewFile(uintptr(fd), path)})
	}
	return func() {
		for _, lock := range locks {
			lock.remove()
		}
	}
}

func isMirrorGitLock(mirror, path string) bool {
	relative, err := filepath.Rel(mirror, path)
	if err != nil || !filepath.IsLocal(relative) || !strings.HasSuffix(relative, ".lock") {
		return false
	}
	switch relative {
	case "config.lock", "index.lock", "shallow.lock", "packed-refs.lock":
		return true
	default:
		return strings.HasPrefix(relative, "refs/")
	}
}

func gitLockFDInfo(path string) (flags, inode uint64) {
	data, err := os.ReadFile(path) //nolint:gosec // /proc/<our Git pid>/fdinfo
	if err != nil {
		return 0, 0
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch key {
		case "flags":
			flags, _ = strconv.ParseUint(strings.TrimSpace(value), 8, 64)
		case "ino":
			inode, _ = strconv.ParseUint(strings.TrimSpace(value), 10, 64)
		}
	}
	return flags, inode
}

func (lock ownedGitLock) remove() {
	defer func() { _ = lock.file.Close() }()
	owned, ownErr := lock.file.Stat()
	current, currentErr := os.Lstat(lock.path)
	if ownErr == nil && currentErr == nil && os.SameFile(owned, current) {
		if err := removeIfPresent(lock.path); err != nil {
			fmt.Fprintln(os.Stderr, "recovering Git lock:", err)
		}
	}
}
