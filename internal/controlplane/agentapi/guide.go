package agentapi

import (
	"context"
	"log/slog"
	"slices"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// The tools a "Tools" block names (issue #723, decisions 2.2 and 2.3;
// agenttool.ToolGuide): the names of the tools an agent of a kind has for the
// life of its prompt, after its switches. A worker's reach its node in its
// offer (nodeproto.Offer.Tools), a Bot's pi's through `hypomnema bots
// prompt` (the client API's GET /bots/{bot}/tools). Both come from here, so
// a worker and a Bot with the same families get the same block.
//
// Since issue #789 they are what the agent's tools/list gives, not only what
// every task or chat has: Superset's when the agent may use a Superset
// Connection (a worker's as its Dispatch's key says, a Bot's as its list
// does); WhatsApp's and Zalo's for a Bot whose list has the owner's account,
// which pi keeps listed between his turns (#748); the phone's while a node
// holds it. Then, past the switches, which name only the control plane's
// tools, the command-line tools the agent reaches accounts with through the
// service proxy, by their command: gh and glab only a Bot's, since a worker
// keeps its own (agenttool.Def.BotsOnly). Hermes and OpenClaw build their
// guidance from the tools the session has (agent/system_prompt.py:289-313;
// src/agents/system-prompt-tool-list.ts:20-92).

// anyChat stands for a chat when a Bot's tools are listed for none.
const anyChat = "*"

// WorkerTools are the names of the tools a Dispatch's worker gets, after
// its profile's switches, read with the Dispatch's own key (its agent
// session), as its tools server will be; none when they cannot be read.
func (s *Server) WorkerTools(ctx context.Context, dispatchID, key string) []string {
	a := agent{name: "dispatch " + dispatchID, dispatchID: dispatchID,
		may: func(orchestration.Connection) bool { return false }}

	if key != "" {
		session, err := s.store.AgentSessionOf(ctx, key)
		if err != nil {
			s.logger.WarnContext(ctx, "the agent session of a dispatch could not be read",
				slog.String("event", "agentapi.session_unknown"), slog.String("dispatch_id", dispatchID),
				slog.String("error", err.Error()))

			return nil
		}

		a.may = session.May
	}

	return s.standingTools(ctx, a)
}

// BotTools are the names of the tools a Bot's pi gets in any chat, after the
// Bot's switches: those of the Connections on its list, as pi lists them
// whoever's turn it is (agent.offered).
func (s *Server) BotTools(ctx context.Context, bot orchestration.Bot) []string {
	return s.standingTools(ctx, agent{
		name: "bot " + bot.ID, coordinator: true, chatID: anyChat, bot: bot.ID, botSwitches: bot.ToolSwitches(),
		may: bot.MayUse, offered: bot.MayUse, botRow: bot,
	})
}

func (s *Server) standingTools(ctx context.Context, a agent) []string {
	var names []string

	for _, t := range []func(agent) (agenttool.BrowserTool, bool){memoryTool, artifactTool, routineTool} {
		if tool, ok := t(a); ok {
			names = append(names, tool.Name)
		}
	}

	// request_connection while the Bot has a Connection to ask for: the block is the same for each of its chats,
	// and the tool's line says it is its main chat's (agenttool.ToolFamilies).
	if t, ok := s.requestConnectionTool(ctx, a); ok {
		names = append(names, t.Name)
	}

	names = append(names, s.gatewayTools(ctx, a)...)

	for _, t := range slices.Concat(s.supersetToolList(ctx, a), s.messagingToolList(ctx, a, false)) {
		names = append(names, t.Name)
	}

	if s.hasNodeTools(a) {
		names = append(names, nodeTools...)
	}

	if s.hasBrowser(a) {
		names = append(names, agenttool.BrowserTools...)
	}

	if s.hasBrowserTakes(a) {
		names = append(names, browserTakeTools...)
	}

	// The phone's, as its own route lists them: none while no node holds it.
	if s.device != nil {
		if tools, err := s.device.Tools(ctx); err == nil {
			for _, t := range tools {
				names = append(names, t.Name)
			}
		}
	}

	switches, err := s.switchesOf(ctx, a)
	if err != nil {
		s.logger.WarnContext(ctx, "an agent's tool switches could not be read",
			slog.String("event", "agentapi.switches_unknown"), slog.String("agent", a.name),
			slog.String("error", err.Error()))

		return nil
	}

	out := make([]string, 0, len(names))

	for _, name := range names {
		if switches.Allows(name) {
			out = append(out, name)
		}
	}

	return append(out, s.commandTools(ctx, a)...)
}

// commandTools are the commands of the command-line tools with a line in the
// "Tools" block that the agent reaches an account with through the service
// proxy.
func (s *Server) commandTools(ctx context.Context, a agent) []string {
	var out []string

	for _, def := range agenttool.Catalog() {
		if def.WhenToUse == "" || def.BotsOnly && a.dispatchID != "" {
			continue
		}

		allowed, err := s.allowedOf(ctx, a, def.Integration, def)
		if err != nil {
			s.logger.WarnContext(ctx, "the accounts of an agent's command could not be read",
				slog.String("event", "agentapi.command_accounts_failed"), slog.String("agent", a.name),
				slog.String("tool", def.Name), slog.String("error", err.Error()))

			continue
		}

		if len(allowed) > 0 {
			out = append(out, def.Name)
		}
	}

	return out
}
