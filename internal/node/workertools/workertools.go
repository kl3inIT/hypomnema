// Package workertools is the stdio MCP server a worker's CLI starts from
// --mcp-config (issue #32), as Omnigent's `serve-mcp` bridge
// (omnigent/harnesses/claude_native/bridge.py at cf1021e): Hypomnema's one
// tools server (internal/shared/toolsmcp, which a Bot's pi mounts too, issue
// #492) with a worker's tools. It offers tools of its own, which talk to
// the node daemon only through the bridge directory, and the tools the
// control plane lists for a worker (PathTools, issue #536: the memory's, the
// gateway's, show_artifact and the browser every agent shares) and the
// phone's (issue #417), which it passes on to the control plane;
// generate_image's files are read and written on the node
// (toolsmcp/image.go), and show_artifact's read in the worker's worktree
// (toolsmcp/artifact.go). Until issue #536 the memory's
// tool was defined here; a node of that release still defines it and calls
// agenttool.PathMemory, which the control plane keeps.
//
//   - ask: a question the worker waits for (a `wait` question of the
//     orchestration core). The call blocks until the answer arrives and sends
//     notifications/progress every ProgressEvery, because Claude Code ends a
//     tool call that is silent for 30 min (spike 2,
//     docs/research/2026-11-08-worker-survival-and-long-tool-calls.md).
//   - finish: the worker's final report, called once (Orca's worker_done),
//     optionally with a decision Gate for the owner.
//   - publish_branch: push the GitHub Task branch through the node.
package workertools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
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
	// worktree is the folder the worker works in, where show_artifact reads
	// its path; empty when the runner did not say, and then the folder the
	// server was started in.
	worktree string
}

// Serve answers MCP requests on in until it closes.
func Serve(ctx context.Context, in io.Reader, out io.Writer, dir string) error {
	s := &server{dir: dir, poll: pollEvery, progress: ProgressEvery}
	s.agent, s.worktree = agentOf(dir)

	return toolsmcp.Serve(ctx, in, out, s.config())
}

// agentOf is the agent API of the Dispatch whose bridge directory dir is, or
// nil when the runner gave none: the worker then has no browser; and the
// worker's worktree, when the runner named it.
func agentOf(dir string) (*toolsmcp.Agent, string) {
	agent, ok, err := bridge.AgentOf(dir)
	if err != nil || !ok {
		return nil, ""
	}

	return toolsmcp.NewAgent(agent.URL, agent.Key), agent.Worktree
}

