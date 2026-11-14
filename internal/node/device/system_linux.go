//go:build linux

package device

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Supported says whether this machine can run the emulator: Google
// publishes it, and the phone image, for Linux x86_64 only.
func Supported() bool { return runtime.GOARCH == "amd64" }

// newSystem is the machine's own, or nil where the emulator cannot run.
func newSystem() System {
	if !Supported() {
		return nil
	}

	return linuxSystem{}
}

// linuxSystem runs the device's programs on this machine.
type linuxSystem struct{}

// runOutputBytes bounds what a short run's output is kept of.
const runOutputBytes = 64 << 10

func (linuxSystem) Run(ctx context.Context, c Command) (Output, error) {
	var stdout, stderr bytes.Buffer

	cmd := exec.CommandContext(ctx, c.Path, c.Args...) //nolint:gosec // A pinned program, with arguments the device wrote.
	cmd.Env = c.Env
	cmd.Stdout, cmd.Stderr = &capped{buf: &stdout}, &capped{buf: &stderr}
	// `adb start-server` leaves the server behind, holding what it inherited open: the run still ends.
	cmd.WaitDelay = 5 * time.Second

	err := cmd.Run()
	out := Output{Stdout: stdout.String(), Stderr: stderr.String()}

	var exit *exec.ExitError
	if errors.As(err, &exit) && ctx.Err() == nil {
		out.Code = exit.ExitCode()

		return out, nil
	}

	if err != nil {
		return Output{}, err
	}

	return out, nil
}

// capped keeps the first bytes written to it and drops the rest.
type capped struct{ buf *bytes.Buffer }

func (c *capped) Write(p []byte) (int, error) {
	if room := runOutputBytes - c.buf.Len(); room > 0 {
		c.buf.Write(p[:min(room, len(p))])
	}

	return len(p), nil
}

func (linuxSystem) Start(c Command) (Process, error) {
	path, err := exec.LookPath(c.Path)
	if err != nil {
		return nil, err
	}

	log, err := os.OpenFile(c.Log, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	defer log.Close() //nolint:errcheck // The child holds its own copy.

	// The program lives as long as the phone, not a request, so it takes no context.
	cmd := exec.Command(path, c.Args...) //nolint:gosec,noctx // A pinned program, with arguments the device wrote.
	cmd.Env = c.Env
	cmd.Stdout, cmd.Stderr = log, log
	// A group of its own: the emulator's qemu is its child, and a node that ends leaves the group running for
	// the next to take back (device.go, adopt).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	p := &linuxProcess{pid: cmd.Process.Pid, done: make(chan struct{})}

	go func() {
		_ = cmd.Wait()

		close(p.done)
	}()

	return p, nil
}

type linuxProcess struct {
	pid  int
	done chan struct{}
}

func (p *linuxProcess) Pid() int              { return p.pid }
func (p *linuxProcess) Done() <-chan struct{} { return p.done }

func (linuxSystem) Signal(pgid int, sig Signal) error {
	s := syscall.SIGTERM
	if sig == Kill {
		s = syscall.SIGKILL
	}

	if pgid <= 1 {
		return fmt.Errorf("no process group %d", pgid)
	}

	err := syscall.Kill(-pgid, s)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}

	return err
}

// Alive reads /proc: a process of the group that is not a zombie and whose
// command line holds needle.
func (linuxSystem) Alive(pgid int, needle string) bool {
	if pgid <= 1 {
		return false
	}

	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false
	}

	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}

		group, state, ok := procStat(pid)
		if !ok || group != pgid || state == "Z" {
			continue
		}

		cmdline, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if err == nil && strings.Contains(strings.ReplaceAll(string(cmdline), "\x00", " "), needle) {
			return true
		}
	}

	return false
}

// procStat is a process's group and state, from /proc/<pid>/stat
// (proc(5): the command is in parentheses and may hold spaces).
func procStat(pid int) (int, string, bool) {
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, "", false
	}

	end := bytes.LastIndexByte(raw, ')')
	if end < 0 {
		return 0, "", false
	}

	// After the command: state, ppid, pgrp.
	fields := strings.Fields(string(raw[end+1:]))
	if len(fields) < 3 {
		return 0, "", false
	}

	group, err := strconv.Atoi(fields[2])
	if err != nil {
		return 0, "", false
	}

	return group, fields[0], true
}

// An X server holds display n with a lock file and listens on a socket
// (Xserver(1)).
func (linuxSystem) DisplayFree(n int) bool {
	return !exists(fmt.Sprintf("/tmp/.X%d-lock", n)) && !exists(fmt.Sprintf("/tmp/.X11-unix/X%d", n))
}

func (linuxSystem) DisplayReady(n int) bool { return exists(fmt.Sprintf("/tmp/.X11-unix/X%d", n)) }

func exists(path string) bool {
	_, err := os.Stat(path)

	return err == nil
}
