//go:build unix

package platform

import "syscall"

// FreeBytes is how many bytes the file system that holds path has free for
// an unprivileged user (statfs's f_bavail), as autodemo reads the free disk
// before a take (src/render.js:326, statfsSync; src/live.js:97-99).
func FreeBytes(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}

	return uint64(st.Bavail) * uint64(st.Bsize), nil //nolint:gosec,unconvert // Never negative; the types vary by OS.
}
