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
	assert.Contains(t, out, "You answer questions about VMAP.\n\n## Memory")
	assert.Contains(t, out, "You are the bot `vmap`.")
	assert.Contains(t, out, "Your memory is empty.")

	_, err = pi.cmd(t, "memory", "add", "Linh leads the maps team.")
	require.ErrorContains(t, err, "name the bot", "outside pi, the bot is named")

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

	_, err = pi.cmd(t, "bots", "set", "vmap", "--effort", "low")
	require.ErrorContains(t, err, "forbidden")

	_, err = pi.cmd(t, "bots", "rm", "vmap")
	require.ErrorContains(t, err, "forbidden")

	out, err = w.cmd(t, "bots", "show", "vmap")
	require.NoError(t, err)
	assert.Contains(t, out, "memory:\n- Linh leads the maps team; Minh the routing.")

	_, err = w.cmd(t, "bots", "rm", "vmap")
	require.ErrorContains(t, err, "has a memory")

	_, err = w.cmd(t, "memory", "remove", "--bot", "vmap", "Linh")
	require.NoError(t, err)

	out, err = w.cmd(t, "bots", "rm", "vmap")
	require.NoError(t, err)
	assert.Contains(t, out, "bot vmap is removed")

	_, err = w.cmd(t, "bots", "rm", "coordinator")
	require.ErrorContains(t, err, "always there")
}
