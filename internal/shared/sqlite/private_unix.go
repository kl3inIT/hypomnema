//go:build unix

package sqlite

import (
	"fmt"
	"os"
	"syscall"
)

// validatePrivateFile refuses a file that group or others can access, or that
// another user owns, as Omnara's localstore does.
func validatePrivateFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspecting the database: %w", err)
	}

	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("database %q has permissions %o; it must be 0600", path, perm)
	}

	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("database %q: owner is unavailable", path)
	}

	if int(stat.Uid) != os.Geteuid() {
		return fmt.Errorf("database %q is owned by uid %d, not by this process", path, stat.Uid)
	}

	return nil
}
