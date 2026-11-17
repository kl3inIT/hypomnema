//go:build unix

package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// The update's marker (issue #716): while `update install` runs, from before it waits for Bots' turns until the
// release's coordinator files are in place, its process is named in UpdatingFile in the control plane's data folder.
// The control plane starts no pi and gives an idle pi no new turn while it is there, so that no pi starts once the
// update has begun, neither in the control plane being stopped nor in the new one before setup-coordinator.sh has put
// pi in place. It is Hermes's live update marker (hermes_cli/update_lock.py read_live_update): the owner's process
// is named, and a marker whose process is gone, an update killed before it removed it, is no update.

// writeMarker names this process in the marker; the returned func removes it. The control plane writes it, under the
// lock each pi start holds from its last look for an update until pi has its token, so no pi starts once it is there
// (coordinator BeginUpdate). When the control plane does not answer (one from before this, or one that is down), the
// marker is written here, and a pi that started meanwhile is stopped before it is given anything.
func (o Options) writeMarker(ctx context.Context) (func(), error) {
	dir := filepath.Join(o.Home, "data")
	pid := os.Getpid()
	start, _ := startTime(pid)
	b := Begin{PID: pid, Start: start}

	if err := o.begin(ctx, b); err != nil {
		o.printf("the control plane did not mark the update (%v); marking it here\n", err)

		if err := WriteMarker(dir, b); err != nil {
			return nil, err
		}
	}

	path := filepath.Join(dir, UpdatingFile)

	return func() {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			o.printf("removing %s failed: %v\n", path, err)
		}
	}, nil
}

// WriteMarker names the update's process in the marker in dir.
func WriteMarker(dir string, b Begin) error {
	if b.PID <= 0 || strings.Trim(b.Start, "0123456789") != "" {
		return fmt.Errorf("%w: an update is named by its process id and start time", errBadBegin)
	}

	path := filepath.Join(dir, UpdatingFile)

	//nolint:gosec // read by the control plane's user and root; it names a process, nothing secret
	if err := os.WriteFile(path, fmt.Appendf(nil, "%d %s\n", b.PID, b.Start), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}

	return nil
}

var errBadBegin = errors.New("bad update")

// Updating says whether an update runs on the control plane whose data folder is dir: its marker is there and names
// a process that runs, the same one (its start time, when the system tells it).
func Updating(dir string) bool {
	data, err := os.ReadFile(filepath.Join(dir, UpdatingFile)) //nolint:gosec // the control plane's own folder
	if err != nil {
		return false
	}

	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return false
	}

	pid, err := strconv.Atoi(fields[0])
	if err != nil || pid <= 0 {
		return false
	}

	if now, ok := startTime(pid); ok {
		return len(fields) < 2 || fields[1] == now
	}

	// No start time to compare (no /proc, or one that hides other users' processes): a signal 0 says whether pid
	// runs, refused for a process of another user, as the update's is.
	err = syscall.Kill(pid, 0)

	return err == nil || errors.Is(err, syscall.EPERM)
}

// startTime is when the process pid started, in clock ticks since boot (proc(5), /proc/<pid>/stat field 22), as
// Hermes pairs a pid with its start time so that a reused pid is not taken for the writer.
func startTime(pid int) (string, bool) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", false
	}

	// The command name, field 2, is in parentheses and may hold spaces: the fields after it are counted from its end.
	_, rest, ok := strings.Cut(string(data[strings.LastIndexByte(string(data), ')')+1:]), " ")
	if !ok {
		return "", false
	}

	fields := strings.Fields(rest)
	// rest starts at field 3; field 22 is the 20th of them.
	if len(fields) < 20 {
		return "", false
	}

	return fields[19], true
}
