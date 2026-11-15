// Package nodeexec runs an agent's commands on the node's machine (issue
// #543), as OpenClaw's node host runs system.run, Omnara's daemon
// run_command, Nezha's agent server.exec and Coder's agent a workspace's
// execute: `sh -c` as the node's own user, the output kept and the process
// group killed when its time is up. A command may run in the background
// instead, as Coder's and Omnara's may: it answers at once with its id,
// runs with no time limit, and its output is asked for later, as Coder's
// agentproc keeps a process (start, output with a wait, signal; an ended one
// kept an hour). The node's own end ends them all.
package nodeexec

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// keepEnded is how long a background process that ended is kept for its
// output to be read (Coder's exitedProcessReapAge).
const keepEnded = time.Hour

// waitDelay bounds the wait for a killed command's output: a child that
// keeps the pipe open does not hold the answer up (Coder sets 5 s).
const waitDelay = 5 * time.Second

// quietEnv keeps a command from waiting for a person: no pager, no editor,
// no prompt for git's credentials, as Coder sets on every process
// (codersdk/toolsdk/workspacetools/execute.go, nonInteractiveEnv).
var quietEnv = []string{
	"GIT_EDITOR=true", "GIT_SEQUENCE_EDITOR=true", "EDITOR=true", "VISUAL=true", "GIT_TERMINAL_PROMPT=0",
	"NO_COLOR=1", "TERM=dumb", "PAGER=cat", "GIT_PAGER=cat",
}

// errClosed is a command asked for after the node began to stop.
var errClosed = errors.New("the node is stopping")

// Request is one command.
type Request struct {
	Command string
	// Cwd is where it runs; the node user's home when empty.
	Cwd string
	// Timeout ends a command run in the foreground; a background one has none.
	Timeout time.Duration
	// SudoPassword is given to sudo through its askpass helper; empty: none.
	SudoPassword string
}

// Manager runs the commands and keeps those in the background.
type Manager struct {
	mu     sync.Mutex
	procs  map[string]*process
	closed bool
	now    func() time.Time
}

// New returns a Manager.
func New() *Manager {
	return &Manager{procs: map[string]*process{}, now: time.Now}
}

type process struct {
	id   string
	cmd  *exec.Cmd
	out  *headTail
	done chan struct{}

	mu       sync.Mutex
	exitCode *int
	timedOut bool
	endedAt  time.Time
}

// Run runs a command and answers when it ends, or when its time is up and
// it was killed.
func (m *Manager) Run(ctx context.Context, req Request) nodeproto.ExecAnswer {
	p, err := m.start(req)
	if err != nil {
		return nodeproto.ExecAnswer{Error: err.Error()}
	}

	timeout := req.Timeout
	if timeout <= 0 || timeout > nodeproto.ExecTimeoutMax {
		timeout = nodeproto.ExecTimeoutDefault
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-p.done:
	case <-timer.C:
		p.mu.Lock()
		p.timedOut = true
		p.mu.Unlock()
		p.kill()
		<-p.done
	case <-ctx.Done():
		p.kill()
		<-p.done
	}

	return p.answer(false)
}

// Start starts a command in the background and answers at once with its id.
func (m *Manager) Start(req Request) nodeproto.ExecAnswer {
	p, err := m.start(req)
	if err != nil {
		return nodeproto.ExecAnswer{Error: err.Error()}
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		p.kill()

		return nodeproto.ExecAnswer{Error: errClosed.Error()}
	}

	m.forgetEnded()
	m.procs[p.id] = p

	return nodeproto.ExecAnswer{ProcessID: p.id, Running: true}
}

// Output answers about a background process once it has ended, or after
// wait while it still runs.
func (m *Manager) Output(ctx context.Context, id string, wait time.Duration) nodeproto.ExecAnswer {
	p, ok := m.get(id)
	if !ok {
		return unknown(id)
	}

	if wait > nodeproto.ExecWaitMax {
		wait = nodeproto.ExecWaitMax
	}

	if wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()

		select {
		case <-p.done:
		case <-timer.C:
		case <-ctx.Done():
		}
	}

	return p.answer(true)
}

