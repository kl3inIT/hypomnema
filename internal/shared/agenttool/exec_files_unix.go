//go:build darwin || linux

package agenttool

import (
	"errors"
	"os"
	"syscall"
)

func privateExecDir(path string, create bool) error {
	if create {
		if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm() != 0o700 {
		return errors.New("the exec-ca directory must be private (mode 0700) and not a symlink")
	}

	return nil
}

func lockExecFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}
