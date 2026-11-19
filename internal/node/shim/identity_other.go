//go:build !linux

package shim

import (
	"errors"
	"os"
)

func bootID() (string, error) { return "", nil }

func identityStart(pid int, _ string) (int64, error) { return wallStart(pid) }

func identifyShim(pid int, dir string) (processIdentity, error) {
	started, err := wallStart(pid)
	return processIdentity{PID: pid, Started: started, Dir: dir}, err
}

func recordWorker(*os.Root, int) error { return nil }

func killOrphanedGroup(string, processIdentity) error {
	return errors.New("orphaned shim group cannot be verified")
}
