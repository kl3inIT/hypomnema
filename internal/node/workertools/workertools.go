// Package workertools is the stdio MCP server a worker's CLI starts from
// --mcp-config (issue #32), as Omnigent's `serve-mcp` bridge
// (omnigent/harnesses/claude_native/bridge.py at cf1021e): Hypomnema's one
// tools server (internal/shared/toolsmcp, which a Bot's pi mounts too, issue
// #492) with a worker's tools. It offers two tools of its own, which talk to
// the node daemon only through the bridge directory, the memory's, and the
// tools the control plane lists, the browser every agent shares (issue
// #212), the gateway's (issues #354, #492) and the phone's (issue #417),
// which it passes on to the control plane; generate_image's files are read
// and written on the node (toolsmcp/image.go):
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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/node/bridge"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
	"github.com/kl3inIT/hypomnema/internal/shared/toolsmcp"
)

// Timers.
const (
	ProgressEvery = time.Minute
	pollEvery     = time.Second
)

// Name is the server's name in --mcp-config; the tools are mcp__hypomnema__ask and mcp__hypomnema__finish.
const Name = "hypomnema"

type server struct {
	dir string
	// poll is how often an ask looks for its answer (shorter in tests).
	poll     time.Duration
	progress time.Duration
	// agent reaches the control plane's agent API with the Dispatch's key;
	// nil when the runner gave none.
	agent *toolsmcp.Agent
}

// Serve answers MCP requests on in until it closes.
func Serve(ctx context.Context, in io.Reader, out io.Writer, dir string) error {
	s := &server{dir: dir, poll: pollEvery, progress: ProgressEvery, agent: agentOf(dir)}

	return toolsmcp.Serve(ctx, in, out, s.config())
}

// agentOf is the agent API of the Dispatch whose bridge directory dir is, or
// nil when the runner gave none: the worker then has no browser.
func agentOf(dir string) *toolsmcp.Agent {
	agent, ok, err := bridge.AgentOf(dir)
	if err != nil || !ok {
		return nil
	}

	return toolsmcp.NewAgent(agent.URL, agent.Key)
}

// config is the worker's tools: ask and finish, and with the agent API the
// memory's and those the control plane lists, the browser's (with the
// gateway's) and the phone's.
func (s *server) config() toolsmcp.Config {
	return toolsmcp.Config{
		Name: Name,
		List: func(ctx context.Context) []map[string]any {
			listed := tools()
			if s.agent != nil {
				// The memory's tool is a worker's with the agent API, before the browser's, whose list the
				// control plane gives.
				listed = append(listed, memoryTool())
				listed = append(listed, s.agent.Tools(ctx, agenttool.PathBrowserTools)...)
				listed = append(listed, s.agent.Tools(ctx, agenttool.PathDeviceTools)...)
			}

			return listed
		},
		Call: s.call,
	}
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

func (s *server) call(ctx context.Context, c toolsmcp.Call) (any, error) {
	switch c.Name {
	case "ask":
		return s.ask(ctx, c.Arguments, c.Progress)
	case "finish":
		return s.finish(c.Arguments)
	case "memory":
		if s.agent == nil {
			return nil, errors.New("this worker has no memory: its runner gave no agent API")
		}

		return memoryCall(ctx, s.agent, c.Arguments)
	default:
		// The phone's tools are named device_* (issue #417); generate_image's files are read and written here
		// (issue #492); any other tool is one the control plane lists, the browser's or the web's (issue #354): it
		// knows which it offers.
		if s.agent != nil && strings.HasPrefix(c.Name, devicetool.Prefix) {
			return s.agent.ToolCall(ctx, agenttool.PathDeviceCall, toolsmcp.DeviceCallTimeout, c.Name, c.Arguments)
		}

		if s.agent != nil && c.Name == agenttool.ToolGenerateImage {
			return s.agent.ImageCall(ctx, c.Arguments, toolsmcp.Images{Path: agenttool.PathBrowserCall})
		}

		if s.agent != nil {
			return s.agent.ToolCall(ctx, agenttool.PathBrowserCall, toolsmcp.CallTimeout, c.Name, c.Arguments)
		}

		return nil, fmt.Errorf("unknown tool %q", c.Name)
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

func (s *server) ask(ctx context.Context, raw json.RawMessage, report func(n int, message string),
) (map[string]any, error) {
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
			return toolsmcp.Text("The answer: "+answer, false), nil
		}

		select {
		case <-ctx.Done():
			return toolsmcp.Text("The question was withdrawn.", true), nil
		case <-progress.C:
			waited++

			report(waited, fmt.Sprintf("waiting for the answer (%d min)", waited))
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

	return toolsmcp.Text("Recorded. End your turn now, without calling more tools.", false), nil
}
