// Package browser is the one browser every agent shares (issue #193): a
// headless Chrome on the control plane's machine, with one persistent profile
// so that the owner signs in once, driven through agent-browser.
//
// The browser belongs to the control plane, not to who watches it, as T3
// Code's belongs to its server (docs/user/remote-access.md, "Browser on a
// remote environment") and OpenMausBot's to its own (server/browser/). Each
// agent is an agent-browser session attached to that Chrome and pinned to a
// tab of its own, which is how agent-browser has several sessions share one
// browser and its cookies (skill-data/core/references/session-management.md,
// "Tab Pinning in a Shared Browser").
//
// Chrome is started at the first call and closed when nothing has used it for
// a while; a sign-in outlives that, since it is in the profile on disk.
package browser

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

// The idle rules, T3 Code's (apps/server/src/preview/ServerBrowser.ts,
// AGENT_TAB_IDLE_MS and idleBrowserMs).
const (
	// TabIdle is how long an agent's tab may go unused before it is closed.
	TabIdle = 30 * time.Minute
	// ChromeIdle is how long Chrome runs with no agent's tab.
	ChromeIdle = 5 * time.Minute
	sweepEvery = time.Minute
)

const (
	callTimeout = 3 * time.Minute
	// hostChecked is how long the engine trusts that Chrome is still up.
	hostChecked = 15 * time.Second
	// maxResultBytes bounds a tool's answer; a screenshot is the largest.
	maxResultBytes = 8 << 20
	hostSession    = "host"
	blankPage      = "about:blank"
)

// ErrUnavailable reports that the browser cannot be used now, with why.
var ErrUnavailable = errors.New("the browser is not available")

// command is one run of a program.
type command struct {
	Path  string
	Args  []string
	Env   []string
	Stdin string
}

// output is what a run wrote and how it ended.
type output struct {
	Stdout []byte
	Stderr []byte
	Code   int
}

// Config is how an Engine is set up.
type Config struct {
	// Dir holds everything of the browser: the programs, the profile, and
	// agent-browser's own files.
	Dir string
	// NoSandbox runs Chrome without its sandbox. It is an explicit setting
	// only, as in T3 Code (T3CODE_SERVER_BROWSER_SANDBOX=0): a launch that
	// fails never turns the sandbox off.
	NoSandbox bool
	Logger    *slog.Logger
	Clock     platform.Clock
}

// Engine runs the shared browser.
type Engine struct {
	cfg Config

	// mu guards what follows. It is held while Chrome is installed, started
	// or checked, so those happen once; a tool call holds only its session's
	// own lock.
	mu       sync.Mutex
	binary   string
	chrome   string
	catalog  *catalog
	cdp      string
	checked  time.Time
	cleared  bool
	lastUsed time.Time
	sessions map[string]*session
}

// session is one agent's hold on the browser.
type session struct {
	// mu makes an agent's calls run one at a time, as agent-browser runs one
	// command a session.
	mu     sync.Mutex
	name   string
	opened bool
	// released is set once the session's tab was closed: it is not used again.
	released bool
	used     time.Time
}

// New returns an Engine. Nothing is installed or started until it is used.
func New(cfg Config) *Engine {
	if cfg.Clock == nil {
		cfg.Clock = platform.SystemClock{}
	}

	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}

	return &Engine{cfg: cfg, sessions: map[string]*session{}}
}

func (e *Engine) download(ctx context.Context) (string, string, error) {
	in := installer{dir: e.cfg.Dir}

	binary, err := in.install(ctx, agentBrowser())
	if err != nil {
		return "", "", err
	}

	chrome, err := in.install(ctx, headlessShell())
	if err != nil {
		return "", "", err
	}

	return binary, chrome, nil
}

// Tools lists the tools an agent is offered.
func (e *Engine) Tools(ctx context.Context) ([]Tool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if err := e.installed(ctx); err != nil {
		return nil, err
	}

	return e.catalog.tools, nil
}

