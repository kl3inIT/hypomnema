package clientapi

import (
	"context"
	"slices"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

func (s *Server) WithToolUnavailability(report func(context.Context, string) map[string]string) *Server {
	s.toolUnavailability = report

	return s
}

func (s *Server) toolAccess(ctx context.Context, bot *orchestration.Bot, runtime string,
	switches agenttool.ToolSwitches,
) (*openapi.ToolAccess, error) {
	connections, err := s.svc.ListConnections(ctx)
	if err != nil {
		return nil, err
	}

	var unavailable map[string]string
	if s.toolUnavailability != nil {
		unavailable = s.toolUnavailability(ctx, runtime)
	}

	out := &openapi.ToolAccess{Families: []openapi.ToolFamilyAccess{}, Skills: []openapi.SkillAccess{}}
	for _, family := range agenttool.ToolFamilies {
		access := openapi.ToolFamilyAccess{Name: family.Name, Description: family.Description}
		for _, name := range family.Tools {
			item := toolAccessItem(name, family.Name, bot, switches, connections, unavailable)
			access.Tools = append(access.Tools, item)

			// A family's state is its own tools': the phone is not on because the browser's family gives the video
			// tools both name (issue #1189).
			if agenttool.FamilyOf(name) != family.Name {
				continue
			}

			if access.State == "" || stateRank(item.State) > stateRank(access.State) {
				access.State, access.Reason, access.Source = item.State, item.Reason, item.Source
			}
		}

		out.Families = append(out.Families, access)
	}

	capabilities, err := s.svc.SkillCapabilities(ctx, bot, switches)
	if err != nil {
		return nil, err
	}

	skills, err := s.svc.ListSkills(ctx)
	if err != nil {
		return nil, err
	}

	for _, skill := range skills {
		status := openapi.SkillAccess{Name: skill.Name}
		var missing []string

		for _, req := range skill.Latest.Requires {
			if capabilities[req] {
				continue
			}

			switch {
			case req == "device:android" && !switches.AllowsFamily("phone"):
				missing = append(missing, "Phone tools are off")
			case req == "device:android":
				missing = append(missing, "No node holds a phone")
			case strings.HasPrefix(req, "integration:"):
				missing = append(missing, "No "+connectionTitle(strings.TrimPrefix(req, "integration:"))+" Connection")
			default:
				missing = append(missing, "Requires "+req)
			}
		}

		status.Missing, status.Reason = len(missing) != 0, strings.Join(missing, "; ")
		out.Skills = append(out.Skills, status)
	}

	return out, nil
}

func toolAccessItem(name, family string, bot *orchestration.Bot, switches agenttool.ToolSwitches,
	connections []orchestration.Connection, unavailable map[string]string,
) openapi.ToolAccessItem {
	allowed, source, reason := switches.Decision(name)
	item := openapi.ToolAccessItem{Name: name, State: "off", Source: openapi.ToolAccessSource(source), Reason: reason}
	if !allowed {
		return item
	}

	item.State = "available"
	if bot == nil && (family == "routines" || family == "whatsapp" || family == "zalo" || family == "telegram") {
		item.State, item.Reason = "unavailable", "Workers do not get this tool"

		return item
	}

	if bot == nil && family == "artifacts" {
		item.State, item.Reason = "on", "Requires a Run linked to a chat"
	}

	if family == "superset" || family == "whatsapp" || family == "zalo" || family == "telegram" {
		item.Source = "connection"
		title := connectionTitle(family)
		if !slices.ContainsFunc(connections, func(conn orchestration.Connection) bool {
			return conn.Integration == family && (bot == nil || bot.MayUse(conn))
		}) {
			item.State, item.Reason = "unavailable", "No "+title+" Connection"

			return item
		}

		if bot == nil {
			item.State, item.Reason = "on", "Requires a "+title+" Connection on the Run"
		} else {
			item.Reason = title + " Connection allowed"
		}
	}

	if reason := unavailable[name]; reason != "" {
		item.State, item.Reason = "unavailable", reason
	}

	return item
}

func connectionTitle(integration string) string {
	switch integration {
	case "whatsapp":
		return "WhatsApp"
	case "zalo":
		return "Zalo"
	case "telegram":
		return "Telegram"
	case "superset":
		return "Superset"
	default:
		return integration
	}
}

func stateRank(state openapi.ToolAccessState) int {
	switch state {
	case "available":
		return 3
	case "on":
		return 2
	case "unavailable":
		return 1
	default:
		return 0
	}
}
