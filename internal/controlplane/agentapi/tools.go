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
//     superset_api (superset.go): a worker given a Superset Connection by its
//     Run, a Bot's pi one switched on for chats and on its Bot's list;
//   - routines (routines.go): a Bot's pi only, its own Bot's; a worker never,
//     as Hermes keeps cronjob_manage from its sub-agents;
//   - the browser's: a worker and a Bot's pi (issue #536, step 4), each in its
//     own tab (agent.key: a worker's its Dispatch's, a pi's its chat's).
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

	answer(w, agenttool.BrowserToolList{Data: s.toolList(r.Context(), a)})
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

	switch {
	case asked.Tool == agenttool.ToolMemory && memoryTargets(a) != nil:
		s.memoryCall(w, r, a, asked)
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
