// Package workertools is the stdio MCP server a worker's CLI starts from
// --mcp-config (issue #32), as Omnigent's `serve-mcp` bridge
// (omnigent/harnesses/claude_native/bridge.py at cf1021e). It offers two
// tools of its own, which talk to the node daemon only through the bridge
// directory, and the tools of the browser every agent shares (issue #212),
// which it passes on to the control plane:
//
//   - ask: a question the worker waits for (a `wait` question of the
//     orchestration core). The call blocks until the answer arrives and sends
//     notifications/progress every ProgressEvery, because Claude Code ends a
//     tool call that is silent for 30 min (spike 2,
//     docs/research/2026-11-08-worker-survival-and-long-tool-calls.md).
//   - finish: the worker's final report, called once (Orca's worker_done),
//     optionally with a decision Gate for the owner.
package workertools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/kl3inIT/hypomnema/internal/node/bridge"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// Timers.
const (
	ProgressEvery = time.Minute
	pollEvery     = time.Second
)

// Name is the server's name in --mcp-config; the tools are mcp__hypomnema__ask and mcp__hypomnema__finish.
const Name = "hypomnema"

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type server struct {
	dir string
	out io.Writer
	mu  sync.Mutex
	// calls in flight, by JSON-RPC id, so a cancellation can end one.
	calls map[string]context.CancelFunc
	// poll is how often an ask looks for its answer (shorter in tests).
	poll     time.Duration
	progress time.Duration
	// agent reaches the control plane's agent API with the Dispatch's key;
	// nil when the runner gave none.
	agent *agentAPI
}

// Serve answers MCP requests on in until it closes.
func Serve(ctx context.Context, in io.Reader, out io.Writer, dir string) error {
	s := &server{
		dir: dir, out: out, calls: map[string]context.CancelFunc{}, poll: pollEvery, progress: ProgressEvery,
		agent: agentOf(dir),
	}

	return s.serve(ctx, in)
}

func (s *server) serve(ctx context.Context, in io.Reader) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64<<10), 8<<20)

	var wg sync.WaitGroup

	defer wg.Wait()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	for sc.Scan() {
		var r request
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			s.reply(nil, nil, &rpcError{Code: -32700, Message: "parse error"})

			continue
		}

		switch r.Method {
		case "initialize":
			var p struct {
				ProtocolVersion string `json:"protocolVersion"`
			}

			_ = json.Unmarshal(r.Params, &p)
			s.reply(r.ID, map[string]any{
				"protocolVersion": p.ProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": Name, "version": "1"},
			}, nil)
		case "notifications/initialized":
		case "ping":
			s.reply(r.ID, map[string]any{}, nil)
		case "tools/list":
			listed := tools()
			if s.agent != nil {
				// The memory's tool is a worker's with the agent API, before the browser's, whose list the
				// control plane gives.
				listed = append(listed, memoryTool())
				listed = append(listed, s.agent.browserTools(ctx)...)
			}

			s.reply(r.ID, map[string]any{"tools": listed}, nil)
		case "tools/call":
			callCtx, callCancel := context.WithCancel(ctx)
			s.track(r.ID, callCancel)
			wg.Add(1)

			go func() {
				defer wg.Done()
				defer s.untrack(r.ID)

				result, err := s.call(callCtx, r.Params)
				if err != nil {
					s.reply(r.ID, toolText(err.Error(), true), nil)

					return
				}

				s.reply(r.ID, result, nil)
			}()
		case "notifications/cancelled": //nolint:misspell // the MCP method is spelled this way
			var p struct {
				RequestID json.RawMessage `json:"requestId"`
			}

			if json.Unmarshal(r.Params, &p) == nil {
				s.cancel(p.RequestID)
			}
		default:
			if len(r.ID) > 0 {
				s.reply(r.ID, nil, &rpcError{Code: -32601, Message: "method not found: " + r.Method})
			}
		}
	}

	return sc.Err()
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (s *server) reply(id json.RawMessage, result any, e *rpcError) {
	msg := map[string]any{"jsonrpc": "2.0", "id": id}
	if e != nil {
		msg["error"] = e
	} else {
		msg["result"] = result
	}

	s.write(msg)
}

func (s *server) write(msg map[string]any) {
	raw, err := json.Marshal(msg)
	if err != nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	_, _ = s.out.Write(append(raw, '\n'))
}

func (s *server) track(id json.RawMessage, cancel context.CancelFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.calls[string(id)] = cancel
}

func (s *server) untrack(id json.RawMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.calls, string(id))
}

func (s *server) cancel(id json.RawMessage) {
	s.mu.Lock()
	cancel := s.calls[string(id)]
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
}

func toolText(text string, isError bool) map[string]any {
	return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": isError}
}

