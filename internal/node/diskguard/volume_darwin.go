package diskguard

import "golang.org/x/sys/unix"

func volume(path string) (unix.Fsid, error) {
	var stat unix.Statfs_t
	err := unix.Statfs(path, &stat)
	return stat.Fsid, err
}
