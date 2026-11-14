//go:build !linux

package device

// Supported says whether this machine can run the emulator: Google
// publishes it, and the phone image, for Linux x86_64 only.
func Supported() bool { return false }

// newSystem is nil: the emulator does not run here.
func newSystem() System { return nil }
