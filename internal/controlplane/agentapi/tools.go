package agentapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"

	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
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
//   - whatsapp_* and zalo_* (messaging_tools.go, issues #638, #680): a
//     Bot's pi given the owner's WhatsApp or Zalo, on
//     his turn only; a worker never;
//   - routines (routines.go): a Bot's pi only, its own Bot's; a worker never,
//     as Hermes keeps cronjob_manage from its sub-agents;
//   - request_connection (connectionrequests.go, issue #471, part D): a Bot's
//     pi in its Bot's main chat only, while there is a Connection to ask for;
//   - inbox_* (inbox.go, issue #1004): a Bot's pi only, as routines;
//   - chat_search (chats.go, issue #805): a Bot's pi only, outside a Routine's read run;
//   - meeting_list, meeting_get (meetings.go, issue #1023): a Bot's pi, and the check script of its read Routine;
//   - the browser's: a worker and a Bot's pi (issue #536, step 4), each in its
//     own tab (agent.key: a worker's its Dispatch's, a pi's its chat's);
//   - node_exec, node_process, node_process_kill (nodes.go, issue #543): a
//     worker and a Bot's pi;
//   - video_make, video_get (videos.go, issue #818): a worker and a Bot's pi,
//     for the takes each filmed;
//   - browser_video_start, browser_video_say, browser_video_stop
//     (browsertakes.go, issue #1189): an agent with its own tab, beside the
//     browser's tools.
//
// The owner gets the installed tools and all Connections without switches.
// Tools that require a Run or chat (artifacts and Routines) still require one.
// The phone's tools are listed on their own route (device.go).

