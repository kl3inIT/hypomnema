// Package shim keeps one worker process alive across restarts of the node
// daemon (issue #32; spike 1b in
// docs/research/2026-11-08-worker-survival-and-long-tool-calls.md). The
// daemon starts `hypomnema-node shim --dir D` in its own session, as
// Omnara's runner (internal/machinedaemon/runner_subprocess.go at 0ef1711).
// The shim runs the command in D/spec.json, appends every line the worker
// writes on stdout to D/journal.jsonl with an fsync, and writes to the
// worker's stdin what clients send on the unix socket D/in.sock. A line
// {"type":"shim_close"} closes the worker's stdin. When the worker exits the
// shim writes D/exit.json and exits.
package shim

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/process"
)

// File names inside a shim's directory.
const (
	SpecFile      = "spec.json"
	JournalFile   = "journal.jsonl"
	SocketFile    = "in.sock"
	ExitFile      = "exit.json"
	PIDFile       = "shim.pid"
	StderrFile    = "stderr.log"
	PreparingFile = "preparing"
	LaunchingFile = "launching"
	ClaimedFile   = "launch-claimed"
)

// CloseLine is the line that closes the worker's stdin.
const CloseLine = `{"type":"shim_close"}`

type stopRequest struct {
	Type   string `json:"type"`
	Reason string `json:"reason"`
}

// maxLine bounds one journal line (a stream-JSON event).
const maxLine = 16 << 20

// Spec is the worker to run.
type Spec struct {
	Argv []string `json:"argv"`
	// Env is the worker's whole environment; nothing is inherited.
	Env []string `json:"env"`
	Dir string   `json:"dir"`
}

// Exit is how the worker ended.
type Exit struct {
	Code        int    `json:"code"`
	Signal      string `json:"signal,omitempty"`
	Error       string `json:"error,omitempty"`
	Unconfirmed bool   `json:"unconfirmed,omitempty"`
}

// WriteSpec writes the spec a shim will run into dir, private to the owner.
func WriteSpec(dir string, s Spec) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("making %s: %w", dir, err)
	}

	raw, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("encoding the spec: %w", err)
	}

	return os.WriteFile(filepath.Join(dir, SpecFile), raw, 0o600)
}

// Main runs the shim over dir until its worker exits.
func Main(ctx context.Context, socketDir string) error {
	dir, err := realShimDir(socketDir)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close() //nolint:errcheck // directory handle
	if err := recordPID(root); err != nil {
		return fmt.Errorf("writing the pid file: %w", err)
	}
	if _, err := root.Lstat(PreparingFile); err == nil {
		if err := root.Rename(LaunchingFile, ClaimedFile); err != nil {
			return fmt.Errorf("claiming permission to start the worker: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	raw, err := root.ReadFile(SpecFile)
	if err != nil {
		return fmt.Errorf("reading the spec: %w", err)
	}

	var spec Spec
	if err := json.Unmarshal(raw, &spec); err != nil || len(spec.Argv) == 0 {
		return fmt.Errorf("decoding the spec: %w", errors.Join(err, errors.New("argv is required")))
	}

	// The spec holds the worker's environment, credentials included; it is
	// not needed once the worker runs.
	if err := root.Remove(SpecFile); err != nil {
		return fmt.Errorf("removing the spec: %w", err)
	}

	journal, err := root.OpenFile(JournalFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("opening the journal: %w", err)
	}
	defer journal.Close() //nolint:errcheck // every line was synced when written

	stderr, err := root.OpenFile(StderrFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("opening the stderr log: %w", err)
	}
	defer stderr.Close() //nolint:errcheck // nothing is left to report it to

	_ = root.Remove(SocketFile)

	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(socketDir, SocketFile), Net: "unix"})
	if err != nil {
		return fmt.Errorf("listening on the input socket: %w", err)
	}
	ln.SetUnlinkOnClose(false)
	defer func() {
		_ = ln.Close()
		_ = root.Remove(SocketFile)
	}()

	cmd := exec.CommandContext(ctx, spec.Argv[0], spec.Argv[1:]...) //nolint:gosec // the daemon's own spec
	cmd.Env, cmd.Dir, cmd.Stderr = spec.Env, spec.Dir, stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}

	if _, err := root.Stat(ExitFile); err == nil {
		return nil
	}
	stop, err := startWorker(cmd)
	if err != nil {
		return writeExit(root, Exit{Code: -1, Error: err.Error()})
	}
	defer stop.close()
	if err := recordWorker(root, cmd.Process.Pid); err != nil {
		_, _ = stop.kill()
		_ = cmd.Wait()
		return writeExit(root, Exit{Code: -1, Error: err.Error()})
	}
	stopReaping := reapOrphans(cmd.Process.Pid)
	defer stopReaping()

	in := &input{w: stdin}
	stopping := make(chan string, 1)
	go accept(ln, in, stopping)
	finished := make(chan Exit, 1)
	go func() {
		copyErr := copyLines(stdout, journal)
		waitErr := cmd.Wait()
		finished <- exitOf(cmd, errors.Join(copyErr, waitErr))
	}()

	select {
	case exit := <-finished:
		return writeExit(root, exit)
	case reason := <-stopping:
		signaled, err := stop.kill()
		exit := Exit{Code: -1, Error: reason}
		if signaled {
			exit.Signal = killSignal()
		}
		if err != nil {
			exit.Error = errors.Join(errors.New(reason), err).Error()
		}
		select {
		case actual := <-finished:
			exit.Code = actual.Code
			if actual.Signal != "" {
				exit.Signal = actual.Signal
			}
		case <-time.After(250 * time.Millisecond):
		}
		return writeExit(root, exit)
	}
}

