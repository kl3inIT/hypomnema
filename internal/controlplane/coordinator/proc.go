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

	mu         sync.Mutex
	working    bool
	lastActive time.Time
	nextID     int
	waiting    map[string]chan response
	stderr     []string
}

type response struct {
	Success bool            `json:"success"`
	Error   string          `json:"error"`
	Data    json.RawMessage `json:"data"`
}

// start runs the configured command with the chat id and turns pi's own
// retries off.
func start(ctx context.Context, s *Supervisor, chatID string, modelArgs []string) (*proc, error) {
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
		waiting: map[string]chan response{},
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
	case <-time.After(commandTimeout):
		return response{}, fmt.Errorf("pi did not answer %s in %s", cmd["type"], commandTimeout)
	}
}

// record is the part of a pi stdout record the supervisor reads.
type record struct {
	Type    string          `json:"type"`
	ID      string          `json:"id"`
	Success bool            `json:"success"`
	Error   string          `json:"error"`
	Data    json.RawMessage `json:"data"`
	Message *struct {
		Role       string `json:"role"`
		StopReason string `json:"stopReason"`
		Error      string `json:"errorMessage"`
		Content    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"message"`
	ToolName string          `json:"toolName"`
	Args     json.RawMessage `json:"args"`
}

// read follows pi's stdout until it ends. Records are split on LF only, as
// pi's rpc.md requires.
func (p *proc) read(ctx context.Context, stdout io.Reader) {
	defer func() {
		_ = p.cmd.Wait()

		p.setWorking(false)
		close(p.done)
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

func (p *proc) handle(ctx context.Context, line []byte) {
	var rec record
	if err := json.Unmarshal(line, &rec); err != nil {
		return
	}

	switch rec.Type {
	case "response":
		p.mu.Lock()
		ch := p.waiting[rec.ID]
		p.mu.Unlock()

		if ch != nil {
			ch <- response{Success: rec.Success, Error: rec.Error, Data: rec.Data}
		}
	case "agent_start":
		p.setWorking(true)
	case "agent_settled":
		p.setWorking(false)
		p.s.settled()
	case "message_end":
		p.keepReply(ctx, rec)
	case "tool_execution_start":
		p.keep(ctx, orchestration.ChatSystem, toolLine(rec.ToolName, rec.Args))
	}
}

// keepReply stores an assistant message's text, or its error.
func (p *proc) keepReply(ctx context.Context, rec record) {
	m := rec.Message
	if m == nil || m.Role != "assistant" {
		return
	}

	var texts []string

	for _, c := range m.Content {
		if c.Type == "text" && strings.TrimSpace(c.Text) != "" {
			texts = append(texts, c.Text)
		}
	}

	if len(texts) > 0 {
		p.keep(ctx, orchestration.ChatCoordinator, strings.Join(texts, "\n\n"))
	}

	if m.StopReason == "error" {
		p.keep(ctx, orchestration.ChatSystem, "pi's model call failed: "+m.Error)
	}
}

func (p *proc) keep(ctx context.Context, role orchestration.ChatRole, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}

	if _, err := p.s.svc.AddChatMessage(ctx, p.chatID, role, text); err != nil && ctx.Err() == nil {
		p.s.cfg.Logger.ErrorContext(ctx, "keeping a chat message failed", slog.String("chat_id", p.chatID),
			slog.Any("error", err))
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
	p.working, p.lastActive = w, time.Now()
	p.mu.Unlock()
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
