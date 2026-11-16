package agenttool

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
)

// Tool switches (issue #536, step 7; #492, row 10): a worker profile and a
// Bot narrow the control plane's tools their agents get with two lists beside
// their skills, `tools` (only these; empty, every tool the agent kind gets)
// and `tools_off` (never these), as OpenClaw's agent has tools.allow and
// tools.deny, deny winning (docs/gateway/config-tools/tool-policy.md), and
// OpenMausBot's bot an Allow and an Exclude list, Exclude winning
// (docs/tool-selection.md). An entry is a tool's name, or a family: a name's
// start and `*`, such as `agent_browser_*` or `superset_*`, as OpenClaw takes
// `*` wildcards and server globs (`outlook__*`) and OpenMausBot a whole
// server's tools (`mcp:mail:*`); a `*` elsewhere is refused, as OpenMausBot
// refuses partial wildcards (only a trailing one is taken here: inference).
// A name no tool has is refused when it is set, as a skill no one added is.
// The switches only narrow: a tool an agent kind is not given stays away
// whatever they name. They are Hypomnema's tools only: a CLI's own (Claude
// Code's Bash, Codex's shell) are not switched here.

// The gateway's web tools (issue #354), listed and called as generate_image is.
const (
	ToolWebSearch = "web_search"
	ToolWebFetch  = "web_fetch"
)

// Superset's tools (issue #536, step 3).
const (
	ToolSupersetDashboards = "superset_dashboards"
	ToolSupersetDashboard  = "superset_dashboard"
	ToolSupersetChartData  = "superset_chart_data"
	ToolSupersetAPI        = "superset_api"
)

// WhatsApp's tools (issue #638): the owner's own WhatsApp, read by a Bot on his turn.
const (
	ToolWhatsAppChats    = "whatsapp_chats"
	ToolWhatsAppMessages = "whatsapp_messages"
	ToolWhatsAppMedia    = "whatsapp_media"
	ToolWhatsAppHistory  = "whatsapp_history"
)

// BrowserTools are the browser's tools an agent is offered, out of
// agent-browser's `core` set (cli/src/mcp.rs, CORE_PROFILE_TOOLS). Each agent
// has one tab, which the engine opens and closes, so the tab tools and
// `close` are not offered; neither are flows, which are files on the engine's
// machine, nor `agent_browser_read`, which fetches an address outside the
// browser. OpenMausBot puts a list of its own in front of agent-browser's in
// the same way (server/browser/tools.ts).
var BrowserTools = []string{
	"agent_browser_open",
	"agent_browser_snapshot",
	"agent_browser_click",
	"agent_browser_fill",
	"agent_browser_type",
	"agent_browser_press",
	"agent_browser_check",
	"agent_browser_uncheck",
	"agent_browser_select",
	"agent_browser_scroll",
	"agent_browser_back",
	"agent_browser_forward",
	"agent_browser_reload",
	"agent_browser_wait_ms",
	"agent_browser_wait_for_selector",
	"agent_browser_wait_for_text",
	"agent_browser_wait_for_load",
	"agent_browser_screenshot",
	"agent_browser_get_text",
	"agent_browser_get_url",
	"agent_browser_get_title",
	"agent_browser_eval",
}

// ToolFamily is a group of the control plane's tools, as OpenClaw's tool
// groups (`group:web`, `group:memory`) are: what a settings screen switches as
// one, by Pattern, the entry that names them all.
type ToolFamily struct {
	Name        string
	Description string
	Pattern     string
	Tools       []string
}