func tools() []map[string]any {
	return []map[string]any{
		{
			"name": "ask",
			"description": "Ask the owner (or the coordinator) a question and wait for the answer. " +
				"Use it instead of guessing when a decision is not yours to make. It can take hours.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"question": map[string]any{"type": "string", "description": "The question, with what is needed to answer it."},
					"options": map[string]any{
						"type": "array", "items": map[string]any{"type": "string"},
						"description": "Choices to pick from; leave empty for a free answer.",
					},
					"to": map[string]any{"type": "string", "enum": []string{"owner", "coordinator"}, "default": "owner"},
				},
				"required": []string{"question"},
			},
		},
		{
			"name": "finish",
			"description": "Report the end of your task, exactly once, then end your turn. " +
				"Give a short summary and the evidence (the pull request). With a gate, the owner decides before work goes on.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"summary": map[string]any{"type": "string"},
					"evidence": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"kind": map[string]any{"type": "string", "description": "pull_request, commit or ci_run"},
								"ref":  map[string]any{"type": "string", "description": "the pull request's URL, a SHA or another URL"},
							},
							"required": []string{"kind", "ref"},
						},
					},
					"gate": map[string]any{
						"type":        "object",
						"description": "A decision the owner must make before the task can go on, such as whether to merge.",
						"properties": map[string]any{
							"question": map[string]any{"type": "string"},
							"options":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
						},
						"required": []string{"question"},
					},
				},
				"required": []string{"summary"},
			},
		},
	}
}

func (s *server) call(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
		Meta      struct {
			ProgressToken json.RawMessage `json:"progressToken"`
		} `json:"_meta"`
	}

	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("invalid call: %w", err)
	}

	switch p.Name {
	case "ask":
		return s.ask(ctx, p.Arguments, p.Meta.ProgressToken)
	case "finish":
		return s.finish(p.Arguments)
	case "memory":
		if s.agent == nil {
			return nil, errors.New("this worker has no memory: its runner gave no agent API")
		}

		return s.agent.memoryCall(ctx, p.Arguments)
	default:
		// Any other tool is the browser's: the control plane knows which it offers.
		if s.agent != nil {
			return s.agent.browserCall(ctx, p.Name, p.Arguments)
		}

		return nil, fmt.Errorf("unknown tool %q", p.Name)
	}
}

func options(labels []string) []nodeproto.Option {
	out := make([]nodeproto.Option, 0, len(labels))
	for _, l := range labels {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, nodeproto.Option{Label: l})
		}
	}

	return out
}

func (s *server) ask(ctx context.Context, raw, progressToken json.RawMessage) (map[string]any, error) {
	var a struct {
		Question string   `json:"question"`
		Options  []string `json:"options"`
		To       string   `json:"to"`
	}

	if err := json.Unmarshal(raw, &a); err != nil || strings.TrimSpace(a.Question) == "" {
		return nil, errors.New("ask needs a question")
	}

	if a.To == "" {
		a.To = "owner"
	}

	seq, err := bridge.Ask(s.dir, nodeproto.Ask{Text: a.Question, Options: options(a.Options), To: a.To})
	if err != nil {
		return nil, fmt.Errorf("recording the question: %w", err)
	}

	poll := time.NewTicker(s.poll)
	defer poll.Stop()

	progress := time.NewTicker(s.progress)
	defer progress.Stop()

	waited := 0

	for {
		// A read that fails (the answer being renamed into place on Windows)
		// is tried again on the next poll.
		answer, ok, err := bridge.Answered(s.dir, seq)
		if err == nil && ok {
			return toolText("The answer: "+answer, false), nil
		}

		select {
		case <-ctx.Done():
			return toolText("The question was withdrawn.", true), nil
		case <-progress.C:
			waited++

			if len(progressToken) > 0 {
				s.write(map[string]any{"jsonrpc": "2.0", "method": "notifications/progress", "params": map[string]any{
					"progressToken": progressToken, "progress": waited,
					"message": fmt.Sprintf("waiting for the answer (%d min)", waited),
				}})
			}
		case <-poll.C:
		}
	}
}

func (s *server) finish(raw json.RawMessage) (map[string]any, error) {
	var f struct {
		Summary  string               `json:"summary"`
		Evidence []nodeproto.Evidence `json:"evidence"`
		Gate     *struct {
			Question string   `json:"question"`
			Options  []string `json:"options"`
		} `json:"gate"`
	}

	if err := json.Unmarshal(raw, &f); err != nil || strings.TrimSpace(f.Summary) == "" {
		return nil, errors.New("finish needs a summary")
	}

	final := bridge.Final{Summary: f.Summary, Evidence: f.Evidence}
	if f.Gate != nil && strings.TrimSpace(f.Gate.Question) != "" {
		final.Gate = &nodeproto.Ask{Text: f.Gate.Question, Options: options(f.Gate.Options), To: "owner"}
	}

	if err := bridge.Finish(s.dir, final); err != nil {
		return nil, fmt.Errorf("recording the final report: %w", err)
	}

	return toolText("Recorded. End your turn now, without calling more tools.", false), nil
}
