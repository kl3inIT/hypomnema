//go:build windows

package videos

// syncDir does nothing on Windows, which syncs no folder through a handle os.Open gives.
func syncDir(string) error { return nil }