// Call runs one tool for an agent, in the agent's own tab, and returns
// agent-browser's answer: an MCP tool result.
func (e *Engine) Call(ctx context.Context, agent, tool string, arguments json.RawMessage) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	s, cdp, cat, err := e.attach(ctx, agent)
	if err != nil {
		return nil, err
	}

	arguments, err = cat.arguments(tool, arguments)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()

	// The session was released while this call waited for it: the agent gets a new one.
	for s.released {
		s.mu.Unlock()

		if s, cdp, _, err = e.attach(ctx, agent); err != nil {
			return nil, err
		}

		s.mu.Lock()
	}

	defer s.mu.Unlock()

	// A session's first command makes its tab. `open` does; anything else is given a blank tab first, so that it
	// never lands in another session's.
	if !s.opened && tool != toolOpen {
		if _, err := e.tool(ctx, s, cdp, toolOpen, json.RawMessage(`{"url":"`+blankPage+`"}`)); err != nil {
			return nil, err
		}
	}

	result, err := e.tool(ctx, s, cdp, tool, arguments)
	if err != nil {
		return nil, err
	}

	s.opened = true

	e.mu.Lock()
	s.used = e.cfg.Clock.Now()
	e.lastUsed = s.used
	e.mu.Unlock()

	return result, nil
}

// attach returns the agent's session, with Chrome running.
func (e *Engine) attach(ctx context.Context, agent string) (*session, string, *catalog, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if err := e.installed(ctx); err != nil {
		return nil, "", nil, err
	}

	if err := e.running(ctx); err != nil {
		return nil, "", nil, err
	}

	s, ok := e.sessions[agent]
	if !ok {
		s = &session{name: sessionName(agent), used: e.cfg.Clock.Now()}
		e.sessions[agent] = s
	}

	return s, e.cdp, e.catalog, nil
}

// installed has the programs and the catalog. The caller holds mu.
func (e *Engine) installed(ctx context.Context) error {
	if e.catalog != nil {
		return nil
	}

	binary, chrome, err := e.download(ctx)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}

	e.binary, e.chrome = binary, chrome

	for _, dir := range []string{e.home(), e.sockets(), e.profile()} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
	}

	listed, err := e.listTools(ctx)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}

	cat, err := newCatalog(listed)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}

	e.catalog = &cat

	return nil
}

// running has Chrome up and its address known. The caller holds mu.
func (e *Engine) running(ctx context.Context) error {
	now := e.cfg.Clock.Now()

	// A Chrome left by an earlier control plane holds tabs of agents this one does not know: it is closed, and
	// the profile on disk keeps the sign-ins.
	if !e.cleared {
		_, _ = e.cli(ctx, hostSession, "", "close")
		e.cleared = true
	}

	if e.cdp != "" && now.Sub(e.checked) < hostChecked {
		return nil
	}

	if e.cdp != "" {
		if address := e.address(ctx); address == e.cdp {
			e.checked = now

			return nil
		}

		// Chrome is gone, and every tab with it.
		e.cfg.Logger.WarnContext(ctx, "the browser was gone and is started again",
			slog.String("event", "browser.restarted"), slog.Int("sessions", len(e.sessions)))
		e.forget(ctx)
	}

	args := []string{"--profile", e.profile(), "--executable-path", e.chrome}
	if e.cfg.NoSandbox {
		args = append(args, "--args", "--no-sandbox")
	}

	out, err := e.cli(ctx, hostSession, "", append(args, "open", blankPage)...)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}

	if out.Code != 0 {
		text := string(out.Stdout) + string(out.Stderr)
		if hint := sandboxHint(text); hint != "" {
			return fmt.Errorf("%w: %s", ErrUnavailable, hint)
		}

		return fmt.Errorf("%w: Chrome did not start: %s", ErrUnavailable, lastLine(text))
	}

	if e.cdp = e.address(ctx); e.cdp == "" {
		return fmt.Errorf("%w: Chrome started and gave no address", ErrUnavailable)
	}

	e.checked, e.lastUsed = now, now
	e.cfg.Logger.InfoContext(ctx, "the browser started", slog.String("event", "browser.started"))

	return nil
}

// address is Chrome's DevTools address, or empty when Chrome is not up. The
// command that asks never starts a browser.
func (e *Engine) address(ctx context.Context) string {
	out, err := e.cli(ctx, hostSession, "", "get", "cdp-url")
	if err != nil || out.Code != 0 {
		return ""
	}

	if line := lastLine(string(out.Stdout)); strings.HasPrefix(line, "ws://") || strings.HasPrefix(line, "wss://") {
		return line
	}

	return ""
}

