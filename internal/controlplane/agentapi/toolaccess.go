package agentapi

import (
	"context"
	"slices"

	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// ToolUnavailability reports the agent-kind and service limits independent of presets and Connections.
// An empty runtime describes a Bot. A worker's Run supplies its Connections separately.
func (s *Server) ToolUnavailability(ctx context.Context, runtime string) map[string]string {
	a := agent{chatID: anyChat, bot: anyChat}
	named := botTools
	if runtime != "" {
		a = agent{dispatchID: "profile"}
		named = toolsOf[runtime]
	}

	gateway := s.configuredGatewayTools(ctx, named)
	result := make(map[string]string)
	var phone []string

	if s.device != nil {
		if tools, err := s.device.Tools(ctx); err == nil {
			for _, tool := range tools {
				phone = append(phone, tool.Name)
			}
		}
	}

	for _, family := range agenttool.ToolFamilies {
		for _, name := range family.Tools {
			reason := "This service is not configured"
			switch family.Name {
			case "memory", "artifacts", "routines":
				continue
			case "superset":
				if s.relay != nil {
					continue
				}
			case "whatsapp", "zalo":
				if s.messaging != nil {
					continue
				}
			case "browser":
				if s.hasBrowser(a) {
					continue
				}
			case "nodes":
				if s.hasNodeTools(a) {
					continue
				}
			case "phone":
				if slices.Contains(phone, name) {
					continue
				}
				reason = "No node holds a phone"
			default:
				if slices.Contains(gateway, name) {
					continue
				}
				if !slices.Contains(named, name) {
					reason = "This runtime does not get this tool"
				}
			}

			result[name] = reason
		}
	}

	return result
}
