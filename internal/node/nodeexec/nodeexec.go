// Package nodeexec runs an agent's commands on the node's machine (issue
// #543), as OpenClaw's node host runs system.run, Omnara's daemon
// run_command, Nezha's agent server.exec and Coder's agent a workspace's
// execute: `sh -c` as the node's own user, the output kept and the process
// group killed when its time is up. A command may run in the background
// instead, as Coder's and Omnara's may: it answers at once with its id,
// runs with no time limit, and its output is asked for later, as Coder's
// agentproc keeps a process (start, output with a wait, signal; an ended one
// kept an hour). A command run in the foreground is kept by its id too, the
// one its caller chose when it did (issue #1060), so that its result can be
// asked for once the answer that would have carried it is lost, as when the
// control plane restarts while it runs: Coder runs a foreground command as a
// process its tool waits on and reads again after a transport error
// (codersdk/toolsdk/workspacetools/execute.go waitForProcess). The node's own
// end ends them all.
package nodeexec

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// keepEnded is how long a background process that ended is kept for its
// output to be read (Coder's exitedProcessReapAge).
const keepEnded = time.Hour

// listCommandBytes is how much of a command List answers with.
const listCommandBytes = 1 << 20

// keepEndedMost is how many ended processes are kept at most, the oldest
// dropped first, as OpenClaw keeps at most 50 finished sessions
// (src/agents/bash-process-registry.ts MAX_FINISHED_SESSION_COUNT): with
// ExecOutputBytes each, a few megabytes at most (issue #1060).
const keepEndedMost = 50

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
	// ID names it (nodeproto.IsProcessID); the node makes one when empty.
	ID string
	// Agent is whose it is (nodeproto.Exec.Agent).
	Agent   string
	Command string
	// Cwd is where it runs; the node user's home when empty.
	Cwd string
	// Timeout ends a command run in the foreground; a background one has none.
	Timeout time.Duration
	// SudoPassword is given to sudo through its askpass helper; empty: none.
	SudoPassword string
}

// Manager runs the commands and keeps them by their agent and id.
type Manager struct {
	mu    sync.Mutex
	procs map[procKey]*process
	// reserved are the ids of commands being started, taken before they start so that no two start under one id.
	reserved map[procKey]bool
	closed   bool
	now      func() time.Time
}

// procKey is whose a process is and its id: an agent reads and kills its own alone (issue #1060), as Coder keys a
// process by its chat and id (agent/agentproc/process.go procKey).
type procKey struct {
	agent, id string
}

// New returns a Manager.
func New() *Manager {
	return &Manager{procs: map[procKey]*process{}, reserved: map[procKey]bool{}, now: time.Now}
}

type process struct {
	key procKey
	id  string
	// command is the agent's, as it asked; startedAt is when it started.
	command   string
	startedAt time.Time
	// hide is the sudo password given to the command, taken out of what it answers; "" when none.
	hide string
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

	if err := m.keep(p); err != nil {
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

	if err := m.keep(p); err != nil {
		return nodeproto.ExecAnswer{Error: err.Error()}
	}

	return nodeproto.ExecAnswer{ProcessID: p.id, Running: true}
}

// keep keeps a process that started under the id it reserved, for its output to be asked for; one that started as
// the node began to stop is killed.
func (m *Manager) keep(p *process) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.reserved, p.key)

	if m.closed {
		p.kill()

		return errClosed
	}

	m.procs[p.key] = p

	return nil
}