// forget drops every session after Chrome went away. The caller holds mu.
func (e *Engine) forget(ctx context.Context) {
	for agent, s := range e.sessions {
		_, _ = e.cli(ctx, s.name, e.cdp, "close")

		delete(e.sessions, agent)
	}

	e.cdp = ""
}

// tool runs one MCP tool of agent-browser in a session.
func (e *Engine) tool(
	ctx context.Context, s *session, cdp, tool string, arguments json.RawMessage,
) (json.RawMessage, error) {
	out, err := run(ctx, command{
		Path: e.binary, Args: []string{"mcp", "call", tool, "-", "--tools", "core"},
		Env: e.env(s.name, cdp), Stdin: string(arguments),
	})
	if err != nil {
		return nil, fmt.Errorf("%w: running %s: %w", ErrUnavailable, tool, err)
	}

	line := bytes.TrimSpace(out.Stdout)
	if len(line) > maxResultBytes {
		return nil, fmt.Errorf("%s answered more than %d MB", tool, maxResultBytes>>20)
	}

	// 0 is an answer and 1 an answer that reports a failure to the agent; both are the tool's result. 2 is a
	// call agent-browser did not take (cli/src/mcp.rs, run_mcp_call).
	if out.Code == 0 || out.Code == 1 {
		// An agent is given what the tool says and whether it failed. The rest of agent-browser's answer
		// describes the engine itself: how Chrome was launched, and where.
		var answer struct {
			Content json.RawMessage `json:"content"`
			IsError bool            `json:"isError"`
		}

		if err := json.Unmarshal(line, &answer); err != nil || len(answer.Content) == 0 {
			return nil, fmt.Errorf("%w: %s answered what is not a tool's result", ErrUnavailable, tool)
		}

		result, err := json.Marshal(map[string]any{"content": answer.Content, "isError": answer.IsError})
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
		}

		return result, nil
	}

	var refused struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}

	if json.Unmarshal(line, &refused) == nil && refused.Error.Message != "" {
		return nil, fmt.Errorf("%w: %s", ErrBadArguments, refused.Error.Message)
	}

	return nil, fmt.Errorf("%w: %s ended with code %d: %s", ErrUnavailable, tool, out.Code,
		lastLine(string(out.Stderr)))
}

