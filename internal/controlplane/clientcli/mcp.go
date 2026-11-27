package clientcli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
	"github.com/kl3inIT/hypomnema/internal/shared/toolsmcp"
)

// A Bot's pi mounts Hypomnema's tools server through pi's own MCP (issue
// #492; pi docs/mcp.md), as OpenMausBot mounts its `agents` server into its
// pi (server/drivers/pi.ts, pi-mcp-servers.ts): the server a worker's CLI
// runs (internal/shared/toolsmcp), started by pi inside the Bot's box as
// `hypomnema mcp`, with the login in ~/.hypomnema/client.json, which is the
// pi's own token there, or the owner's token on the owner's machine. It lists the tools the control plane
// gives that login (PathTools, issue #536: memory, show_artifact, routines, web_search,
// web_fetch, generate_image, audio_transcribe, audio_speak, the shared browser's, and Superset's superset_*
// when its Bot may use a Superset account) and the phone's (PathDeviceTools,
// issue #536 step 4), as a worker's server does (internal/node/workertools),
// so that a Bot is offered none of a worker's own (ask, finish). An image is saved under images/ in the folder pi
// works in, the Bot's, whose files its chat sends, and the answer gives the
// MEDIA line that sends it; show_artifact's path and a Routine's script are
// read in that folder.

const (
	// mcpName is the server's name in mcp.json; pi names its tools mcp__hypomnema__<tool>.
	mcpName = "hypomnema"
	// mcpImages is the folder, in the one pi works in, a Bot's images are saved in.
	mcpImages = "images"
	// mcpAudio is the folder, in the one pi works in, a Bot's speech is saved in (issue #723).
	mcpAudio = "audio"
	// mcpTimeout is pi's wait for one request, in seconds (its default is 60): longer than an image may take
	// (toolsmcp.ImageCallTimeout).
	mcpTimeout = 12 * 60
)

// mcpServe serves the tools on standard input and output until the client closes
// them.
func mcpServe(ctx context.Context, e *env, _ *flag.FlagSet) error {
	agent, err := toolAgent(e.home)
	if err != nil {
		return err
	}

	return toolsmcp.Serve(ctx, e.in, e.out, toolsmcp.Config{
		Name: mcpName,
		List: func(ctx context.Context) ([]map[string]any, error) {
			return listAgentTools(ctx, agent)
		},
		Call: func(ctx context.Context, c toolsmcp.Call) (any, error) {
			return callAgentTool(ctx, agent, c.Name, c.Arguments)
		},
	})
}

func callAgentTool(ctx context.Context, agent *toolsmcp.Agent, name string, args json.RawMessage) (any, error) {
	// A screenshot asked with save_to is written in the Bot's folder (issue #1059).
	if path, within, ok := toolsmcp.ShotRoute(name); ok {
		dir, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("reading the working folder: %w", err)
		}

		return agent.ShotCall(ctx, path, within, name, args, dir)
	}

	switch {
	case devicetool.Routed(name):
		return agent.ToolCall(ctx, agenttool.PathDeviceCall, toolsmcp.DeviceCallTimeout, name, args)
	case agenttool.NodeRereadable(name):
		// A command on a node is read again when the control plane restarts while it runs (issue #1060).
		return agent.NodeCall(ctx, agenttool.PathToolsCall, name, args)
	case name == agenttool.ToolGenerateImage:
		return agent.ImageCall(ctx, args, toolsmcp.Images{Path: agenttool.PathToolsCall, Folder: mcpImages})
	case name == agenttool.ToolAudioTranscribe:
		return agent.TranscribeCall(ctx, args, toolsmcp.Audio{Path: agenttool.PathToolsCall})
	case name == agenttool.ToolAudioSpeak:
		return agent.SpeakCall(ctx, args, toolsmcp.Audio{Path: agenttool.PathToolsCall, Folder: mcpAudio})
	case name == agenttool.ToolShowArtifact || agenttool.SendsAFile(name) || name == agenttool.ToolRoutines:
		dir, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("reading the working folder: %w", err)
		}

		switch name {
		case agenttool.ToolShowArtifact:
			return agent.ArtifactCall(ctx, agenttool.PathToolsCall, args, dir)
		case agenttool.ToolRoutines:
			return agent.RoutineCall(ctx, agenttool.PathToolsCall, args, dir)
		default:
			return agent.SendCall(ctx, agenttool.PathToolsCall, name, args, dir)
		}
	default:
		return agent.ToolCall(ctx, agenttool.PathToolsCall, toolsmcp.CallTimeout, name, args)
	}
}

// piMCPFile is pi's mcp.json (pi docs/mcp.md, "Configure servers").
type piMCPFile struct {
	MCPServers map[string]piMCPServer `json:"mcpServers"`
}

type piMCPServer struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
	Timeout int      `json:"timeout"`
	// No exposure: pi's default, codemode, applies (pi docs/mcp.md, "Control tool exposure"). The model reaches the
	// tools from a codemode script, which receives each result whole and can filter it before the model sees it,
	// where a direct call has a result over 20 KB cut in the middle (owner, 2026-11-21, issue #1015;
	// docs/research/2026-11-21-code-mode.md). Until then it was direct.
}

// piMCP prints the mcp.json a chat's pi starts with: one server, this
// binary's `mcp`, at the path the launcher names, which the box binds.
func piMCP(_ context.Context, e *env, fs *flag.FlagSet) error {
	out, err := json.MarshalIndent(piMCPFile{MCPServers: map[string]piMCPServer{
		mcpName: {Command: fs.Arg(0), Args: []string{"mcp"}, Timeout: mcpTimeout},
	}}, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding pi's mcp.json: %w", err)
	}

	e.printf("%s\n", out)

	return nil
}
