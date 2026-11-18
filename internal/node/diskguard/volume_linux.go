package diskguard

import (
	"errors"

	"golang.org/x/sys/unix"
)

func volume(path string) (uint64, error) {
	var stat unix.Statx_t
	if err := unix.Statx(unix.AT_FDCWD, path, unix.AT_SYMLINK_NOFOLLOW, unix.STATX_MNT_ID, &stat); err != nil {
		return 0, err
	}

	if stat.Mask&unix.STATX_MNT_ID == 0 {
		return 0, errors.New("mount identity is unavailable")
	}

	return stat.Mnt_id, nil
}
