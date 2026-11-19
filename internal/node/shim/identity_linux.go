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
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
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
	return procStat{state: fields[0], group: group, session: session, started: started},
		errors.Join(groupErr, sessionErr, startErr)
}

func identityStart(pid int, boot string) (int64, error) {
	if boot == "" {
		return wallStart(pid)
	}
	stat, err := readProcStat(pid)
	return stat.started, err
}

func identifyShim(pid int, dir string) (processIdentity, error) {
	boot, err := bootID()
	if err != nil {
		return processIdentity{}, err
	}
	stat, err := readProcStat(pid)
	if err != nil {
		return processIdentity{}, err
	}
	return processIdentity{
		PID: pid, Dir: dir, BootID: boot, Started: stat.started, Group: stat.group, Session: stat.session,
	}, nil
}

type workerIdentity struct {
	PID     int
	Started int64
}

func recordWorker(root *os.Root, pid int) error {
	stat, err := readProcStat(pid)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(workerIdentity{PID: pid, Started: stat.started})
	if err != nil {
		return err
	}
	return root.WriteFile(workerIdentityFile, raw, 0o600)
}

func killOrphanedGroup(dir string, identity processIdentity) error {
	if GroupGone(identity.PID) {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(dir, workerIdentityFile)) //nolint:gosec // the daemon's own directory
	if os.IsNotExist(err) {
		return killRemainingSession(identity)
	}
	if err != nil {
		return err
	}
	var worker workerIdentity
	if err := json.Unmarshal(raw, &worker); err != nil {
		return err
	}
	if worker.PID <= 1 || worker.Started <= 0 {
		return errors.New("orphaned shim worker identity is invalid")
	}
	stat, err := readProcStat(worker.PID)
	if os.IsNotExist(err) {
		return killRemainingSession(identity)
	}
	if err != nil {
		return err
	}
	if stat.started != worker.Started {
		return killRemainingSession(identity)
	}
	if stat.group != int64(identity.PID) || stat.session != int64(identity.PID) {
		return errors.New("orphaned shim group identity does not match")
	}
	return killWithoutLeader(identity.PID)
}

func killRemainingSession(identity processIdentity) error {
	if identity.Group != int64(identity.PID) || identity.Session != int64(identity.PID) {
		return errors.New("the shim was not recorded as the group and session leader")
	}
	processes, err := os.ReadDir("/proc")
	if err != nil {
		return err
	}
	for _, entry := range processes {
		member, err := strconv.Atoi(entry.Name())
		if err != nil || !entry.IsDir() || member <= 1 || member == identity.PID {
			continue
		}
		stat, err := readProcStat(member)
		if err == nil && matchesSessionMember(stat, identity) {
			return killWithoutLeader(identity.PID)
		}
	}
	if GroupGone(identity.PID) {
		return nil
	}
	return errors.New("no surviving member matches the recorded shim group and session")
}

func matchesSessionMember(stat procStat, identity processIdentity) bool {
	return stat.group == identity.Group && stat.session == identity.Session && stat.started >= identity.Started
}

func killWithoutLeader(pid int) error {
	if Alive(pid) {
		return errors.New("the shim leader PID was reused")
	}
	return Kill(pid)
}
