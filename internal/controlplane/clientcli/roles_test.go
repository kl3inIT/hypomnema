package clientcli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
)

// Scenario (issue #1336): the role routing is a role's, not a profile's. `hypomnema roles routing` prints it, and
// `hypomnema profiles routing`, its spelling until 2026-11-28, still does for one release; the list of commands names
// the new spelling only.
func TestRolesRoutingAndItsOldSpellingFromTheCLI(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.login(t, bearertoken.Owner)

	out, err := w.cmd(t, "roles", "routing")
	require.NoError(t, err)
	assert.Contains(t, out, "implementer:")
	assert.Contains(t, out, "reviewer:")

	old, err := w.cmd(t, "profiles", "routing")
	require.NoError(t, err)
	assert.Equal(t, out, old, "the old spelling prints the same")

	_, err = w.cmd(t, "roles", "metrics")
	require.NoError(t, err)

	_, err = w.cmd(t, "profiles", "metrics")
	require.NoError(t, err)

	assert.Contains(t, Usage(), "hypomnema roles routing\n")
	assert.NotContains(t, Usage(), "hypomnema profiles routing")
	assert.Contains(t, Commands(), "roles")
}

// Scenario (issue #1336): the owner lists a Bot's saved settings and restores the first, which brings back its
// instructions; the memory stays. --instructions-file sets them, and --persona-file, its name until 2026-11-28, too.
func TestABotsVersionsFromTheCLI(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.login(t, bearertoken.Owner)

	_, err := w.cmdIn(t, "You answer about maps.\n", "bots", "set", "maps", "--instructions-file", "-")
	require.NoError(t, err)

	var changed openapi.Bot
	w.jsonCmd(t, &changed, "You answer about roads.\n", "bots", "set", "maps", "--persona-file", "-")
	assert.Equal(t, "You answer about roads.", changed.Instructions)

	out, err := w.cmd(t, "bots", "versions", "maps")
	require.NoError(t, err)
	assert.Contains(t, out, "v2\t")
	assert.Contains(t, out, "save by owner")

	var versions openapi.BotVersionList
	w.jsonCmd(t, &versions, "", "bots", "versions", "maps")
	require.Len(t, versions.Data, 2)

	var restored openapi.Bot
	w.jsonCmd(t, &restored, "", "bots", "restore", "maps", versions.Data[1].Id)
	assert.Equal(t, "You answer about maps.", restored.Instructions)

	out, err = w.cmd(t, "bots", "versions", "maps")
	require.NoError(t, err)
	assert.Contains(t, out, "restore by owner from "+versions.Data[1].Id)
}
