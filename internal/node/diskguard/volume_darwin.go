package diskguard

import (
	"os"

	"golang.org/x/sys/unix"
)

type mountID = unix.Fsid

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
