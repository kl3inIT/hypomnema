package agentapi_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/channelstore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/memorystore"
)

// Scenario: one memory tool for a Bot's pi and a worker (issues #536, #589),
// Anthropic's memory tool over a directory. Each finds it on the control
// plane's one list. A Bot's pi works in its own Bot's /memories, a worker in
// its agent's: MEMORY.md is the memory the CLI shows, USER.md the one about
// the owner both share, and a note one kind keeps is not in the other's
// directory. A change's result says how full the memory is; a path outside
// /memories and a change over a limit are refused in Anthropic's words and
// change nothing. A Lark group's chat, and agents with no MCP (the
// coordinator token), are not given the tool. The log names the agent, never
// what it kept.
func TestBotsAndWorkersKeepMemoriesWithOneTool(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	_, coordinator, err := w.svc.AddToken(ctx, "pi", bearertoken.Coordinator)
	require.NoError(t, err)

	chat, err := w.svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	pi, err := w.svc.AddBotToken(ctx, "pi", chat.ID, orchestration.DefaultBotID)
	require.NoError(t, err)

	worker := w.workerOf("n1", "")

	piTools := w.tools(pi)
	require.Contains(t, toolNames(piTools), "memory")
	assert.Contains(t, string(piTools[0].InputSchema),
		`"enum":["view","create","str_replace","insert","delete","rename"]`)
	assert.Contains(t, piTools[0].Description, "/memories, yours, the same in every chat")

	workerTools := w.tools(worker)
	require.Contains(t, toolNames(workerTools), "memory")
	assert.Contains(t, workerTools[0].Description, "/memories, the agent you run as")

	_, text, isError := w.use(pi, "memory", `{"command":"view","path":"/memories"}`)
	assert.False(t, isError, text)
	assert.Equal(t, "Here're the files and directories up to 2 levels deep in /memories, excluding hidden items "+
		"and node_modules:\n0B\t/memories\n0B\t/memories/MEMORY.md\n0B\t/memories/USER.md", text)

	_, text, isError = w.use(pi, "memory",
		`{"command":"insert","path":"/memories/MEMORY.md","insert_line":0,"insert_text":"Lan is the designer."}`)
	assert.False(t, isError, text)
	assert.Equal(t, "The file /memories/MEMORY.md has been edited.\n"+
		"MEMORY.md: 20 of 2,200 characters (0%).\nNotes: 0 of 200 files (0%).", text)

	bot, err := w.svc.GetBot(ctx, orchestration.DefaultBotID)
	require.NoError(t, err)
	assert.Equal(t, "Lan is the designer.", bot.Memory, "the Bot's own memory, as `hypomnema memory show` shows it")

	_, text, isError = w.use(pi, "memory",
		`{"command":"create","path":"/memories/vmap/dashboard.md","file_text":"DAU counts guests.\nTotal: 3,586."}`)
	assert.False(t, isError, text)
	assert.Contains(t, text, "File created successfully at: /memories/vmap/dashboard.md\n")
	assert.Contains(t, text, "Notes: 1 of 200 files (0%).")

	_, text, _ = w.use(pi, "memory", `{"command":"view","path":"/memories/vmap/dashboard.md","view_range":[2,-1]}`)
	assert.Equal(t, "Here's the content of /memories/vmap/dashboard.md with line numbers:\n     2\tTotal: 3,586.", text)

	_, text, _ = w.use(worker, "memory", `{"command":"view","path":"/memories"}`)
	assert.NotContains(t, text, "dashboard", "a worker's agent has its own directory")

	_, text, isError = w.use(worker, "memory",
		`{"command":"insert","path":"/memories/USER.md","insert_line":0,"insert_text":"The owner writes in Vietnamese."}`)
	assert.False(t, isError, text)
	assert.Contains(t, text, "USER.md: 31 of 1,375 characters (2%).")

	_, text, isError = w.use(pi, "memory",
		`{"command":"str_replace","path":"/memories/USER.md","old_str":"Vietnamese.","new_str":"Vietnamese, short."}`)
	assert.False(t, isError, text)
	assert.Contains(t, text, "The memory file has been edited.")

	owner, err := w.svc.Memories().OwnerMemory(ctx)
	require.NoError(t, err)
	assert.Equal(t, "The owner writes in Vietnamese, short.", owner.Text, "one memory about the owner for both kinds")

	for _, refused := range []struct{ args, says string }{
		{`{"command":"view","path":"/etc/passwd"}`, "is not under /memories"},
		{`{"command":"view","path":"/memories/../etc"}`, "is not a name"},
		{`{"command":"view","path":"/memories/%2e%2e"}`, "has a % or a backslash"},
		{`{"command":"create","path":"/memories/a/b/c.md","file_text":"x"}`, "more than 2 levels"},
		{`{"command":"create","path":"/memories/MEMORY.md","file_text":"x"}`,
			"Error: File /memories/MEMORY.md already exists"},
		{`{"command":"delete","path":"/memories/MEMORY.md"}`, "cannot be deleted"},
		{`{"command":"str_replace","path":"/memories/MEMORY.md","old_str":"Linh"}`, "did not appear verbatim"},
		{`{"command":"create","path":"/memories/big.md","file_text":"` + strings.Repeat("x", 9000) + `"}`,
			"over a note's limit of 8,192"},
		{`{"command":"insert","path":"/memories/MEMORY.md","insert_line":1,"insert_text":"` +
			strings.Repeat("x", 2200) + `"}`, "over its limit of 2,200; nothing was changed"},
		{`{"command":"fly"}`, "unknown command"},
	} {
		_, text, isError = w.use(pi, "memory", refused.args)
		assert.True(t, isError, refused.args)
		assert.Contains(t, text, refused.says, refused.args)
	}

	bot, err = w.svc.GetBot(ctx, orchestration.DefaultBotID)
	require.NoError(t, err)
	assert.Equal(t, "Lan is the designer.", bot.Memory, "a refused change changes nothing")

	_, text, isError = w.use(pi, "memory",
		`{"command":"rename","old_path":"/memories/vmap","new_path":"/memories/maps"}`)
	assert.False(t, isError, text)

	notes, err := w.svc.Memories().Notes(ctx, memorystore.BotDir(orchestration.DefaultBotID))
	require.NoError(t, err)
	require.Len(t, notes, 1)
	assert.Equal(t, "/memories/maps/dashboard.md", notes[0].Path, "a directory moves with its notes")

	_, text, isError = w.use(pi, "memory", `{"command":"delete","path":"/memories/maps"}`)
	assert.False(t, isError, text)
	assert.Contains(t, text, "Successfully deleted /memories/maps")

	// A Lark group's chat has no memory tool: its prompt leaves the memory out (issue #246).
	vmap, err := w.svc.PutBot(ctx, orchestration.Bot{
		Name:      "vmap",
		AgentSpec: orchestration.AgentSpec{Instructions: "You answer about VMAP."},
	})
	require.NoError(t, err)
	_, err = w.svc.Channels().SetChannel(ctx, channelstore.Channel{
		Name: "maps", AppID: "cli_m4p5", Secret: "lark-channel-maps", Bot: vmap.ID,
		Mode: channelstore.ChannelOwner, OwnerID: "ou_owner", Enabled: true,
	})
	require.NoError(t, err)
	group, err := w.svc.ChannelGroupChatFor(ctx, "maps", "oc_group", "Lark: Maps team")
	require.NoError(t, err)
	inGroup, err := w.svc.AddBotToken(ctx, "pi", group.ChatID, vmap.ID)
	require.NoError(t, err)

	assert.NotContains(t, toolNames(w.tools(inGroup)), "memory")
	status, _, _ := w.use(inGroup, "memory", `{"command":"view","path":"/memories"}`)
	assert.Equal(t, http.StatusNotFound, status)

	assert.NotContains(t, toolNames(w.tools(coordinator)), "memory", "the coordinator token has no MCP")

	status, _, _ = w.use(coordinator, "memory", `{"command":"view","path":"/memories"}`)
	assert.Equal(t, http.StatusNotFound, status)

	assert.Contains(t, w.logs.String(), "bot "+orchestration.DefaultBotID+" in chat "+chat.ID)
	assert.NotContains(t, w.logs.String(), "Lan is the designer")
	assert.NotContains(t, w.logs.String(), "Vietnamese")
	assert.NotContains(t, w.logs.String(), "DAU counts")
}
