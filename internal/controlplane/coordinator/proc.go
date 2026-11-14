package coordinator

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// proc is one running pi in RPC mode for one chat.
type proc struct {
	s      *Supervisor
	chatID string
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	done   chan struct{}
	mu     sync.Mutex
	// keys are the model gateway's key this pi asks for and its token; each
	// is empty when the supervisor makes none. The key is made again for
	// another model when the chat's choice changes (model.go).
	keys gatewayKey
	// model is the model and effort pi was started or last set with.
	model      orchestration.ChatModel
	working    bool
	lastActive time.Time
	nextID     int
	waiting    map[string]chan response
	stderr     []string
	// calls holds the arguments of running tool calls by id, until they end.
	calls map[string]json.RawMessage
	// stopAsked says the owner stopped the turn that runs (Abort), and
	// stopKept that its stopped row is kept; both clear when the turn settles.
	stopAsked bool
	stopKept  bool
	// compacting says the owner's compaction runs (usage.go).
	compacting bool

	// usageMu orders the reads of pi's context usage; usage is the last kept.
	usageMu sync.Mutex
	usage   *orchestration.ContextUsage
}

type response struct {
	Success bool            `json:"success"`
	Error   string          `json:"error"`
	Data    json.RawMessage `json:"data"`
}

// gatewayKey is what is made for one pi: the model gateway's key, and the
// token it calls the control plane with (issue #260).
type gatewayKey struct {
	id     string
	secret string
	token  string
}

// start runs the configured command with the chat id and turns pi's own
// retries off. The key and the token are held with the process and end with
// it; the token is written first on its stdin, one line before pi's
// commands, for the launcher to read.
func start(ctx context.Context, s *Supervisor, chatID string, modelArgs []string, key gatewayKey) (*proc, error) {
	argv := append(append(append([]string{}, s.cfg.Command...), chatID), modelArgs...)
	//nolint:gosec // the owner's configured command
	cmd := exec.CommandContext(context.WithoutCancel(ctx), argv[0], argv[1:]...)
	cmd.WaitDelay = stopTimeout

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("opening stdin: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("opening stdout: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("opening stderr: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("running %s: %w", argv[0], err)
	}

	p := &proc{
		s: s, chatID: chatID, cmd: cmd, stdin: stdin, done: make(chan struct{}), lastActive: time.Now(),
		keys: key, waiting: map[string]chan response{}, calls: map[string]json.RawMessage{},
	}

	if key.token != "" {
		if _, err := io.WriteString(stdin, key.token+"\n"); err != nil {
			_ = stdin.Close()
			_ = cmd.Process.Kill()
			_ = cmd.Wait()

			return nil, fmt.Errorf("giving pi its token: %w", err)
		}
	}

	go p.readStderr(stderr)
	go p.read(ctx, stdout)

	s.cfg.Logger.InfoContext(ctx, "pi started", slog.String("event", "coordinator.started"),
		slog.String("chat_id", chatID), slog.Int("pid", cmd.Process.Pid))

	if _, err := p.call(ctx, map[string]any{"type": "set_auto_retry", "enabled": false}); err != nil {
		p.stop()

		return nil, fmt.Errorf("turning pi's retries off: %w", err)
	}

	return p, nil
}

// send gives pi a message: a prompt when idle, a follow-up while it works.
func (p *proc) send(ctx context.Context, text string) error {
	cmd := map[string]any{"type": "prompt", "message": text}
	if p.isWorking() {
		cmd["streamingBehavior"] = "followUp"
	}

	r, err := p.call(ctx, cmd)
	if err != nil {
		return err
	}

	var data struct {
		Disposition string `json:"disposition"`
	}

	_ = json.Unmarshal(r.Data, &data)

	if data.Disposition == "started" {
		p.setWorking(true)
	}

	return nil
}

// call writes one command and waits for its response.
func (p *proc) call(ctx context.Context, cmd map[string]any) (response, error) {
	return p.callWithin(ctx, cmd, commandTimeout)
}

// callWithin is call with its own bound on the wait, for a command that takes
// long, such as a compaction.
func (p *proc) callWithin(ctx context.Context, cmd map[string]any, timeout time.Duration) (response, error) {
	p.mu.Lock()
	p.nextID++
	id := "hyp-" + strconv.Itoa(p.nextID)
	ch := make(chan response, 1)
	p.waiting[id] = ch
	p.mu.Unlock()

	defer func() {
		p.mu.Lock()
		delete(p.waiting, id)
		p.mu.Unlock()
	}()

	cmd["id"] = id

	line, err := json.Marshal(cmd)
	if err != nil {
		return response{}, fmt.Errorf("encoding a command: %w", err)
	}

	if _, err := p.stdin.Write(append(line, '\n')); err != nil {
		return response{}, fmt.Errorf("writing to pi: %w", err)
	}

	select {
	case r := <-ch:
		if !r.Success {
			return r, fmt.Errorf("pi refused %s: %s", cmd["type"], r.Error)
		}

		return r, nil
	case <-p.done:
		return response{}, fmt.Errorf("pi exited: %s", p.stderrTail())
	case <-ctx.Done():
		return response{}, ctx.Err()
	case <-time.After(timeout):
		return response{}, fmt.Errorf("pi did not answer %s in %s", cmd["type"], timeout)
	}
}

// read follows pi's stdout until it ends. Records are split on LF only, as
// pi's rpc.md requires.
func (p *proc) read(ctx context.Context, stdout io.Reader) {
	defer func() {
		_ = p.cmd.Wait()

		p.setWorking(false)
		close(p.done)
		p.s.revoke(ctx, p.chatID, p.gatewayKey())
		p.s.cfg.Logger.InfoContext(ctx, "pi exited", slog.String("event", "coordinator.exited"),
			slog.String("chat_id", p.chatID), slog.String("stderr", p.stderrTail()))
		p.s.settled()
	}()

	r := bufio.NewReaderSize(stdout, 64<<10)

	for {
		line, err := r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			p.handle(ctx, bytes.TrimSuffix(bytes.TrimSuffix(line, []byte("\n")), []byte("\r")))
		}

		if err != nil {
			return
		}
	}
}

