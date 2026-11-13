package clientcli

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
)

// Scenario: the owner makes a Bot from the laptop with a persona from
// standard input, and later changes only its effort: the persona stays. pi,
// which holds a coordinator's token, reads what it is given about the Bot
// when it starts, writes the Bot's memory and sees it in the prompt of its
// next start; it can neither change nor remove a Bot. The owner reads the
// memory and removes the Bot once its memory is empty.
func TestBotsAndTheirMemoryFromTheCLI(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.login(t, bearertoken.Owner)

	var bots openapi.BotList
	w.jsonCmd(t, &bots, "", "bots", "list")
	require.Len(t, bots.Data, 1)
	assert.Equal(t, "coordinator", bots.Data[0].Name)

	out, err := w.cmdIn(t, "You answer questions about VMAP.\n", "bots", "set", "vmap", "--persona-file", "-")
	require.NoError(t, err)
	assert.Contains(t, out, "vmap\tpi's default model\tmemory 0/2200")

	var vmap openapi.Bot
	w.jsonCmd(t, &vmap, "", "bots", "set", "vmap", "--effort", "high")
	assert.Equal(t, "You answer questions about VMAP.", vmap.Persona, "what the flags leave out stays")
	assert.Equal(t, "high", *vmap.Effort)

	pi := &world{svc: w.svc, url: w.url, home: filepath.Join(t.TempDir(), "coordinator")}
	pi.login(t, bearertoken.Coordinator)

	out, err = pi.cmd(t, "bots", "prompt", "vmap")
	require.NoError(t, err)
	assert.Contains(t, out, "You answer questions about VMAP.\n\n## Who is speaking\n",
		"a Bot is told who may instruct it, after its persona")
	assert.Contains(t, out, "do not look for another way to do it.\n\n## About the owner")
	assert.Contains(t, out, "You are the bot `vmap`.")
	assert.Contains(t, out, "Your memory is empty.")

	_, err = pi.cmd(t, "memory", "add", "Linh leads the maps team.")
	require.ErrorContains(t, err, "name the memory", "outside pi, the bot is named")

	out, err = pi.cmd(t, "memory", "add", "--bot", "vmap", "Linh leads the maps team.")
	require.NoError(t, err)
	assert.Contains(t, out, "memory of vmap: 25 of 2200 characters\n- Linh leads the maps team.")

	_, err = pi.cmd(t, "memory", "replace", "--bot", "vmap", "Linh", "Linh leads the maps team; Minh the routing.")
	require.NoError(t, err)

	_, err = pi.cmd(t, "memory", "remove", "--bot", "vmap", "nobody")
	require.ErrorContains(t, err, "no entry has")

	out, err = pi.cmd(t, "bots", "prompt", "vmap")
	require.NoError(t, err)
	assert.Contains(t, out, "Your memory now:\n\n- Linh leads the maps team; Minh the routing.\n")

	out, err = pi.cmd(t, "bots", "prompt", "--group", "vmap")
	require.NoError(t, err)
	assert.Contains(t, out, "You answer questions about VMAP.\n\n## Conversation")
	assert.Contains(t, out, "Only that opening label is added by Hypomnema")
	assert.Contains(t, out, "## Who is speaking", "a group's chat is told who may instruct the Bot")
	assert.NotContains(t, out, "Linh", "a group's chat is not given the memory")

	_, err = pi.cmd(t, "bots", "set", "vmap", "--effort", "low")
	require.ErrorContains(t, err, "forbidden")

	_, err = pi.cmd(t, "bots", "rm", "vmap")
	require.ErrorContains(t, err, "forbidden")

	out, err = w.cmd(t, "bots", "show", "vmap")
	require.NoError(t, err)
	assert.Contains(t, out, "memory:\n- Linh leads the maps team; Minh the routing.")

	_, err = w.cmd(t, "bots", "rm", "vmap")
	require.ErrorContains(t, err, "has a memory")

	// The owner edits the memory by hand: the lines he gives are the memory.
	out, err = w.cmdIn(t, "- Linh leads maps.\n\nMinh leads routing.\nLinh leads maps.\n",
		"memory", "set", "--bot", "vmap", "-")
	require.NoError(t, err)
	assert.Contains(t, out, "- Linh leads maps.\n- Minh leads routing.\n")

	_, err = pi.cmdIn(t, "x\n", "memory", "set", "--bot", "vmap", "-")
	require.ErrorContains(t, err, "forbidden", "a Bot changes its memory an entry at a time")

	_, err = w.cmdIn(t, "", "memory", "set", "--bot", "vmap", "-")
	require.NoError(t, err)

	out, err = w.cmd(t, "bots", "rm", "vmap")
	require.NoError(t, err)
	assert.Contains(t, out, "bot vmap is removed")

	_, err = w.cmd(t, "bots", "rm", "coordinator")
	require.ErrorContains(t, err, "always there")
}

// Scenario: the owner limits what a Bot may use from the terminal (issue
// #169): no Connection and one worker profile. The Bot is told in its prompt
// which profiles it gives work to, and the Bot coordinator that it manages
// Bots and profiles. A flag left out keeps a list; --all-workers lifts it.
func TestABotsListsFromTheCLI(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.login(t, bearertoken.Owner)

	out, err := w.cmd(t, "bots", "set", "vmap", "--connections", "", "--workers", "reviewer")
	require.NoError(t, err)
	assert.Contains(t, out, "connections: none\nworkers: reviewer\n")

	out, err = w.cmd(t, "bots", "prompt", "vmap")
	require.NoError(t, err)
	assert.Contains(t, out, "## Workers\n\nYou give work only to these worker profiles: reviewer.")
	assert.NotContains(t, out, "## Bots and profiles", "only the Bot coordinator manages them")

	out, err = w.cmd(t, "bots", "set", "vmap", "--effort", "high")
	require.NoError(t, err)
	assert.Contains(t, out, "connections: none\nworkers: reviewer\n", "what the flags leave out stays")

	out, err = w.cmd(t, "bots", "set", "vmap", "--all-workers=false")
	require.NoError(t, err)
	assert.Contains(t, out, "workers: reviewer\n", "=false lifts nothing")

	out, err = w.cmd(t, "bots", "set", "vmap", "--all-workers")
	require.NoError(t, err)
	assert.Contains(t, out, "workers: every profile\n")

	out, err = w.cmd(t, "bots", "prompt", "vmap")
	require.NoError(t, err)
	assert.NotContains(t, out, "## Workers")

	out, err = w.cmd(t, "bots", "prompt", "coordinator")
	require.NoError(t, err)
	assert.Contains(t, out, "## Bots and profiles")
}
