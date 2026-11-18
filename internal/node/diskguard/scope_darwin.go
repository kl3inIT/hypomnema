package diskguard

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

const sandbox = "/usr/bin/sandbox-exec"

func ScopeAvailable() bool {
	info, err := os.Stat(sandbox)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}

func ScopedExec(args []string) error {
	if len(args) < 2 || !filepath.IsAbs(args[1]) || !ScopeAvailable() {
		return errors.New("a scoped cleanup command requires sandbox-exec and an absolute executable")
	}
	modes := strings.Split(args[0], ",")
	if len(modes) > 4 {
		return errors.New("invalid number of cleanup directory handles")
	}
	var profile strings.Builder
	profile.WriteString(`(version 1)(allow default)(deny file-write*)(allow file-write-data (literal "/dev/null"))`)
	argv := []string{sandbox}
	for index, mode := range modes {
		fd := index + 3
		path, err := sandboxDirectory(fd)
		if err != nil {
			return err
		}
		unix.CloseOnExec(fd)
		switch mode {
		case "all":
			key := fmt.Sprintf("scope%d", index)
			argv = append(argv, "-D", key+"="+path)
			fmt.Fprintf(&profile, `(allow file-write* (subpath (param %q)))`, key)
		case "rmdir":
		default:
			return errors.New("invalid cleanup scope")
		}
	}
	argv = append(argv, "-p", profile.String())
	return unix.Exec(sandbox, append(argv, args[1:]...), os.Environ())
}

func sandboxDirectory(fd int) (string, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return "", err
	}
	var buffer [unix.PathMax]byte
	_, _, errno := unix.Syscall(unix.SYS_FCNTL, uintptr(fd), unix.F_GETPATH, uintptr(unsafe.Pointer(&buffer[0]))) //nolint:gosec // fcntl writes a path to the buffer
	if errno != 0 {
		return "", errno
	}
	path := unix.ByteSliceToString(buffer[:])
	var current unix.Stat_t
	if err := unix.Lstat(path, &current); err != nil {
		return "", err
	}
	if current.Dev != stat.Dev || current.Ino != stat.Ino || current.Mode&unix.S_IFMT != unix.S_IFDIR || !RealDir(path) {
		return "", errors.New("the cleanup directory changed")
	}
	return path, nil
}