// Output answers about a background process once it has ended, or after
// wait while it still runs.
func (m *Manager) Output(ctx context.Context, agent, id string, wait time.Duration) nodeproto.ExecAnswer {
	p, ok := m.get(procKey{agent: agent, id: id})
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
func (m *Manager) Kill(agent, id string) nodeproto.ExecAnswer {
	p, ok := m.get(procKey{agent: agent, id: id})
	if !ok {
		return unknown(id)
	}

	p.kill()
	<-p.done

	return p.answer(true)
}

// List is the agent's processes the node keeps, running first, then the newest, at most ExecListMost, as Coder
// lists a chat's processes (agent/agentproc/api.go handleListProcesses).
func (m *Manager) List(agent string) []nodeproto.ExecProcess {
	m.mu.Lock()
	m.forgetEnded()

	var own []*process

	for key, p := range m.procs {
		if key.agent == agent {
			own = append(own, p)
		}
	}
	m.mu.Unlock()

	out := make([]nodeproto.ExecProcess, 0, len(own))

	for _, p := range own {
		out = append(out, p.listed())
	}

	slices.SortFunc(out, func(a, b nodeproto.ExecProcess) int {
		if a.Running != b.Running {
			if a.Running {
				return -1
			}

			return 1
		}

		return cmp.Compare(b.StartedAt, a.StartedAt)
	})

	return out[:min(len(out), nodeproto.ExecListMost)]
}

// listed is the process as List answers it; its command with the sudo password hidden.
func (p *process) listed() nodeproto.ExecProcess {
	command := p.command
	if p.hide != "" {
		command = strings.ReplaceAll(command, p.hide, hidden)
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	return nodeproto.ExecProcess{
		ProcessID: p.id, Command: command, Running: p.exitCode == nil, ExitCode: p.exitCode,
		StartedAt: p.startedAt.UnixMilli(),
	}
}

// Close kills the processes still running in the background: they end with
// the node, as Coder's end with its agent.
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	procs := m.procs
	m.procs = map[procKey]*process{}
	m.mu.Unlock()

	for _, p := range procs {
		p.kill()
		<-p.done
	}
}

func (m *Manager) get(key procKey) (*process, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.forgetEnded()
	p, ok := m.procs[key]

	return p, ok
}

// forgetEnded drops the processes that ended more than keepEnded ago, and the
// oldest ended past keepEndedMost. The caller holds m.mu.
func (m *Manager) forgetEnded() {
	now := m.now()

	var ended []*process

	for key, p := range m.procs {
		p.mu.Lock()
		at := p.endedAt
		p.mu.Unlock()

		switch {
		case at.IsZero():
		case now.Sub(at) > keepEnded:
			delete(m.procs, key)
		default:
			ended = append(ended, p)
		}
	}

	if len(ended) <= keepEndedMost {
		return
	}

	slices.SortFunc(ended, func(a, b *process) int { return a.endedAtTime().Compare(b.endedAtTime()) })

	for _, p := range ended[:len(ended)-keepEndedMost] {
		delete(m.procs, p.key)
	}
}

func (p *process) endedAtTime() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.endedAt
}

func unknown(id string) nodeproto.ExecAnswer {
	return nodeproto.ExecAnswer{Error: fmt.Sprintf("no process %q on this node: it never started, it ended more "+
		"than an hour ago, or the node restarted since", id)}
}

// start starts a command as `sh -c` in a process group of its own, its
// output kept, and its sudo given the password when there is one.
func (m *Manager) start(req Request) (*process, error) {
	key, err := m.reserve(req)
	if err != nil {
		return nil, err
	}

	p, err := m.spawn(req, key)
	if err != nil {
		m.mu.Lock()
		delete(m.reserved, key)
		m.mu.Unlock()

		return nil, err
	}

	return p, nil
}

// reserve takes the command's id, the one asked for or a new one, before it starts: a second start under it is
// refused before it runs anything.
func (m *Manager) reserve(req Request) (procKey, error) {
	id := req.ID
	if id == "" {
		id = newID()
	} else if !nodeproto.IsProcessID(id) {
		return procKey{}, fmt.Errorf("the process id %q is not one this node takes", id)
	}

	key := procKey{agent: req.Agent, id: id}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return procKey{}, errClosed
	}

	m.forgetEnded()

	if _, taken := m.procs[key]; taken || m.reserved[key] {
		return procKey{}, fmt.Errorf("a process %q already runs on this node", id)
	}

	m.reserved[key] = true

	return key, nil
}

// spawn starts the command under key.
func (m *Manager) spawn(req Request, key procKey) (*process, error) {
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

	p := &process{
		key: key, id: key.id, command: req.Command, startedAt: m.now(), cmd: cmd, out: out, done: make(chan struct{}),
	}
	if helper != nil {
		p.hide = req.SudoPassword
	}

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

		// An end drops what is past the bounds, as OpenClaw's registry does when a session finishes.
		m.mu.Lock()
		m.forgetEnded()
		m.mu.Unlock()
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

// hidden stands for the sudo password in an answer.
const hidden = "[sudo password hidden]"

// answer is what the process has come to; withID names it, for one in the
// background. The sudo password the command was given never leaves in its
// output, as Hermes redacts secrets from a command's result
// (tools/terminal_tool_result.py).
func (p *process) answer(withID bool) nodeproto.ExecAnswer {
	output, cut := p.out.String()
	if p.hide != "" {
		output = strings.ReplaceAll(output, p.hide, hidden)
	}

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
