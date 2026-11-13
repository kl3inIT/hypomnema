package clientcli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
)

// Scenario: the owner writes what the reviewer agent keeps and what every
// agent knows about him; a Bot adds a fact about him, which every Bot then
// finds in its prompt, but not in a group's chat. A Bot may not touch an
// agent's memory. A change says how full the memory is, and asks for room
// from 80% (issue #21).
func TestTheMemoriesOfAgentsAndTheOwnerFromTheCLI(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.login(t, bearertoken.Owner)

	out, err := w.cmd(t, "memory", "show", "--agent", "reviewer")
	require.NoError(t, err)
	assert.Equal(t, "memory of agent reviewer: 0 of 2200 characters\n", out)

	out, err = w.cmdIn(t, "Run make verify before a review.\n", "memory", "set", "--agent", "reviewer", "-")
	require.NoError(t, err)
	assert.Contains(t, out, "- Run make verify before a review.\n")

	_, err = w.cmd(t, "memory", "show", "--agent", "nobody")
	require.ErrorContains(t, err, "not found")

	_, err = w.cmd(t, "memory", "show", "--agent", "reviewer", "--owner")
	require.ErrorIs(t, err, ErrUsage)

	pi := &world{svc: w.svc, url: w.url, home: filepath.Join(t.TempDir(), "coordinator")}
	pi.login(t, bearertoken.Coordinator)

	out, err = pi.cmd(t, "memory", "add", "--owner", "The owner writes in Vietnamese.")
	require.NoError(t, err)
	assert.Equal(t, "memory about the owner: 31 of 1375 characters\n- The owner writes in Vietnamese.\n"+
		"memory: 31 of 1,375 characters (2%)\n", out)

	_, err = pi.cmd(t, "memory", "add", "--agent", "reviewer", "x")
	require.ErrorContains(t, err, "forbidden", "a worker changes its agent's memory through the agent API")

	out, err = pi.cmd(t, "bots", "prompt", "coordinator")
	require.NoError(t, err)
	assert.Contains(t, out, "## About the owner\n\n")
	assert.Contains(t, out, "31 of its 1375 characters are used.\n\n- The owner writes in Vietnamese.\n\n## Memory")

	out, err = pi.cmd(t, "bots", "prompt", "--group", "coordinator")
	require.NoError(t, err)
	assert.NotContains(t, out, "Vietnamese", "a group's chat is not given the memory about the owner")

	// Nearly full, the change asks to make room.
	out, err = pi.cmd(t, "memory", "add", "--owner", strings.Repeat("a", 1100))
	require.NoError(t, err)
	assert.Contains(t, out, "memory: 1,132 of 1,375 characters (82%). It is nearly full")
}
