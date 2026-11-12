package browser

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// listed is agent-browser's tools/list as the engine reads it: every offered
// tool, each with an argument of its own and arguments an agent may not set.
func listed() []Tool {
	tools := make([]Tool, 0, len(offered)+1)
	for _, name := range offered {
		tools = append(tools, Tool{
			Name: name, Description: "does " + name,
			InputSchema: json.RawMessage(`{"type":"object","properties":{"url":{"type":"string"},` +
				`"selector":{"type":"string"},"timeoutMs":{"type":"integer"},"session":{"type":"string"},` +
				`"extraArgs":{"type":"array"},"path":{"type":"string"},"screenshotDir":{"type":"string"}}}`),
		})
	}

	return append(tools, Tool{Name: "agent_browser_close", InputSchema: json.RawMessage(`{"type":"object"}`)})
}

func properties(t *testing.T, tool Tool) map[string]json.RawMessage {
	t.Helper()

	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}

	require.NoError(t, json.Unmarshal(tool.InputSchema, &schema))

	return schema.Properties
}

// An agent is not shown the arguments it may not set, nor a tool that is not
// offered.
func TestCatalogOffersToolsWithoutWithheldArguments(t *testing.T) {
	t.Parallel()

	c, err := newCatalog(listed())
	require.NoError(t, err)
	require.Len(t, c.tools, len(offered))

	for _, tool := range c.tools {
		assert.NotEqual(t, "agent_browser_close", tool.Name)

		props := properties(t, tool)
		assert.Contains(t, props, "selector")
		assert.Contains(t, props, "timeoutMs")
		assert.NotContains(t, props, "session")
		assert.NotContains(t, props, "extraArgs")
	}

	shot := properties(t, c.tools[indexOf(t, c, "agent_browser_screenshot")])
	assert.NotContains(t, shot, "path", "where a screenshot is written is the engine's to say")
	assert.NotContains(t, shot, "screenshotDir")
	assert.Contains(t, properties(t, c.tools[indexOf(t, c, "agent_browser_click")]), "path",
		"an argument is withheld from the tool it is a file's path for, not from every tool")
}

func indexOf(t *testing.T, c catalog, name string) int {
	t.Helper()

	for i, tool := range c.tools {
		if tool.Name == name {
			return i
		}
	}

	require.Fail(t, "no tool "+name)

	return -1
}

// A pinned agent-browser that no longer has an offered tool is found when
// the engine starts, not when an agent calls the tool.
func TestCatalogNeedsEveryOfferedTool(t *testing.T) {
	t.Parallel()

	_, err := newCatalog(listed()[1:])
	require.ErrorContains(t, err, offered[0])
}

// A call keeps the tool's own arguments and loses the rest, whether the
// schema listed them or not: agent-browser takes them on every tool.
func TestArgumentsDropWhatAnAgentMayNotSet(t *testing.T) {
	t.Parallel()

	c, err := newCatalog(listed())
	require.NoError(t, err)

	got, err := c.arguments("agent_browser_click", json.RawMessage(
		`{"selector":"#go","timeoutMs":500,"session":"host","extraArgs":["--profile","/x"],"restore":true,"idleTimeout":"0"}`))
	require.NoError(t, err)
	assert.JSONEq(t, `{"selector":"#go","timeoutMs":500}`, string(got))

	got, err = c.arguments("agent_browser_get_url", nil)
	require.NoError(t, err)
	assert.JSONEq(t, `{}`, string(got))

	_, err = c.arguments("agent_browser_close", json.RawMessage(`{}`))
	require.ErrorIs(t, err, ErrNoSuchTool)

	_, err = c.arguments("agent_browser_click", json.RawMessage(`["#go"]`))
	require.ErrorIs(t, err, ErrBadArguments)
}

// An agent opens web pages and the blank page; a file on the engine's
// machine, a script and a missing address are refused.
func TestOpenTakesOnlyAWebAddress(t *testing.T) {
	t.Parallel()

	c, err := newCatalog(listed())
	require.NoError(t, err)

	opens := map[string]string{
		"https://example.com/a?b=c": "https://example.com/a?b=c",
		"http://localhost:5173":     "http://localhost:5173",
		"example.com/docs":          "https://example.com/docs",
		" about:blank ":             "about:blank",
	}

	for given, want := range opens {
		raw, err := json.Marshal(map[string]string{"url": given})
		require.NoError(t, err)

		got, err := c.arguments(toolOpen, raw)
		require.NoError(t, err, given)
		assert.JSONEq(t, `{"url":"`+want+`"}`, string(got))
	}

	for _, given := range []string{
		"file:///etc/passwd", "javascript:alert(1)", "data:text/html,x", "chrome://settings", "view-source:x", "",
	} {
		raw, err := json.Marshal(map[string]string{"url": given})
		require.NoError(t, err)

		_, err = c.arguments(toolOpen, raw)
		require.ErrorIs(t, err, ErrBadArguments, given)
	}

	_, err = c.arguments(toolOpen, json.RawMessage(`{}`))
	require.ErrorIs(t, err, ErrBadArguments)
}