// ToolFamilies are every tool of the control plane, by family. Which agent
// kind gets which is each tool's own definition (internal/controlplane/agentapi,
// tools.go).
var ToolFamilies = []ToolFamily{
	{Name: "memory", Description: "What the agent keeps across chats or Runs", Pattern: ToolMemory,
		Tools: []string{ToolMemory}},
	{Name: "artifacts", Description: "Show a page or a deck in the chat", Pattern: ToolShowArtifact,
		Tools: []string{ToolShowArtifact}},
	{Name: "routines", Description: "Make and run the Bot's Routines (a Bot only)", Pattern: ToolRoutines,
		Tools: []string{ToolRoutines}},
	{Name: "web", Description: "Search the web and read pages", Pattern: "web_*",
		Tools: []string{ToolWebSearch, ToolWebFetch}},
	{Name: "images", Description: "Make and edit images", Pattern: ToolGenerateImage,
		Tools: []string{ToolGenerateImage}},
	{Name: "superset", Description: "Superset's dashboards, charts and data, with a Superset Connection",
		Pattern: "superset_*",
		Tools:   []string{ToolSupersetDashboards, ToolSupersetDashboard, ToolSupersetChartData, ToolSupersetAPI}},
	{Name: "whatsapp", Description: "The owner's own WhatsApp, read on his turn, with a WhatsApp Connection",
		Pattern: "whatsapp_*",
		Tools: []string{ToolWhatsAppChats, ToolWhatsAppMessages, ToolWhatsAppMedia, ToolWhatsAppHistory,
			ToolWhatsAppSend}},
	{Name: "nodes", Description: "Run commands on the nodes' machines", Pattern: "node_*",
		Tools: []string{ToolNodeExec, ToolNodeProcess, ToolNodeProcessKill}},
	{Name: "browser", Description: "The browser every agent shares, in a tab of its own", Pattern: "agent_browser_*",
		Tools: BrowserTools},
	{Name: "phone", Description: "The phone a node holds", Pattern: devicetool.Prefix + "*",
		Tools: []string{devicetool.Screen, devicetool.Layout, devicetool.Tap, devicetool.Swipe, devicetool.Type,
			devicetool.Key, devicetool.Launch, devicetool.Logcat}},
}

// ToolNames are the names of every tool of the control plane.
func ToolNames() []string {
	var out []string
	for _, f := range ToolFamilies {
		out = append(out, f.Tools...)
	}

	return out
}

// ErrBadToolSwitch reports a switch that names no tool or names one twice.
var ErrBadToolSwitch = errors.New("not a tool switch")

// ToolSwitches are the two lists: On, only these when any is named; Off,
// never these.
type ToolSwitches struct {
	On  []string
	Off []string
}

// Allows says whether the switches let an agent have the tool named: Off
// names none of it, and On is empty or names it.
func (s ToolSwitches) Allows(tool string) bool {
	if slices.ContainsFunc(s.Off, func(entry string) bool { return matches(entry, tool) }) {
		return false
	}

	return len(s.On) == 0 || slices.ContainsFunc(s.On, func(entry string) bool { return matches(entry, tool) })
}

// matches says whether an entry, a name or a name's start and `*`, names the tool.
func matches(entry, tool string) bool {
	if start, family := strings.CutSuffix(entry, "*"); family {
		return strings.HasPrefix(tool, start)
	}

	return entry == tool
}

// CheckToolSwitches checks the two lists before they are stored: each entry
// a tool's name or a family that names at least one, named once, and not in
// both lists.
func CheckToolSwitches(on, off []string) error {
	seen := map[string]bool{}
	names := ToolNames()

	for _, entry := range slices.Concat(on, off) {
		if seen[entry] {
			return fmt.Errorf("%w: tool %q is named twice", ErrBadToolSwitch, entry)
		}

		seen[entry] = true

		if strings.Contains(strings.TrimSuffix(entry, "*"), "*") {
			return fmt.Errorf("%w: %q: a tool is named whole, or by its start and one * at the end", ErrBadToolSwitch,
				entry)
		}

		if !slices.ContainsFunc(names, func(tool string) bool { return matches(entry, tool) }) {
			patterns := make([]string, 0, len(ToolFamilies))
			for _, f := range ToolFamilies {
				patterns = append(patterns, f.Pattern)
			}

			return fmt.Errorf("%w: %q names none of Hypomnema's tools; they are %s", ErrBadToolSwitch, entry,
				strings.Join(patterns, ", "))
		}
	}

	return nil
}