// listTools asks agent-browser's MCP server for its `core` tools. The server
// ends when its input does.
func (e *Engine) listTools(ctx context.Context) ([]Tool, error) {
	const asked = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18",` +
		`"capabilities":{},"clientInfo":{"name":"hypomnema","version":"1"}}}` + "\n" +
		`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n"

	out, err := run(ctx, command{
		Path: e.binary, Args: []string{"mcp", "--tools", "core"}, Env: e.env(hostSession, ""), Stdin: asked,
	})
	if err != nil {
		return nil, fmt.Errorf("listing agent-browser's tools: %w", err)
	}

	lines := bufio.NewScanner(bytes.NewReader(out.Stdout))
	lines.Buffer(make([]byte, 64<<10), maxResultBytes)

	for lines.Scan() {
		var answer struct {
			ID     int `json:"id"`
			Result struct {
				Tools []Tool `json:"tools"`
			} `json:"result"`
		}

		if json.Unmarshal(lines.Bytes(), &answer) == nil && answer.ID == 2 && len(answer.Result.Tools) > 0 {
			return answer.Result.Tools, nil
		}
	}

	return nil, fmt.Errorf("agent-browser listed no tools: %s", lastLine(string(out.Stderr)))
}

// cli runs an agent-browser command in a session; cdp attaches the session
// to the shared Chrome and is empty for the session that launches it.
func (e *Engine) cli(ctx context.Context, name, cdp string, args ...string) (output, error) {
	return run(ctx, command{Path: e.binary, Args: args, Env: e.env(name, cdp)})
}

// env is what agent-browser is run with. Nothing of the control plane's own
// environment is passed on but PATH: it holds the vault's keys.
func (e *Engine) env(name, cdp string) []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + e.home(),
		"AGENT_BROWSER_SOCKET_DIR=" + e.sockets(),
		"AGENT_BROWSER_SESSION=" + name,
		// The engine ends what is idle; agent-browser's own hour would end a daemon under a tab still in use.
		"AGENT_BROWSER_IDLE_TIMEOUT_MS=0",
	}

	if cdp != "" {
		env = append(env, "AGENT_BROWSER_CDP="+cdp, "AGENT_BROWSER_PIN_TAB=1")
	}

	return env
}

func (e *Engine) home() string    { return filepath.Join(e.cfg.Dir, "home") }
func (e *Engine) sockets() string { return filepath.Join(e.cfg.Dir, "run") }
func (e *Engine) profile() string { return filepath.Join(e.cfg.Dir, "profile") }

// Release closes an agent's tab and ends its session. An agent with none is
// left alone.
func (e *Engine) Release(ctx context.Context, agent string) {
	e.mu.Lock()
	s, ok := e.sessions[agent]
	cdp := e.cdp

	delete(e.sessions, agent)
	e.mu.Unlock()

	if !ok {
		return
	}

	// After the agent's running call, so that it is not cut off mid-way.
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.opened {
		// A session's end leaves its tab open (tried on zm, 2026-11-12), so the tab is closed by itself first.
		_, _ = e.cli(ctx, s.name, cdp, "tab", "close")
	}

	_, _ = e.cli(ctx, s.name, cdp, "close")
	s.released = true
}

// Sweep closes the tabs of agents that have been idle, and Chrome once it has
// had no tab for a while.
func (e *Engine) Sweep(ctx context.Context) {
	now := e.cfg.Clock.Now()

	e.mu.Lock()

	var idle []string

	for agent, s := range e.sessions {
		if now.Sub(s.used) >= TabIdle {
			idle = append(idle, agent)
		}
	}

	e.mu.Unlock()

	for _, agent := range idle {
		e.Release(ctx, agent)
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	if e.cdp == "" || len(e.sessions) > 0 || now.Sub(e.lastUsed) < ChromeIdle {
		return
	}

	e.stop(ctx)
	e.cfg.Logger.InfoContext(ctx, "the idle browser was closed", slog.String("event", "browser.stopped"))
}

// stop closes Chrome. The caller holds mu.
func (e *Engine) stop(ctx context.Context) {
	e.forget(ctx)

	_, _ = e.cli(ctx, hostSession, "", "close")
}

// Run sweeps until ctx ends, then closes Chrome.
func (e *Engine) Run(ctx context.Context) {
	tick := time.NewTicker(sweepEvery)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			// The control plane is stopping: Chrome is closed with what time is given for that.
			closing, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
			defer cancel()

			e.Close(closing)

			return
		case <-tick.C:
			e.Sweep(ctx)
		}
	}
}

// Close ends every session and Chrome.
func (e *Engine) Close(ctx context.Context) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.binary == "" || e.cdp == "" {
		return
	}

	e.stop(ctx)
}

// Status is what the engine is doing.
type Status struct {
	Installed bool
	Running   bool
	// Agents are the agents that hold a tab.
	Agents []string
}

// Status reports the engine's state without starting anything.
func (e *Engine) Status() Status {
	e.mu.Lock()
	defer e.mu.Unlock()

	st := Status{Installed: e.catalog != nil, Running: e.cdp != ""}
	for agent := range e.sessions {
		st.Agents = append(st.Agents, agent)
	}

	return st
}

var sessionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,47}$`)

// sessionName is the agent-browser session of an agent: the agent's own name
// when it is one a session may have, and a digest of it otherwise. The name
// is part of a socket's path, which is short.
func sessionName(agent string) string {
	if sessionPattern.MatchString(agent) && agent != hostSession {
		return agent
	}

	sum := sha256.Sum256([]byte(agent))

	return "a-" + hex.EncodeToString(sum[:12])
}

// lastLine is the last line of a program's output that says something, cut
// to a length fit for an error.
func lastLine(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")

	line := strings.TrimSpace(lines[len(lines)-1])
	if len(line) > 300 {
		line = line[:300]
	}

	return line
}