// input serializes writes to the worker's stdin from every client.
type input struct {
	mu     sync.Mutex
	w      io.WriteCloser
	closed bool
}

func (in *input) line(b []byte) {
	in.mu.Lock()
	defer in.mu.Unlock()

	if in.closed {
		return
	}

	if string(b) == CloseLine {
		in.closed = true
		_ = in.w.Close()

		return
	}

	_, _ = in.w.Write(append(b, '\n'))
}

func accept(ln net.Listener, in *input, stopping chan<- string) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}

		go func() {
			defer conn.Close() //nolint:errcheck // the client is done

			sc := bufio.NewScanner(conn)
			sc.Buffer(make([]byte, 64<<10), maxLine)

			for sc.Scan() {
				var request stopRequest
				if json.Unmarshal(sc.Bytes(), &request) == nil && request.Type == "shim_stop" {
					select {
					case stopping <- request.Reason:
					default:
					}
					return
				}
				in.line(append([]byte(nil), sc.Bytes()...))
			}
		}()
	}
}

// copyLines appends each line of r to the journal and syncs it, so a line
// the daemon reads is on disk.
func copyLines(r io.Reader, journal *os.File) error {
	br := bufio.NewReaderSize(r, 64<<10)

	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			if line[len(line)-1] != '\n' {
				line = append(line, '\n')
			}

			if _, werr := journal.Write(line); werr != nil {
				return fmt.Errorf("writing the journal: %w", werr)
			}

			if serr := journal.Sync(); serr != nil {
				return fmt.Errorf("syncing the journal: %w", serr)
			}
		}

		if errors.Is(err, io.EOF) {
			return nil
		}

		if err != nil {
			return fmt.Errorf("reading the worker's output: %w", err)
		}
	}
}

func exitOf(cmd *exec.Cmd, err error) Exit {
	out := Exit{}
	if cmd.ProcessState != nil {
		out.Code = cmd.ProcessState.ExitCode()
		out.Signal = signalOf(cmd.ProcessState)
	}

	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		out.Error = err.Error()
	}

	return out
}

func writeExit(root *os.Root, e Exit) error {
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}

	const tmp = ExitFile + ".tmp"
	if err := root.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("writing the exit file: %w", err)
	}

	return root.Rename(tmp, ExitFile)
}

