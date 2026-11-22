//go:build unix

package videos

import "os"

// syncDir makes a folder's entries durable: a rename into it survives a crash once it returns.
func syncDir(dir string) error {
	f, err := os.Open(dir) //nolint:gosec // The music folder.
	if err != nil {
		return err
	}

	err = f.Sync()
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}

	return err
}
