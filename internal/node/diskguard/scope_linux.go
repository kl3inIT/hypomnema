package diskguard

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"unsafe"

	"golang.org/x/sys/unix"
)

func ScopeAvailable() bool {
	return scopeVersion() != 0
}

func scopeVersion() uintptr {
	version, _, err := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	if err != 0 {
		return 0
	}
	return version
}

func ScopedExec(args []string) error {
	version := scopeVersion()
	if len(args) < 2 || !filepath.IsAbs(args[1]) || version == 0 {
		return errors.New("a scoped disk cleanup command requires Landlock and an absolute executable")
	}
	count, err := strconv.Atoi(args[0])
	if err != nil || count < 1 || count > 4 {
		return errors.New("invalid number of disk cleanup directory handles")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	const mutations = unix.LANDLOCK_ACCESS_FS_WRITE_FILE | unix.LANDLOCK_ACCESS_FS_REMOVE_DIR |
		unix.LANDLOCK_ACCESS_FS_REMOVE_FILE | unix.LANDLOCK_ACCESS_FS_MAKE_DIR | unix.LANDLOCK_ACCESS_FS_MAKE_REG |
		unix.LANDLOCK_ACCESS_FS_MAKE_CHAR | unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK | unix.LANDLOCK_ACCESS_FS_MAKE_FIFO |
		unix.LANDLOCK_ACCESS_FS_MAKE_SYM | unix.LANDLOCK_ACCESS_FS_MAKE_SOCK
	access := uint64(mutations)
	if version >= 2 {
		access |= unix.LANDLOCK_ACCESS_FS_REFER
	}
	if version >= 3 {
		access |= unix.LANDLOCK_ACCESS_FS_TRUNCATE
	}
	fd, _, errNo := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, uintptr(unsafe.Pointer(&access)), unsafe.Sizeof(access), 0)
	if errNo != 0 {
		return errNo
	}
	defer unix.Close(int(fd)) //nolint:errcheck,gosec // kernel returned an fd
	for index := range count {
		directory := 3 + index
		var stat unix.Stat_t
		if err := unix.Fstat(directory, &stat); err != nil {
			return err
		}
		if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
			return errors.New("cleanup scope is not a directory")
		}
		rule := unix.LandlockPathBeneathAttr{Allowed_access: access, Parent_fd: int32(directory)} //nolint:gosec // at most four inherited handles
		if _, _, err := unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, fd, unix.LANDLOCK_RULE_PATH_BENEATH,
			uintptr(unsafe.Pointer(&rule)), 0, 0, 0); err != 0 {
			return err
		}
		unix.CloseOnExec(directory)
	}
	null, err := unix.Open(os.DevNull, unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(null)                                                                                               //nolint:errcheck // device handle
	nullRule := unix.LandlockPathBeneathAttr{Allowed_access: unix.LANDLOCK_ACCESS_FS_WRITE_FILE, Parent_fd: int32(null)} //nolint:gosec // kernel descriptor
	if _, _, err := unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, fd, unix.LANDLOCK_RULE_PATH_BENEATH,
		uintptr(unsafe.Pointer(&nullRule)), 0, 0, 0); err != 0 {
		return err
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return err
	}
	if _, _, err := unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, fd, 0, 0); err != 0 {
		return err
	}
	return unix.Exec(args[1], args[1:], os.Environ())
}
