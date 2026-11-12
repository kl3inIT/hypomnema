package browser

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
)

// Tool is one browser tool as an agent is offered it: agent-browser's own
// name, description and input schema, less the arguments an agent may not
// set.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// Errors of a call an agent made wrongly.
var (
	// ErrNoSuchTool reports a tool that is not offered.
	ErrNoSuchTool = errors.New("no such browser tool")
	// ErrBadArguments reports arguments a tool was refused with.
	ErrBadArguments = errors.New("the browser tool's arguments were refused")
)

// offered are the tools an agent gets, out of agent-browser's `core` set
// (cli/src/mcp.rs, CORE_PROFILE_TOOLS). Each agent has one tab, which the
// engine opens and closes, so the tab tools and `close` are not offered;
// neither are flows, which are files on the engine's machine, nor
// `agent_browser_read`, which fetches an address outside the browser.
// OpenMausBot puts a list of its own in front of agent-browser's in the same
// way (server/browser/tools.ts).
var offered = []string{
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

const toolOpen = "agent_browser_open"

// withheld are the arguments every agent-browser tool accepts whether its
// schema lists them or not (cli/src/mcp.rs, SHARED_ARGUMENTS), less
// timeoutMs. They name another session, change how Chrome is launched, or
// read and write files beside the engine, so an agent sets none of them: the
// engine does.
var withheld = []string{
	"session", "namespace", "restore", "restoreSave", "restoreCheckUrl", "restoreCheckText", "restoreCheckFn",
	"allowedDomains", "capture", "caCert", "useSystemCa", "clearCaCert", "idleTimeout", "relaunch", "extraArgs",
}

// withheldOf are a tool's own arguments an agent may not set: where a
// screenshot is written on the engine's machine.
var withheldOf = map[string][]string{
	"agent_browser_screenshot": {"path", "screenshotDir"},
}

// catalog is the tools offered and the arguments each takes.
type catalog struct {
	tools []Tool
	takes map[string][]string
}

// newCatalog makes the catalog from agent-browser's answer to tools/list.
func newCatalog(listed []Tool) (catalog, error) {
	c := catalog{takes: map[string][]string{}}

	for _, name := range offered {
		at := slices.IndexFunc(listed, func(t Tool) bool { return t.Name == name })
		if at < 0 {
			return catalog{}, fmt.Errorf("agent-browser lists no tool %s", name)
		}

		tool, takes, err := trim(listed[at])
		if err != nil {
			return catalog{}, fmt.Errorf("tool %s: %w", name, err)
		}

		c.tools = append(c.tools, tool)
		c.takes[name] = takes
	}

	return c, nil
}

// trim takes the withheld arguments out of a tool's schema and returns the
// names of those left.
func trim(tool Tool) (Tool, []string, error) {
	var schema map[string]json.RawMessage
	if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
		return Tool{}, nil, fmt.Errorf("its input schema is not an object: %w", err)
	}

	properties := map[string]json.RawMessage{}
	if raw, ok := schema["properties"]; ok {
		if err := json.Unmarshal(raw, &properties); err != nil {
			return Tool{}, nil, fmt.Errorf("its properties are not an object: %w", err)
		}
	}

	for _, name := range slices.Concat(withheld, withheldOf[tool.Name]) {
		delete(properties, name)
	}

	takes := make([]string, 0, len(properties))
	for name := range properties {
		takes = append(takes, name)
	}

	slices.Sort(takes)

	raw, err := json.Marshal(properties)
	if err != nil {
		return Tool{}, nil, err
	}

	schema["properties"] = raw

	if tool.InputSchema, err = json.Marshal(schema); err != nil {
		return Tool{}, nil, err
	}

	return tool, takes, nil
}

// arguments returns a call's arguments with only what the tool takes, and an
// address checked.
func (c catalog) arguments(tool string, raw json.RawMessage) (json.RawMessage, error) {
	takes, ok := c.takes[tool]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNoSuchTool, tool)
	}

	given := map[string]json.RawMessage{}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &given); err != nil {
			return nil, fmt.Errorf("%w: they are not a JSON object", ErrBadArguments)
		}
	}

	kept := map[string]json.RawMessage{}

	for name, value := range given {
		if slices.Contains(takes, name) {
			kept[name] = value
		}
	}

	if tool == toolOpen {
		var address string
		if err := json.Unmarshal(kept["url"], &address); err != nil {
			return nil, fmt.Errorf("%w: open needs a url", ErrBadArguments)
		}

		address, err := webAddress(address)
		if err != nil {
			return nil, err
		}

		kept["url"], _ = json.Marshal(address) //nolint:errchkjson // A string always encodes.
	}

	out, err := json.Marshal(kept)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBadArguments, err)
	}

	return out, nil
}

// webAddress returns the address an agent may open: a web page, or the blank
// one. A bare host means https. Anything else, a file on the engine's machine
// first of all, is refused, as T3 Code refuses a preview's address
// (packages/contracts/src/preview.ts, PreviewInvalidUrlError).
func webAddress(address string) (string, error) {
	address = strings.TrimSpace(address)
	if address == "about:blank" {
		return address, nil
	}

	if !strings.Contains(address, "://") {
		address = "https://" + address
	}

	parsed, err := url.Parse(address)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", fmt.Errorf("%w: open takes an http(s) address or about:blank", ErrBadArguments)
	}

	return parsed.String(), nil
}
