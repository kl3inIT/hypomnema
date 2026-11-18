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
)

// File names inside a shim's directory.
const (
	SpecFile    = "spec.json"
	JournalFile = "journal.jsonl"
	SocketFile  = "in.sock"
	ExitFile    = "exit.json"
	PIDFile     = "shim.pid"
	StderrFile  = "stderr.log"
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
func Main(ctx context.Context, dir string) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
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

	if err := root.WriteFile(PIDFile, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		return fmt.Errorf("writing the pid file: %w", err)
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

	ln, err := (&net.ListenConfig{}).Listen(ctx, "unix", filepath.Join(dir, SocketFile))
	if err != nil {
		return fmt.Errorf("listening on the input socket: %w", err)
	}
	if socket, ok := ln.(*net.UnixListener); ok {
		socket.SetUnlinkOnClose(false)
	}
	defer func() { _ = root.Remove(SocketFile) }()
	defer ln.Close() //nolint:errcheck // the shim is exiting

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

	tmp := ExitFile + ".tmp"
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
func Stop(ctx context.Context, dir, reason string) error {
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
	if _, err := root.Stat(ExitFile); err == nil {
		return nil
	}
	line, err := json.Marshal(stopRequest{Type: "shim_stop", Reason: reason})
	if err != nil {
		return err
	}
	sendErr := send(ctx, dir, identity, line)
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
	raw, err := json.Marshal(Exit{Code: -1, Error: errors.Join(errors.New(reason), sendErr).Error(), Unconfirmed: true})
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
