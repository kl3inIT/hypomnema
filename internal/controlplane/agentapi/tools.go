package agentapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"

	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// The control plane's tools for an agent (issue #536, the decision
// docs/decisions/2026-11-15-one-tool-for-every-agent.md): one list, each
// tool given to the agent kinds its definition names, served by Hypomnema's
// one tools server to a Bot's pi (`hypomnema mcp`) and a worker
// (`hypomnema-node mcp`), as OpenMausBot's `agents` server lists one catalog
// for every engine and filters it by the bot's profile
// (server/drivers/agents-catalog.ts). Which kind gets which:
//
//   - memory (memory.go): a worker and a Bot's pi, each its own targets;
//   - show_artifact (artifacts.go): a worker into its Run's chat, a Bot's pi
//     into its own;
//   - web_search, web_fetch, generate_image (web.go): a worker by its
//     runtime, a Bot's pi all three;
//   - superset_dashboards, superset_dashboard, superset_chart_data,
//     superset_filter_values,
//     superset_api (superset.go): a worker given a Superset Connection by its
//     Run, a Bot's pi one switched on for chats and on its Bot's list;
//   - whatsapp_chats, whatsapp_messages, whatsapp_media, whatsapp_history
//     (whatsapp.go, issue #638): a Bot's pi given the owner's WhatsApp, on
//     his turn only; a worker never;
//   - routines (routines.go): a Bot's pi only, its own Bot's; a worker never,
//     as Hermes keeps cronjob_manage from its sub-agents;
//   - the browser's: a worker and a Bot's pi (issue #536, step 4), each in its
//     own tab (agent.key: a worker's its Dispatch's, a pi's its chat's);
//   - node_exec, node_process, node_process_kill (nodes.go, issue #543): a
//     worker and a Bot's pi.
//
// The phone's, which both kinds get too, are listed on their own route
// (device.go): a node of the release before step 4 lists that route beside
// this one, and would list them twice were they here. The browser's,
// gateway's and memory's older routes stay for tools servers of an earlier
// release.

// toolList is the definitions of the tools an agent gets, in the shape of
// tools/list: the memory's, show_artifact, routines, the gateway's,
// Superset's and the browser's. A browser that cannot list its tools takes none of the others
// away.
func (s *Server) toolList(ctx context.Context, a agent) []agenttool.BrowserTool {
	out := []agenttool.BrowserTool{}

	if t, ok := memoryTool(a); ok {
		out = append(out, t)
	}

	if t, ok := artifactTool(a); ok {
		out = append(out, t)
	}

	if t, ok := routineTool(a); ok {
		out = append(out, t)
	}

	out = append(out, s.gatewayToolList(ctx, a)...)
	out = append(out, s.supersetToolList(ctx, a)...)
	out = append(out, s.whatsappToolList(ctx, a)...)

	if s.hasNodeTools(a) {
		out = append(out, nodeToolList()...)
	}

	if !s.hasBrowser(a) {
		return out
	}

	tools, err := s.browser.Tools(ctx)
	if err != nil {
		s.logger.WarnContext(ctx, "the browser's tools could not be listed",
			slog.String("event", "agentapi.browser_tools_failed"), slog.String("error", err.Error()))
	}

	for _, t := range tools {
		out = append(out, agenttool.BrowserTool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
	}

	return out
}

// hasBrowser says whether the agent gets the browser's tools: a worker or a
// Bot's pi, the agents with a tools server, when this control plane has a
// browser; as OpenMausBot mounts its browser server into every engine.
func (s *Server) hasBrowser(a agent) bool {
	return s.browser != nil && (a.dispatchID != "" || a.chatID != "" && a.bot != "")
}

// listTools lists the agent's tools (PathTools).
func (s *Server) listTools(w http.ResponseWriter, r *http.Request) {
	a, err := s.authenticate(r)
	if err != nil {
		refuse(w, http.StatusUnauthorized, err.Error())

		return
	}

	listed, err := s.narrowed(r.Context(), a, s.toolList(r.Context(), a))
	if err != nil {
		s.fail(w, r, err)

		return
	}

	answer(w, agenttool.BrowserToolList{Data: listed})
}

// callTool runs one of the agent's tools (PathToolsCall); a tool it was not
// given is no tool of its.
func (s *Server) callTool(w http.ResponseWriter, r *http.Request) {
	a, err := s.authenticate(r)
	if err != nil {
		refuse(w, http.StatusUnauthorized, err.Error())

		return
	}

	var asked agenttool.BrowserCall
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, agenttool.MaxCallBytes)).Decode(&asked); err != nil ||
		asked.Tool == "" {
		refuse(w, http.StatusBadRequest, "name the tool and give its arguments as a JSON object")

		return
	}

	if s.switchedOff(w, r, a, asked.Tool) {
		return
	}

	if slices.Contains(supersetTools, asked.Tool) {
		// The Connections are read again at each call: one taken back since the list is refused.
		labels, err := s.supersetLabels(r.Context(), a)
		if err != nil {
			s.fail(w, r, err)

			return
		}

		if len(labels) == 0 {
			refuse(w, http.StatusNotFound, "this agent has no tool "+asked.Tool)

			return
		}

		s.supersetCall(w, r, a, labels, asked)

		return
	}

	if slices.Contains(whatsappTools, asked.Tool) {
		// Read again at each call: the owner's turn may have ended since the list (issue #618).
		labels, err := s.whatsappLabels(r.Context(), a)
		if err != nil {
			s.fail(w, r, err)

			return
		}

		if len(labels) == 0 {
			refuse(w, http.StatusNotFound, "this agent has no tool "+asked.Tool)

			return
		}

		s.whatsappCall(w, r, a, labels, asked)

		return
	}

	switch {
	case asked.Tool == agenttool.ToolMemory && memoryTargets(a) != nil:
		s.memoryCall(w, r, a, asked)
	case slices.Contains(nodeTools, asked.Tool) && s.hasNodeTools(a):
		s.nodeCall(w, r, a, asked)
	case asked.Tool == agenttool.ToolShowArtifact && showsArtifacts(a):
		s.artifactCall(w, r, a, asked)
	case asked.Tool == agenttool.ToolRoutines && makesRoutines(a):
		s.routineCall(w, r, a, asked)
	case slices.Contains(s.gatewayTools(r.Context(), a), asked.Tool):
		s.gatewayCall(w, r, a, asked)
	case s.hasBrowser(a):
		// Any other tool of a worker or a Bot's pi is the browser's, which knows which it has.
		s.runBrowser(w, r, a, asked)
	default:
		refuse(w, http.StatusNotFound, "this agent has no tool "+asked.Tool)
	}
}