// ReadExit returns the worker's exit, if it has exited.
func ReadExit(dir string) (Exit, bool, error) {
	raw, err := os.ReadFile(filepath.Join(dir, ExitFile)) //nolint:gosec // the daemon's own directory
	if errors.Is(err, os.ErrNotExist) {
		return Exit{}, false, nil
	}

	if err != nil {
		return Exit{}, false, err
	}

	var e Exit
	if err := json.Unmarshal(raw, &e); err != nil {
		return Exit{}, false, fmt.Errorf("decoding the exit file: %w", err)
	}

	return e, true, nil
}

// Stop asks the shim at this Dispatch's socket to kill its own worker and waits for its exit record.
// socketDir is the path the shim was started with; its files are in the real directory it names.
func Stop(ctx context.Context, socketDir, reason string) error {
	dir, err := realShimDir(socketDir)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	identity, err := root.Stat(".")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 4*time.Second)
	defer cancel()
	// A shim that has not claimed its launch never starts the worker once the claim is gone.
	revoked := true
	if err := root.Remove(LaunchingFile); errors.Is(err, os.ErrNotExist) {
		revoked = false
	} else if err != nil {
		return err
	}
	if _, err := root.Stat(ExitFile); err == nil {
		return nil
	}
	if revoked {
		return linkExit(root, Exit{Code: -1, Error: reason + ": the launch was revoked before the worker started"})
	}
	line, err := json.Marshal(stopRequest{Type: "shim_stop", Reason: reason})
	if err != nil {
		return err
	}
	sendErr := send(ctx, socketDir, identity, line)
	for sendErr == nil {
		if _, err := root.Stat(ExitFile); err == nil {
			return nil
		}
		select {
		case <-time.After(25 * time.Millisecond):
			continue
		case <-ctx.Done():
			sendErr = ctx.Err()
		}
	}
	if _, err := root.Stat(ExitFile); err == nil {
		return nil
	}
	// A shim that cannot be reached is killed with its group only if its recorded identity still matches; what
	// it started in another session may survive, so the exit stays unconfirmed.
	if err := KillRecorded(dir); err != nil {
		sendErr = errors.Join(sendErr, err)
	}
	if _, err := root.Stat(ExitFile); err == nil {
		return nil
	}
	return linkExit(root, Exit{Code: -1, Error: errors.Join(errors.New(reason), sendErr).Error(), Unconfirmed: true})
}

// linkExit records an exit the shim did not write, without replacing one it did.
func linkExit(root *os.Root, exit Exit) error {
	raw, err := json.Marshal(exit)
	if err != nil {
		return err
	}
	temp := ExitFile + "." + rand.Text()
	if err := root.WriteFile(temp, raw, 0o600); err != nil {
		return err
	}
	defer func() { _ = root.Remove(temp) }()
	if err := root.Link(temp, ExitFile); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return nil
}

// Send writes lines to a running shim's worker.
func Send(ctx context.Context, dir string, lines ...[]byte) error {
	return send(ctx, dir, nil, lines...)
}

func send(ctx context.Context, dir string, identity os.FileInfo, lines ...[]byte) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(dir, SocketFile))
	if err != nil {
		return fmt.Errorf("connecting to the shim: %w", err)
	}
	defer conn.Close() //nolint:errcheck // written lines are already sent
	if identity != nil {
		current, err := os.Stat(dir)
		if err != nil || !os.SameFile(identity, current) {
			return errors.New("the shim's directory changed while connecting")
		}
	}

	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetWriteDeadline(deadline)
	}

	for _, l := range lines {
		if _, err := conn.Write(append(append([]byte(nil), l...), '\n')); err != nil {
			return fmt.Errorf("writing to the shim: %w", err)
		}
	}

	return nil
}

// PID returns the shim's process id from its pid file.
func PID(dir string) (int, error) {
	raw, err := os.ReadFile(filepath.Join(dir, PIDFile)) //nolint:gosec // the daemon's own directory
	if err != nil {
		return 0, err
	}

	return strconv.Atoi(string(raw))
}

const identityFile = "shim.identity"

type processIdentity struct {
	PID     int
	Started int64
	Dir     string
	BootID  string `json:",omitempty"`
	Group   int64  `json:",omitempty"`
	Session int64  `json:",omitempty"`
}

func wallStart(pid int) (int64, error) {
	proc, err := process.NewProcess(int32(pid)) //nolint:gosec // an operating-system process id fits int32
	if err != nil {
		return 0, err
	}
	return proc.CreateTime()
}

