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
	Code   int    `json:"code"`
	Signal string `json:"signal,omitempty"`
	Error  string `json:"error,omitempty"`
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
	raw, err := os.ReadFile(filepath.Join(dir, SpecFile)) //nolint:gosec // the daemon's own directory
	if err != nil {
		return fmt.Errorf("reading the spec: %w", err)
	}

	var spec Spec
	if err := json.Unmarshal(raw, &spec); err != nil || len(spec.Argv) == 0 {
		return fmt.Errorf("decoding the spec: %w", errors.Join(err, errors.New("argv is required")))
	}

	// The spec holds the worker's environment, credentials included; it is
	// not needed once the worker runs.
	if err := os.Remove(filepath.Join(dir, SpecFile)); err != nil {
		return fmt.Errorf("removing the spec: %w", err)
	}

	if err := os.WriteFile(filepath.Join(dir, PIDFile), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		return fmt.Errorf("writing the pid file: %w", err)
	}

	//nolint:gosec // the daemon's own directory
	journal, err := os.OpenFile(filepath.Join(dir, JournalFile), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("opening the journal: %w", err)
	}
	defer journal.Close() //nolint:errcheck // every line was synced when written

	//nolint:gosec // the daemon's own directory
	stderr, err := os.OpenFile(filepath.Join(dir, StderrFile), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("opening the stderr log: %w", err)
	}
	defer stderr.Close() //nolint:errcheck // nothing is left to report it to

	_ = os.Remove(filepath.Join(dir, SocketFile))

	ln, err := (&net.ListenConfig{}).Listen(ctx, "unix", filepath.Join(dir, SocketFile))
	if err != nil {
		return fmt.Errorf("listening on the input socket: %w", err)
	}
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

	if err := cmd.Start(); err != nil {
		return writeExit(dir, Exit{Code: -1, Error: err.Error()})
	}

	in := &input{w: stdin}

	go accept(ln, in)

	copyErr := copyLines(stdout, journal)
	waitErr := cmd.Wait()

	return writeExit(dir, exitOf(cmd, errors.Join(copyErr, waitErr)))
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

func accept(ln net.Listener, in *input) {
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

func writeExit(dir string, e Exit) error {
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}

	tmp := filepath.Join(dir, ExitFile+".tmp")
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("writing the exit file: %w", err)
	}

	return os.Rename(tmp, filepath.Join(dir, ExitFile))
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

// Send writes lines to a running shim's worker.
func Send(ctx context.Context, dir string, lines ...[]byte) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(dir, SocketFile))
	if err != nil {
		return fmt.Errorf("connecting to the shim: %w", err)
	}
	defer conn.Close() //nolint:errcheck // written lines are already sent

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
