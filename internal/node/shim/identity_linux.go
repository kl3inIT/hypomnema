package shim

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const workerIdentityFile = "shim.worker"

type procStat struct {
	state   string
	group   int64
	session int64
	started int64
}

func bootID() (string, error) {
	raw, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	return strings.TrimSpace(string(raw)), err
}

func readProcStat(pid int) (procStat, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)) //nolint:gosec // a numeric PID in procfs
	if err != nil {
		return procStat{}, err
	}
	return parseProcStat(string(raw))
}

func parseProcStat(raw string) (procStat, error) {
	end := strings.LastIndexByte(raw, ')')
	if end < 0 {
		return procStat{}, errors.New("missing process name in proc stat")
	}
	fields := strings.Fields(raw[end+1:])
	if len(fields) < 20 {
		return procStat{}, errors.New("missing start ticks in proc stat")
	}
	group, groupErr := strconv.ParseInt(fields[2], 10, 64)
	session, sessionErr := strconv.ParseInt(fields[3], 10, 64)
	started, startErr := strconv.ParseInt(fields[19], 10, 64)
	return procStat{state: fields[0], group: group, session: session, started: started}, errors.Join(groupErr, sessionErr, startErr)
}

func identityStart(pid int, boot string) (int64, error) {
	if boot == "" {
		return wallStart(pid)
	}
	stat, err := readProcStat(pid)
	return stat.started, err
}

type workerIdentity struct {
	PID     int
	Started int64
}

func recordWorker(dir string, pid int) error {
	stat, err := readProcStat(pid)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(workerIdentity{PID: pid, Started: stat.started})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, workerIdentityFile), raw, 0o600)
}

func killOrphanedGroup(dir string, group int) error {
	if GroupGone(group) {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(dir, workerIdentityFile)) //nolint:gosec // the daemon's own directory
	if err != nil {
		return err
	}
	var worker workerIdentity
	if err := json.Unmarshal(raw, &worker); err != nil {
		return err
	}
	stat, err := readProcStat(worker.PID)
	if err != nil {
		return err
	}
	if worker.Started <= 0 || stat.started != worker.Started || stat.group != int64(group) || stat.session != int64(group) {
		return errors.New("orphaned shim group identity does not match")
	}
	return Kill(group)
}
