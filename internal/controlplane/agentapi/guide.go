package agentapi

import (
	"context"
	"log/slog"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// The tools a "Tools" block names (issue #723, decisions 2.2 and 2.3;
// agenttool.ToolGuide): the names of the tools an agent of a kind gets
// whatever its task or chat, after its switches. A worker's reach its node
// in its offer (nodeproto.Offer.Tools), a Bot's pi's through `hypomnema bots
// prompt` (the client API's GET /bots/{bot}/tools). Both come from here, so
// a worker and a Bot with the same families get the same block.
//
// What depends on the task or the chat is not named: Superset's tools,
// which follow the Connections a Run or a turn is given, WhatsApp's and
// Zalo's, which follow the owner's turn, and the phone's, which follow a
// node holding it. Their families say nothing in the block yet (their
// WhenToUse is empty, issue #789).

// anyChat stands for a chat when a Bot's tools are listed for none.
const anyChat = "*"

// WorkerTools are the names of the tools a Dispatch's worker gets, after
// its profile's switches; none when they cannot be read.
func (s *Server) WorkerTools(ctx context.Context, dispatchID string) []string {
	return s.standingTools(ctx, agent{name: "dispatch " + dispatchID, dispatchID: dispatchID})
}

// BotTools are the names of the tools a Bot's pi gets in any chat, after the
// Bot's switches.
func (s *Server) BotTools(ctx context.Context, bot orchestration.Bot) []string {
	return s.standingTools(ctx, agent{
		name: "bot " + bot.ID, coordinator: true, chatID: anyChat, bot: bot.ID, botSwitches: bot.ToolSwitches(),
	})
}

func (s *Server) standingTools(ctx context.Context, a agent) []string {
	var names []string

	for _, t := range []func(agent) (agenttool.BrowserTool, bool){memoryTool, artifactTool, routineTool} {
		if tool, ok := t(a); ok {
			names = append(names, tool.Name)
		}
	}

	names = append(names, s.gatewayTools(ctx, a)...)

	if s.hasNodeTools(a) {
		names = append(names, nodeTools...)
	}

	if s.hasBrowser(a) {
		names = append(names, agenttool.BrowserTools...)
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

	return out
}
