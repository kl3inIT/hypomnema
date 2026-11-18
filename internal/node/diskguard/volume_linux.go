package diskguard

import (
	"bufio"
	"errors"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

type mountID = uint64

func mountedPaths() ([]mountedPath, error) {
	file, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return nil, err
	}
	defer file.Close() //nolint:errcheck // read-only mount table
	decode := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	var mounts []mountedPath
	scan := bufio.NewScanner(file)
	for scan.Scan() {
		fields := strings.Fields(scan.Text())
		if len(fields) < 6 {
			return nil, errors.New("invalid mount table entry")
		}
		mounts = append(mounts, mountedPath{path: decode.Replace(fields[4]), alias: fields[3] != "/"})
	}
	return mounts, scan.Err()
}

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
