package diskguard

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

type mountID = unix.Fsid

func mountedPaths() ([]mountedPath, error) {
	count, err := unix.Getfsstat(nil, unix.MNT_NOWAIT)
	if err != nil {
		return nil, err
	}
	stats := make([]unix.Statfs_t, count+1)
	count, err = unix.Getfsstat(stats, unix.MNT_NOWAIT)
	if err != nil || count >= len(stats) {
		return nil, errors.Join(err, errors.New("could not read a stable mount table"))
	}
	mounts := make([]mountedPath, 0, count)
	for _, stat := range stats[:count] {
		mounts = append(mounts, mountedPath{path: unix.ByteSliceToString(stat.Mntonname[:])})
	}
	return mounts, nil
}

func volume(path string) (mountID, error) {
	var stat unix.Statfs_t
	err := unix.Statfs(path, &stat)
	return stat.Fsid, err
}

func volumeAt(root *os.Root, path string) (mountID, error) {
	file, err := root.Open(".")
	if err != nil {
		return mountID{}, err
	}
	defer file.Close() //nolint:errcheck // directory handle
	fd, err := unix.Openat(int(file.Fd()), path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return mountID{}, err
	}
	defer unix.Close(fd) //nolint:errcheck // directory handle
	var stat unix.Statfs_t
	err = unix.Fstatfs(fd, &stat)
	return stat.Fsid, err
}