// config is the worker's tools: ask and finish, and with the agent API those
// the control plane lists, its own and the phone's.
func (s *server) config() toolsmcp.Config {
	return toolsmcp.Config{
		Name: Name,
		List: func(ctx context.Context) ([]map[string]any, error) {
			listed := tools()
			if bridge.PublishEnabled(s.dir) {
				listed = append(listed, map[string]any{
					"name":        "publish_branch",
					"description": "Push the committed GitHub Task branch to the Run's repository. The node records the pushed head; it never force-pushes.",
					"inputSchema": map[string]any{"type": "object", "additionalProperties": false},
				})
			}
			if s.agent == nil {
				return listed, nil
			}

			own, err := s.agent.Tools(ctx, agenttool.PathTools)
			device, deviceErr := s.agent.Tools(ctx, agenttool.PathDeviceTools)

			return append(append(listed, own...), device...), errors.Join(err, deviceErr)
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
					"options":  optionsSchema("Choices to pick from; leave empty for a free answer. " + optionsHelp),
					"to":       map[string]any{"type": "string", "enum": []string{"owner", "coordinator"}, "default": "owner"},
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
						"description": "A decision the owner must make before the task can go on, such as a choice between designs.",
						"properties": map[string]any{
							"question": map[string]any{"type": "string"},
							"options":  optionsSchema("Choices to pick from. " + optionsHelp),
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
	case "publish_branch":
		return s.publishBranch(ctx)
	default:
		// The phone's tools are named device_* (issue #417) and video_* (issue #817); generate_image's files are read and written here
		// (issue #492), show_artifact's read here (issue #536); any other tool is one the control plane lists for the worker (issue #536): it knows which
		// it offers.
		if s.agent != nil && devicetool.Routed(c.Name) {
			return s.agent.ToolCall(ctx, agenttool.PathDeviceCall, toolsmcp.DeviceCallTimeout, c.Name, c.Arguments)
		}

		if s.agent != nil && c.Name == agenttool.ToolGenerateImage {
			return s.agent.ImageCall(ctx, c.Arguments, toolsmcp.Images{Path: agenttool.PathToolsCall})
		}

		// The speech tools' recording is read here and their speech saved here (issue #723).
		if s.agent != nil && c.Name == agenttool.ToolAudioTranscribe {
			return s.agent.TranscribeCall(ctx, c.Arguments, toolsmcp.Audio{Path: agenttool.PathToolsCall})
		}

		if s.agent != nil && c.Name == agenttool.ToolAudioSpeak {
			return s.agent.SpeakCall(ctx, c.Arguments, toolsmcp.Audio{Path: agenttool.PathToolsCall})
		}

		if s.agent != nil && c.Name == agenttool.ToolShowArtifact {
			dir, err := s.workFolder()
			if err != nil {
				return nil, err
			}

			return s.agent.ArtifactCall(ctx, agenttool.PathToolsCall, c.Arguments, dir)
		}

		// A command on a node is read again when the control plane restarts while it runs (issue #1060).
		if s.agent != nil && agenttool.NodeRereadable(c.Name) {
			return s.agent.NodeCall(ctx, agenttool.PathToolsCall, c.Name, c.Arguments)
		}

		if s.agent != nil {
			return s.agent.ToolCall(ctx, agenttool.PathToolsCall, toolsmcp.CallTimeout, c.Name, c.Arguments)
		}

		return nil, fmt.Errorf("unknown tool %q", c.Name)
	}
}

func (s *server) publishBranch(ctx context.Context) (any, error) {
	if !bridge.PublishEnabled(s.dir) {
		return nil, errors.New("publish_branch is not available for this Task")
	}
	id, err := bridge.RequestPublish(s.dir)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	poll := time.NewTicker(s.poll)
	defer poll.Stop()
	for {
		result, done, err := bridge.Published(s.dir, id)
		if err != nil {
			return nil, err
		}
		if done {
			return toolsmcp.Text(result.Message, result.Failed), nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-poll.C:
		}
	}
}

// workFolder is where show_artifact reads the path it names: the worktree
// the runner named, else the folder the server was started in, which is the
// worktree for the CLIs that start their servers in their own folder.
func (s *server) workFolder() (string, error) {
	if s.worktree != "" {
		return s.worktree, nil
	}

	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("reading the working folder: %w", err)
	}

	return dir, nil
}

// optionsHelp says how an option takes text (issue #714, decision 8).
const optionsHelp = "An option is its label, or {label, allows_text} when the answer may add text to it " +
	"(such as \"Change first\", with what to change). At most 9: a last option \"Other\", with text, is added."

// optionsSchema is the schema of a question's options: a label, or a label
// with allows_text, so that a plain string, which workers sent before issue
// #714, is still accepted.
func optionsSchema(description string) map[string]any {
	return map[string]any{
		"type": "array",
		"items": map[string]any{"anyOf": []any{
			map[string]any{"type": "string"},
			map[string]any{
				"type": "object",
				"properties": map[string]any{
					"label":       map[string]any{"type": "string"},
					"allows_text": map[string]any{"type": "boolean"},
				},
				"required": []string{"label"},
			},
		}},
		"description": description,
	}
}

// option is one choice as the worker gives it: a label, or a label with
// allows_text.
type option nodeproto.Option

func (o *option) UnmarshalJSON(b []byte) error {
	var label string
	if err := json.Unmarshal(b, &label); err == nil {
		*o = option{Label: label}

		return nil
	}

	var full nodeproto.Option
	if err := json.Unmarshal(b, &full); err != nil {
		return fmt.Errorf("an option is a label or {label, allows_text}: %w", err)
	}

	*o = option(full)

	return nil
}

// otherLabel is the last option of every question with options, which takes
// text: Omnara adds "Other" with AllowsText to every question
// (interaction_tools.go:66-72).
const otherLabel = "Other"

// maxOptions is the control plane's MaxOptions, "Other" included.
const maxOptions = 10

// options keeps the options that have a label, each with its AllowsText, and
// ends them with "Other", which takes text (issue #714, decision 8): added
// when the worker gave none, moved last and made to take text when it gave
// one. A question with more options than fit beside "Other" is refused, so
// the worker cuts one, rather than one of them, or "Other", being dropped.
func options(given []option) ([]nodeproto.Option, error) {
	out := make([]nodeproto.Option, 0, len(given)+1)

	for _, o := range given {
		if o.Label = strings.TrimSpace(o.Label); o.Label != "" && !strings.EqualFold(o.Label, otherLabel) {
			out = append(out, nodeproto.Option(o))
		}
	}

	if len(out) == 0 {
		return nil, nil
	}

	if len(out) > maxOptions-1 {
		return nil, fmt.Errorf("a question offers at most %d options, then \"Other\" with text; give fewer",
			maxOptions-1)
	}

	return append(out, nodeproto.Option{Label: otherLabel, AllowsText: true}), nil
}

func (s *server) ask(ctx context.Context, raw json.RawMessage, report func(n int, message string),
) (map[string]any, error) {
	var a struct {
		Question string   `json:"question"`
		Options  []option `json:"options"`
		To       string   `json:"to"`
	}

	if err := json.Unmarshal(raw, &a); err != nil || strings.TrimSpace(a.Question) == "" {
		return nil, errors.New("ask needs a question")
	}

	if a.To == "" {
		a.To = "owner"
	}

	opts, err := options(a.Options)
	if err != nil {
		return nil, err
	}

	seq, err := bridge.Ask(s.dir, nodeproto.Ask{Text: a.Question, Options: opts, To: a.To})
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
	final, err := ParseFinish(raw)
	if err != nil {
		return nil, err
	}

	if err := bridge.Finish(s.dir, final); err != nil {
		return nil, fmt.Errorf("recording the final report: %w", err)
	}

	return toolsmcp.Text("Recorded. End your turn now, without calling more tools.", false), nil
}

// ParseFinish validates the finish tool arguments for delivery or recovery.
func ParseFinish(raw json.RawMessage) (bridge.Final, error) {
	var f struct {
		Summary  string               `json:"summary"`
		Evidence []nodeproto.Evidence `json:"evidence"`
		Gate     *struct {
			Question string   `json:"question"`
			Options  []option `json:"options"`
		} `json:"gate"`
	}

	if err := json.Unmarshal(raw, &f); err != nil || strings.TrimSpace(f.Summary) == "" {
		return bridge.Final{}, errors.New("finish needs a summary")
	}

	final := bridge.Final{Summary: f.Summary, Evidence: f.Evidence}
	if f.Gate != nil && strings.TrimSpace(f.Gate.Question) != "" {
		opts, err := options(f.Gate.Options)
		if err != nil {
			return bridge.Final{}, err
		}

		final.Gate = &nodeproto.Ask{Text: f.Gate.Question, Options: opts, To: "owner"}
	}

	return final, nil
}
