package diskguard

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

type mountID = uint64

func volume(path string) (mountID, error) {
	return volumeAtFD(unix.AT_FDCWD, path)
}

func volumeAt(root *os.Root, path string) (mountID, error) {
	file, err := root.Open(".")
	if err != nil {
		return 0, err
	}
	defer file.Close() //nolint:errcheck // directory handle
	return volumeAtFD(int(file.Fd()), path)
}

func volumeAtFD(fd int, path string) (mountID, error) {
	var stat unix.Statx_t
	if err := unix.Statx(fd, path, unix.AT_SYMLINK_NOFOLLOW, unix.STATX_MNT_ID, &stat); err != nil {
		return 0, err
	}

	if stat.Mask&unix.STATX_MNT_ID == 0 {
		return 0, errors.New("mount identity is unavailable")
	}

	return stat.Mnt_id, nil
}
