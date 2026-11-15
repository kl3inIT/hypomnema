package clientcli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
	"github.com/kl3inIT/hypomnema/internal/shared/toolsmcp"
)

// A Bot's pi mounts Hypomnema's tools server through pi's own MCP (issue
// #492; pi docs/mcp.md), as OpenMausBot mounts its `agents` server into its
// pi (server/drivers/pi.ts, pi-mcp-servers.ts): the server a worker's CLI
// runs (internal/shared/toolsmcp), started by pi inside the Bot's box as
// `hypomnema mcp`, with the login in ~/.hypomnema/client.json, which is the
// pi's own token there. It lists the tools the control plane gives a Bot's pi
// (PathTools, issue #536: memory, show_artifact, web_search, web_fetch,
// generate_image and the shared browser's) and the phone's (PathDeviceTools,
// issue #536 step 4), as a worker's server does (internal/node/workertools),
// so that a Bot is offered none of a worker's own (ask, finish). The phone's
// stay on their own route because a node of the release before this one lists
// both routes, and would list the phone's twice were they on the one list. An image is saved under images/ in the folder pi
// works in, the Bot's, whose files its chat sends, and the answer gives the
// MEDIA line that sends it; show_artifact's path is read in that folder.

const (
	// mcpName is the server's name in mcp.json; pi names its tools mcp__hypomnema__<tool>.
	mcpName = "hypomnema"
	// mcpImages is the folder, in the one pi works in, a Bot's images are saved in.
	mcpImages = "images"
	// mcpTimeout is pi's wait for one request, in seconds (its default is 60): longer than an image may take
	// (toolsmcp.ImageCallTimeout).
	mcpTimeout = 12 * 60
)

// mcpServe serves the tools on standard input and output until pi closes
// them.
func mcpServe(ctx context.Context, e *env, _ *flag.FlagSet) error {
	cfg, err := LoadConfig(e.home)
	if err != nil {
		return err
	}

	agent := toolsmcp.NewAgent(cfg.URL, cfg.Token)
	if agent == nil {
		return errors.New("the login names no control plane")
	}

	return toolsmcp.Serve(ctx, e.in, e.out, toolsmcp.Config{
		Name: mcpName,
		List: func(ctx context.Context) []map[string]any {
			return append(agent.Tools(ctx, agenttool.PathTools), agent.Tools(ctx, agenttool.PathDeviceTools)...)
		},
		Call: func(ctx context.Context, c toolsmcp.Call) (any, error) {
			switch {
			case strings.HasPrefix(c.Name, devicetool.Prefix):
				return agent.ToolCall(ctx, agenttool.PathDeviceCall, toolsmcp.DeviceCallTimeout, c.Name, c.Arguments)
			case c.Name == agenttool.ToolGenerateImage:
				return agent.ImageCall(ctx, c.Arguments,
					toolsmcp.Images{Path: agenttool.PathToolsCall, Folder: mcpImages})
			case c.Name == agenttool.ToolShowArtifact:
				// pi starts the server in the folder it works in, the Bot's, where its pages are.
				dir, err := os.Getwd()
				if err != nil {
					return nil, fmt.Errorf("reading the working folder: %w", err)
				}

				return agent.ArtifactCall(ctx, agenttool.PathToolsCall, c.Arguments, dir)
			}

			return agent.ToolCall(ctx, agenttool.PathToolsCall, toolsmcp.CallTimeout, c.Name, c.Arguments)
		},
	})
}

// piMCPFile is pi's mcp.json (pi docs/mcp.md, "Configure servers").
type piMCPFile struct {
	MCPServers map[string]piMCPServer `json:"mcpServers"`
}

type piMCPServer struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
	Timeout int      `json:"timeout"`
	// Exposure is direct: each tool is declared to the model like a built-in one, not found through codemode or
	// tool_search (owner, 2026-11-15), as OpenMausBot's pi registers every tool it is given.
	Exposure string `json:"exposure"`
}

// piMCP prints the mcp.json a chat's pi starts with: one server, this
// binary's `mcp`, at the path the launcher names, which the box binds.
func piMCP(_ context.Context, e *env, fs *flag.FlagSet) error {
	out, err := json.MarshalIndent(piMCPFile{MCPServers: map[string]piMCPServer{
		mcpName: {Command: fs.Arg(0), Args: []string{"mcp"}, Timeout: mcpTimeout, Exposure: "direct"},
	}}, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding pi's mcp.json: %w", err)
	}

	e.printf("%s\n", out)

	return nil
}