// toolLine summarizes a tool call in one line: the command for bash, the
// path for file tools.
func toolLine(tool string, raw json.RawMessage) string {
	var args map[string]any
	_ = json.Unmarshal(raw, &args)

	detail := ""

	for _, k := range []string{"command", "path", "file_path", "pattern"} {
		if v, ok := args[k].(string); ok && v != "" {
			detail = v

			break
		}
	}

	detail, _, _ = strings.Cut(detail, "\n")
	if len(detail) > 200 {
		detail = detail[:197] + "..."
	}

	if detail == "" {
		return "pi used " + tool
	}

	return fmt.Sprintf("pi ran %s: %s", tool, detail)
}

func (p *proc) readStderr(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)

	for sc.Scan() {
		p.mu.Lock()
		p.stderr = append(p.stderr, sc.Text())
		if len(p.stderr) > 20 {
			p.stderr = p.stderr[len(p.stderr)-20:]
		}
		p.mu.Unlock()
	}
}

func (p *proc) stderrTail() string {
	p.mu.Lock()
	defer p.mu.Unlock()

	return strings.Join(p.stderr, " | ")
}

func (p *proc) setWorking(w bool) {
	p.mu.Lock()
	changed := p.working != w
	p.working, p.lastActive = w, time.Now()
	p.mu.Unlock()

	if changed {
		p.s.publish(p.chatID, Frame{Event: FrameState, Data: map[string]any{"working": w}})
	}
}

// askStop marks the turn that runs as stopped by the owner and says whether
// one runs.
func (p *proc) askStop() bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.stopAsked = p.working

	return p.working
}

// stopping says whether the turn was stopped and its stopped row is still to
// keep, and takes that row as kept.
func (p *proc) stopping() bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.stopAsked || p.stopKept {
		return false
	}

	p.stopKept = true

	return true
}

// stopSettled clears the stop mark once the turn is over, or when pi refused
// to stop it.
func (p *proc) stopSettled() {
	p.mu.Lock()
	p.stopAsked, p.stopKept = false, false
	p.mu.Unlock()
}

func (p *proc) gatewayKey() gatewayKey {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.keys
}

func (p *proc) isWorking() bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.working
}

func (p *proc) idleFor() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.working {
		return 0
	}

	return time.Since(p.lastActive)
}

func (p *proc) exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// stop closes pi's stdin, which asks it to finish and exit (rpc.md), and
// kills it after stopTimeout.
func (p *proc) stop() {
	_ = p.stdin.Close()

	select {
	case <-p.done:
	case <-time.After(stopTimeout):
		_ = p.cmd.Process.Kill()

		<-p.done
	}
}