// The switches of a profile or a Bot (issue #536, step 7; #492, row 10):
// what an agent kind gets, narrowed by the agent's `tools` and `tools_off`,
// on every route that lists or calls a tool, the older per-family ones too,
// as OpenMausBot filters both an MCP server's discovery and its calls, with
// server-side checks behind them (docs/tool-selection.md), and OpenClaw
// applies its allow and deny lists to what a run may call
// (docs/gateway/config-tools/tool-policy.md). They are read at each request:
// a worker's from its Dispatch's profile version, a Bot's pi's from its Bot
// now, so a Bot's tool turned off is refused at the pi's next call. Other
// agents (the owner's token, the coordinator's, a Routine's script) have no
// MCP and no switches.

// switchesOf are the agent's tool switches.
func (s *Server) switchesOf(ctx context.Context, a agent) (agenttool.ToolSwitches, error) {
	switch {
	case a.dispatchID != "":
		return s.store.ToolSwitchesOfDispatch(ctx, a.dispatchID)
	case a.chatID != "" && a.bot != "":
		return a.botSwitches, nil
	default:
		return agenttool.ToolSwitches{}, nil
	}
}

// narrowed is the tools listed less those the agent's switches take away.
func (s *Server) narrowed(ctx context.Context, a agent, tools []agenttool.BrowserTool,
) ([]agenttool.BrowserTool, error) {
	switches, err := s.switchesOf(ctx, a)
	if err != nil {
		return nil, err
	}

	return slices.DeleteFunc(tools, func(t agenttool.BrowserTool) bool { return !switches.Allows(t.Name) }), nil
}

// switchedOff refuses a call of a tool the agent's switches take away, with
// the answer a tool it was never given gets, and says so.
func (s *Server) switchedOff(w http.ResponseWriter, r *http.Request, a agent, tool string) bool {
	switches, err := s.switchesOf(r.Context(), a)
	if err != nil {
		s.fail(w, r, err)

		return true
	}

	if switches.Allows(tool) {
		return false
	}

	s.logger.InfoContext(r.Context(), "an agent called a tool that is switched off",
		slog.String("event", "agentapi.tool_off"), slog.String("agent", a.name), slog.String("tool", tool))
	refuse(w, http.StatusNotFound, "this agent has no tool "+tool)

	return true
}
