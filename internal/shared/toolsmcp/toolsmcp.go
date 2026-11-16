// Package toolsmcp is Hypomnema's stdio MCP server, which an agent's CLI
// starts and which passes the tools the control plane lists to its agent
// API (issues #32, #212, #492). One server for every agent kind, as
// OpenMausBot mounts one `agents` server into every engine it drives
// (server/drivers/agents-mcp.ts) and Omnigent its `serve-mcp` bridge
// (harnesses/claude_native/bridge.py): a worker's CLI runs it as
// `hypomnema-node mcp` with the tools of its own (internal/node/workertools),
// and a Bot's pi as `hypomnema mcp` (clientcli/mcp.go). It holds no tool of
// its own: who starts it says which tools it lists and how each is called.
//
// What it lists may change while its client runs: a Bot given a Connection
// or a tool mid-chat (issue #653). It declares tools.listChanged and, after
// each tools/call, lists again and sends notifications/tools/list_changed
// when the list differs from the one it last answered, as the MCP Go SDK
// notifies only on a real change (mcp/server.go changeAndNotify) and
// Omnigent's bridge on a changed agent (runner/app.py
// _ensure_comment_relay_started). pi, OpenClaw, Hermes and Codex list again
// on it.
package toolsmcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

// Config is what the server lists and how it runs a call.
type Config struct {
	// Name is the server's name; a CLI names its tools after it (mcp__<name>__<tool>).
	Name string
	// List gives the tools, in the shape of tools/list, at each tools/list and after each tools/call. An error
	// says a part could not be listed: tools/list answers the rest, and the list is not taken for a changed one.
	List func(ctx context.Context) ([]map[string]any, error)
	// Call runs one tool. An error is the tool's result, marked as an error, for the agent to read.
	Call func(ctx context.Context, call Call) (any, error)
}

// Call is one tools/call.
type Call struct {
	Name      string
	Arguments json.RawMessage
	// Progress sends notifications/progress for the call, when its client asked for them; otherwise nothing.
	Progress func(n int, message string)
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type server struct {
	cfg Config
	out io.Writer
	mu  sync.Mutex
	// calls in flight, by JSON-RPC id, so a cancellation can end one.
	calls map[string]context.CancelFunc

	listMu sync.Mutex
	// listed is the tools/list last answered, encoded; nil until the client lists.
	listed []byte
}

// Serve answers MCP requests on in until it closes.
func Serve(ctx context.Context, in io.Reader, out io.Writer, cfg Config) error {
	s := &server{cfg: cfg, out: out, calls: map[string]context.CancelFunc{}}

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
				"capabilities":    map[string]any{"tools": map[string]any{"listChanged": true}},
				"serverInfo":      map[string]any{"name": cfg.Name, "version": "1"},
			}, nil)
		case "notifications/initialized":
		case "ping":
			s.reply(r.ID, map[string]any{}, nil)
		case "tools/list":
			listed := []map[string]any{}

			if cfg.List != nil {
				tools, _ := cfg.List(ctx)
				listed = append(listed, tools...)
			}

			s.remember(listed)
			s.reply(r.ID, map[string]any{"tools": listed}, nil)
		case "tools/call":
			callCtx, callCancel := context.WithCancel(ctx)
			s.track(r.ID, callCancel)
			wg.Add(1)

			go func() {
				defer wg.Done()
				defer s.untrack(r.ID)

				result, err := s.call(callCtx, r.Params)
				// Before the answer, so that the client lists again before its model's next request.
				s.notifyIfChanged(ctx)

				if err != nil {
					s.reply(r.ID, Text(err.Error(), true), nil)

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

	if s.cfg.Call == nil {
		return nil, fmt.Errorf("unknown tool %q", p.Name)
	}

	progress := func(int, string) {}
	if token := p.Meta.ProgressToken; len(token) > 0 {
		progress = func(n int, message string) {
			s.write(map[string]any{"jsonrpc": "2.0", "method": "notifications/progress", "params": map[string]any{
				"progressToken": token, "progress": n, "message": message,
			}})
		}
	}

	return s.cfg.Call(ctx, Call{Name: p.Name, Arguments: p.Arguments, Progress: progress})
}

// remember keeps the list answered, to compare a later one with.
func (s *server) remember(listed []map[string]any) {
	raw, err := json.Marshal(listed)
	if err != nil {
		return
	}

	s.listMu.Lock()
	defer s.listMu.Unlock()

	s.listed = raw
}

// notifyIfChanged lists again and tells the client when the list differs
// from the one it last had. A client that never listed, or a list that
// could not be read whole, is not told.
func (s *server) notifyIfChanged(ctx context.Context) {
	if s.cfg.List == nil {
		return
	}

	s.listMu.Lock()
	listedOnce := s.listed != nil
	s.listMu.Unlock()

	if !listedOnce {
		return
	}

	tools, err := s.cfg.List(ctx)
	if err != nil {
		return
	}

	if tools == nil {
		tools = []map[string]any{}
	}

	raw, err := json.Marshal(tools)
	if err != nil {
		return
	}

	s.listMu.Lock()
	changed := !bytes.Equal(raw, s.listed)
	if changed {
		// Told once: the client's next tools/list answers the same.
		s.listed = raw
	}
	s.listMu.Unlock()

	if changed {
		s.write(map[string]any{"jsonrpc": "2.0", "method": "notifications/tools/list_changed"})
	}
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

// Text is a tool's result as MCP has it: one text, and whether it is an
// error.
func Text(text string, isError bool) map[string]any {
	return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": isError}
}