// Kill kills a background process and what it started, and answers with
// what it had written.
func (m *Manager) Kill(id string) nodeproto.ExecAnswer {
	p, ok := m.get(id)
	if !ok {
		return unknown(id)
	}

	p.kill()
	<-p.done

	return p.answer(true)
}

// Close kills the processes still running in the background: they end with
// the node, as Coder's end with its agent.
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	procs := m.procs
	m.procs = map[string]*process{}
	m.mu.Unlock()

	for _, p := range procs {
		p.kill()
		<-p.done
	}
}

func (m *Manager) get(id string) (*process, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.forgetEnded()
	p, ok := m.procs[id]

	return p, ok
}

// forgetEnded drops the processes that ended more than keepEnded ago. The
// caller holds m.mu.
func (m *Manager) forgetEnded() {
	now := m.now()

	for id, p := range m.procs {
		p.mu.Lock()
		ended := p.endedAt
		p.mu.Unlock()

		if !ended.IsZero() && now.Sub(ended) > keepEnded {
			delete(m.procs, id)
		}
	}
}

func unknown(id string) nodeproto.ExecAnswer {
	return nodeproto.ExecAnswer{Error: fmt.Sprintf("no process %q on this node: it ended more than an hour ago, "+
		"or the node restarted since", id)}
}

// start starts a command as `sh -c` in a process group of its own, its
// output kept, and its sudo given the password when there is one.
func (m *Manager) start(req Request) (*process, error) {
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()

	if closed {
		return nil, errClosed
	}

	command := req.Command

	var helper *askpass

	if req.SudoPassword != "" {
		if rewritten, ok := withAskpass(command); ok {
			a, err := newAskpass(req.SudoPassword)
			if err != nil {
				return nil, fmt.Errorf("preparing sudo's password: %w", err)
			}

			command, helper = rewritten, a
		}
	}

	// The command lives as long as it runs, not as long as a request: it takes no context.
	cmd := exec.Command("sh", "-c", command) //nolint:gosec,noctx // G204: the agent's command is the point
	cmd.Env = append(os.Environ(), quietEnv...)

	if helper != nil {
		cmd.Env = append(cmd.Env, "SUDO_ASKPASS="+helper.path())
	}

	cmd.Dir = req.Cwd
	if cmd.Dir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			cmd.Dir = home
		}
	}

	out := newHeadTail(nodeproto.ExecOutputBytes)
	cmd.Stdout, cmd.Stderr = out, out
	cmd.WaitDelay = waitDelay
	ownGroup(cmd)

	if err := cmd.Start(); err != nil {
		if helper != nil {
			helper.remove()
		}

		return nil, fmt.Errorf("starting the command: %w", err)
	}

	p := &process{id: newID(), cmd: cmd, out: out, done: make(chan struct{})}

	go func() {
		err := cmd.Wait()

		if helper != nil {
			helper.remove()
		}

		code := 0

		var exitErr *exec.ExitError

		switch {
		case errors.As(err, &exitErr):
			code = exitErr.ExitCode()
		case err != nil:
			code = -1
		}

		p.mu.Lock()
		p.exitCode, p.endedAt = &code, m.now()
		p.mu.Unlock()

		close(p.done)
	}()

	return p, nil
}

// kill kills the process's group: the shell and what it started.
func (p *process) kill() {
	select {
	case <-p.done:
		return
	default:
	}

	killGroup(p.cmd)
}

// answer is what the process has come to; withID names it, for one in the
// background.
func (p *process) answer(withID bool) nodeproto.ExecAnswer {
	output, cut := p.out.String()

	p.mu.Lock()
	defer p.mu.Unlock()

	a := nodeproto.ExecAnswer{
		Running: p.exitCode == nil, ExitCode: p.exitCode, TimedOut: p.timedOut, Output: output, CutBytes: cut,
	}
	if withID {
		a.ProcessID = p.id
	}

	return a
}

func newID() string {
	raw := make([]byte, 6)
	_, _ = rand.Read(raw)

	return "proc_" + hex.EncodeToString(raw)
}