func recordPID(root *os.Root) error {
	pid := os.Getpid()
	identity, err := identifyShim(pid, root.Name())
	if err != nil {
		return err
	}
	raw, err := json.Marshal(identity)
	if err != nil {
		return err
	}
	if err := root.WriteFile(identityFile, raw, 0o600); err != nil {
		return err
	}
	return root.WriteFile(PIDFile, []byte(strconv.Itoa(pid)), 0o600)
}

func KillRecorded(dir string) error {
	dir, err := realShimDir(dir)
	if err != nil {
		return err
	}
	pid, err := PID(dir)
	if err != nil || pid <= 1 {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(dir, identityFile)) //nolint:gosec // the daemon's own directory
	if errors.Is(err, os.ErrNotExist) {
		return killLegacy(dir, pid)
	}
	if err != nil {
		return err
	}
	var identity processIdentity
	if err := json.Unmarshal(raw, &identity); err != nil {
		return err
	}
	identityDir, pathErr := realShimDir(identity.Dir)
	if pid != identity.PID || pathErr != nil || identityDir != dir || identity.Started <= 0 {
		return errors.New("shim process identity does not match")
	}
	if identity.BootID != "" {
		boot, err := bootID()
		if err != nil {
			return err
		}
		if boot != identity.BootID {
			return errors.New("shim process identity is from another boot")
		}
	}
	started, err := identityStart(pid, identity.BootID)
	if identity.BootID != "" && errors.Is(err, os.ErrNotExist) {
		return killOrphanedGroup(dir, identity)
	}
	if err != nil {
		return err
	}
	if started != identity.Started {
		return errors.New("shim process identity does not match")
	}
	return Kill(pid)
}

func RevokeLaunch(dir string) error {
	dir, err := realShimDir(dir)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close() //nolint:errcheck // directory handle
	if err := root.Remove(LaunchingFile); !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return KillRecorded(dir)
}

func realShimDir(dir string) (string, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if filepath.Base(filepath.Dir(dir)) == "run" {
		home, err := filepath.EvalSymlinks(filepath.Dir(filepath.Dir(dir)))
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, "run", filepath.Base(dir))
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil || resolved != dir {
		return "", errors.New("the shim directory is not a real path")
	}
	return dir, nil
}

func killLegacy(dir string, pid int) error {
	info, err := os.Lstat(filepath.Join(dir, PIDFile))
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("legacy shim PID file is not regular")
	}
	proc, err := process.NewProcess(int32(pid)) //nolint:gosec // operating-system process id
	if err != nil {
		return err
	}
	started, err := proc.CreateTime()
	if err != nil || started <= 0 || started > info.ModTime().UnixMilli() {
		return errors.New("legacy PID was reused or could not be verified")
	}
	argv, err := proc.CmdlineSlice()
	if err != nil || len(argv) != 4 || argv[1] != "shim" || argv[2] != "--dir" {
		return errors.New("legacy PID does not belong to the Dispatch shim")
	}
	actual, err := realShimDir(argv[3])
	if err != nil || actual != dir {
		return errors.New("legacy PID does not belong to the Dispatch shim")
	}
	if current, err := wallStart(pid); err != nil || current != started {
		return errors.New("legacy PID identity changed")
	}
	return Kill(pid)
}

// Start starts `exe shim --dir dir` detached from the caller, with no stdio,
// and returns its pid. The spec must already be in dir.
func Start(exe, dir string) (int, error) {
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return 0, err
	}
	defer devnull.Close() //nolint:errcheck // the child has its own copy

	//nolint:noctx // the shim must outlive every context of the daemon
	cmd := exec.Command(exe, "shim", "--dir", dir) //nolint:gosec // the node's own binary
	cmd.Stdin, cmd.Stdout, cmd.Stderr = devnull, devnull, devnull
	detach(cmd)

	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("starting the shim: %w", err)
	}

	pid := cmd.Process.Pid

	// The shim is not our child to wait for in the long run; reap it if it exits while we run.
	go func() { _ = cmd.Wait() }()

	return pid, nil
}
