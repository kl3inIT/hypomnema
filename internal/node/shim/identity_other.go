//go:build !linux

package shim

import "errors"

func bootID() (string, error) { return "", nil }

func identityStart(pid int, _ string) (int64, error) { return wallStart(pid) }

func recordWorker(string, int) error { return nil }

func killOrphanedGroup(string, int) error {
	return errors.New("orphaned shim group cannot be verified")
}