// toolList is the definitions of the tools an agent gets, in the shape of
// tools/list: the memory's, show_artifact, routines, the gateway's,
// Superset's and the browser's. A browser that cannot list its tools takes none of the others
// away.
func (s *Server) toolList(ctx context.Context, a agent, relayMentions bool) []agenttool.BrowserTool {
	out := []agenttool.BrowserTool{}

	if t, ok := memoryTool(a); ok && s.memoryOfferedNow(ctx, a) {
		out = append(out, t)
	}
	if a.readRun {
		out = append(out, inboxTools(a)...)
		out = append(out, s.meetingTools(a)...)
		out = append(out, s.supersetToolList(ctx, a)...)
		out = append(out, s.messagingToolList(ctx, a, relayMentions)...)
		return append(out, s.readAccountTools(ctx, a)...)
	}

	if t, ok := artifactTool(a); ok {
		out = append(out, t)
	}

	if t, ok := routineTool(a); ok {
		out = append(out, t)
	}

	if t, ok := chatSearchTool(a); ok {
		out = append(out, t)
	}

	if t, ok := s.requestConnectionTool(ctx, a); ok {
		out = append(out, t)
	}

	out = append(out, inboxTools(a)...)
	out = append(out, s.meetingTools(a)...)

	out = append(out, s.gatewayToolList(ctx, a)...)
	out = append(out, s.supersetToolList(ctx, a)...)
	out = append(out, s.messagingToolList(ctx, a, relayMentions)...)

	if s.hasNodeTools(a) {
		out = append(out, nodeToolList()...)
	}

	if s.hasVideoTools(a) {
		out = append(out, videoToolList()...)
	}

	if !s.hasBrowser(a) {
		return out
	}

	if s.hasBrowserTakes(a) {
		out = append(out, browserTakeToolList()...)
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

// hasBrowser says whether the caller gets its own tab when the control plane has a browser: every agent with tools,
// and the coordinator token and a Routine's script, each in a tab of its own (c-, r-), as the browser's own routes
// gave every agent but a read run before issue #1220 moved them here.
func (s *Server) hasBrowser(a agent) bool {
	return s.browser != nil && (a.hasAgentTools() || (a.coordinator && !a.readRun))
}

// listTools lists the agent's tools (PathTools).
func (s *Server) listTools(w http.ResponseWriter, r *http.Request) {
	a, err := s.authenticate(r)
	if err != nil {
		refuse(w, http.StatusUnauthorized, err.Error())

		return
	}

	w.Header().Add("Vary", agenttool.HeaderWhatsAppMentions)
	listed, err := s.narrowed(r.Context(), a, s.toolList(r.Context(), a, relaysWhatsAppMentions(r)))
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

	if m, ok := messagingServiceOf(asked.Tool); ok {
		// Read again at each call: one taken back since the list is refused; whose turn it is, messagingCall decides
		// (issues #618, #748).
		labels, err := s.messagingLabels(r.Context(), a, m)
		if err != nil {
			s.fail(w, r, err)

			return
		}

		if len(labels) == 0 {
			refuse(w, http.StatusNotFound, "this agent has no tool "+asked.Tool)

			return
		}

		s.messagingCall(w, r, a, m, labels, asked)

		return
	}

	switch {
	case asked.Tool == agenttool.ToolAccountRead && a.readLive && s.relay != nil:
		s.readAccount(w, r, a, asked)
	case asked.Tool == agenttool.ToolMemory && s.memoryOfferedNow(r.Context(), a):
		s.memoryCall(w, r, a, asked)
	case asked.Tool == agenttool.ToolMemory:
		// Not the browser's: an agent not given the memory's tool, such as a Lark group's chat, has none.
		refuse(w, http.StatusNotFound, "this agent has no tool "+asked.Tool)
	case slices.Contains(nodeTools, asked.Tool) && s.hasNodeTools(a):
		s.nodeCall(w, r, a, asked)
	case (asked.Tool == devicetool.VideoMake || asked.Tool == devicetool.VideoGet) && s.hasVideoTools(a):
		s.videoCall(w, r, a, asked)
	case slices.Contains(browserTakeTools, asked.Tool) && s.hasBrowserTakes(a):
		s.browserTakeCall(w, r, a, asked)
	case asked.Tool == agenttool.ToolShowArtifact && showsArtifacts(a):
		s.artifactCall(w, r, a, asked)
	case asked.Tool == agenttool.ToolRoutines && makesRoutines(a):
		s.routineCall(w, r, a, asked)
	case asked.Tool == agenttool.ToolChatSearch && searchesChats(a):
		s.chatSearchCall(w, r, a, asked)
	case asked.Tool == agenttool.ToolChatSearch:
		// Not the browser's: an agent that is no Bot's pi has none.
		refuse(w, http.StatusNotFound, "this agent has no tool "+asked.Tool)
	case asked.Tool == agenttool.ToolRequestConnection && asksConnections(a):
		s.requestConnectionCall(w, r, a, asked)
	case asked.Tool == agenttool.ToolRequestConnection:
		// Not the browser's: an agent that is no Bot's pi has none.
		refuse(w, http.StatusNotFound, "this agent has no tool "+asked.Tool)
	case isInboxTool(asked.Tool) && keepsInbox(a):
		s.inboxCall(w, r, a, asked)
	case slices.Contains(agenttool.MeetingTools, asked.Tool) && s.readsMeetings(a):
		s.meetingCall(w, r, asked)
	case slices.Contains(s.gatewayTools(r.Context(), a), asked.Tool):
		s.gatewayCall(w, r, a, asked)
	case s.hasBrowser(a):
		// The browser knows which other tools it has.
		s.runBrowser(w, r, a, asked)
	default:
		refuse(w, http.StatusNotFound, "this agent has no tool "+asked.Tool)
	}
}

// The switches of a profile or a Bot (issue #536, step 7; #492, row 10):
// what an agent kind gets, narrowed by the agent's `tools` and `tools_off`,
// on every route that lists or calls a tool, as OpenMausBot filters both an MCP server's discovery and its calls, with
// server-side checks behind them (docs/tool-selection.md), and OpenClaw
// applies its allow and deny lists to what a run may call
// (docs/gateway/config-tools/tool-policy.md). They are read at each request:
// a worker's from its Dispatch's profile version, a Bot's pi's from its Bot
// now, so a Bot's tool turned off is refused at the pi's next call. The
// owner has no switches. The coordinator and Routine scripts have no MCP.

// switchesOf are the agent's tool switches.
func (s *Server) switchesOf(ctx context.Context, a agent) (agenttool.ToolSwitches, error) {
	switch {
	case a.dispatchID != "":
		return s.store.ToolSwitchesOfDispatch(ctx, a.dispatchID)
	case a.bot != "":
		// A Bot's pi in its chat, or the check of its read Routine (#1023).
		return a.botSwitches, nil
	default:
		return agenttool.ToolSwitches{On: []string{"*"}}, nil
	}
}

// narrowed is the tools listed less those the agent's switches take away.
func (s *Server) narrowed(ctx context.Context, a agent, tools []agenttool.BrowserTool,
) ([]agenttool.BrowserTool, error) {
	switches, err := s.switchesOf(ctx, a)
	if err != nil {
		return nil, err
	}

	return slices.DeleteFunc(tools, func(t agenttool.BrowserTool) bool { return !a.allowsTool(switches, t.Name) }), nil
}

func (a agent) allowsTool(switches agenttool.ToolSwitches, tool string) bool {
	if a.readRun && !agenttool.ReadRunAllows(tool) {
		return false
	}

	if a.ownerReadOnly && slices.Contains(agenttool.OwnerSendTools, tool) {
		return false
	}

	return (tool == agenttool.ToolAccountRead && a.readLive) || switches.Allows(tool)
}

// switchedOff refuses a call of a tool the agent's switches take away, with
// the answer a tool it was never given gets, and says so.
func (s *Server) switchedOff(w http.ResponseWriter, r *http.Request, a agent, tool string) bool {
	if a.readRun && !agenttool.ReadRunAllows(tool) {
		answerTool(w, agenttool.ReadRunRefusal, true)

		return true
	}

	if a.ownerReadOnly && slices.Contains(agenttool.OwnerSendTools, tool) {
		answerTool(w, agenttool.OwnerReadOnlyRefusal, true)

		return true
	}

	switches, err := s.switchesOf(r.Context(), a)
	if err != nil {
		s.fail(w, r, err)

		return true
	}

	if a.allowsTool(switches, tool) {
		return false
	}

	s.logger.InfoContext(r.Context(), "an agent called a tool that is switched off",
		slog.String("event", "agentapi.tool_off"), slog.String("agent", a.name), slog.String("tool", tool))
	refuse(w, http.StatusNotFound, "this agent has no tool "+tool)

	return true
}
