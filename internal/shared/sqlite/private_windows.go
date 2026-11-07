//go:build windows

package sqlite

// validatePrivateFile does nothing on Windows, which is a development
// platform only; deployments are Linux, where permissions are enforced.
func validatePrivateFile(string) error {
	return nil
}
