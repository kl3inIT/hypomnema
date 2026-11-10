//go:build windows

package terminal

// Supported says whether this build can start a shell. The node is released
// for Linux and macOS; on Windows it has no terminal.
func Supported() bool { return false }

// StartShell is not available on Windows.
func StartShell(_, _ uint16) (Shell, error) { return nil, ErrUnsupported }
