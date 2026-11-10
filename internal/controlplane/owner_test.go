package controlplane

import (
	"bytes"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

func migratedConfig(t *testing.T) Config {
	t.Helper()

	cfg, err := LoadConfig(func(name string) string {
		return map[string]string{"HYPOMNEMA_DATABASE_PATH": filepath.Join(t.TempDir(), "hypomnema.db")}[name]
	})
	require.NoError(t, err)
	require.NoError(t, Migrate(t.Context(), cfg, io.Discard))

	return cfg
}

func command(t *testing.T, cfg Config, args ...string) (string, error) {
	t.Helper()

	var out bytes.Buffer
	err := Command(t.Context(), cfg, args, strings.NewReader(""), &out)

	return out.String(), err
}

// Scenario: the owner sets up a node and an account on the server. The
// node's token is printed once; listing shows the node offline until it
// connects.
func TestOwnerSetsUpANodeAndAnAccount(t *testing.T) {
	t.Parallel()

	cfg := migratedConfig(t)

	out, err := command(t, cfg, "nodes", "add", "zm", "--max", "3")
	require.NoError(t, err)
	assert.Contains(t, out, "up to 3 Dispatches")
	assert.Regexp(t, `(?m)^hyn_[A-Za-z0-9_-]+$`, out)

	out, err = command(t, cfg, "nodes", "list")
	require.NoError(t, err)
	assert.Contains(t, out, "\tzm\tactive\toffline\t0/3\t")

	out, err = command(t, cfg, "accounts", "add", "claude", "personal")
	require.NoError(t, err)
	assert.Contains(t, out, "(claude personal)")

	_, err = command(t, cfg, "teleport")
	require.ErrorIs(t, err, ErrUsage)
}

// Scenario: the owner makes a token for the laptop's coordinator, lists it,
// and revokes it. The secret is printed once and never listed.
func TestOwnerManagesClientTokens(t *testing.T) {
	t.Parallel()

	cfg := migratedConfig(t)

	out, err := command(t, cfg, "tokens", "add", "laptop", "--kind", "coordinator")
	require.NoError(t, err)
	assert.Contains(t, out, "token laptop (tok_")

	secret := regexp.MustCompile(`(?m)^hyp_coordinator_v1_\S+$`).FindString(out)
	require.NotEmpty(t, secret)

	id := regexp.MustCompile(`tok_\w+`).FindString(out)

	out, err = command(t, cfg, "tokens", "list")
	require.NoError(t, err)
	assert.Contains(t, out, id+"\tlaptop\tcoordinator\tactive")
	assert.NotContains(t, out, secret)

	_, err = command(t, cfg, "tokens", "add", "web", "--kind", "admin")
	require.ErrorIs(t, err, orchestration.ErrInvalid)

	_, err = command(t, cfg, "tokens", "add", "web", "extra")
	require.ErrorIs(t, err, ErrUsage)

	out, err = command(t, cfg, "tokens", "revoke", id)
	require.NoError(t, err)
	assert.Contains(t, out, "revoked")

	out, err = command(t, cfg, "tokens", "list")
	require.NoError(t, err)
	assert.Contains(t, out, "\tcoordinator\trevoked ")
}

// Scenario: the owner rotates a node's token and later removes the node. The
// new token is printed once and never listed; a removed node is not listed
// and its name is free.
func TestOwnerRotatesANodeTokenAndRemovesTheNode(t *testing.T) {
	t.Parallel()

	cfg := migratedConfig(t)

	_, err := command(t, cfg, "nodes", "add", "zm")
	require.NoError(t, err)

	out, err := command(t, cfg, "nodes", "token", "add", "zm", "rotation")
	require.NoError(t, err)

	secret := regexp.MustCompile(`(?m)^hyn_\S+$`).FindString(out)
	require.NotEmpty(t, secret)

	out, err = command(t, cfg, "nodes", "token", "list", "zm")
	require.NoError(t, err)
	assert.NotContains(t, out, secret)
	assert.Contains(t, out, "\tfirst\tactive\tnever used")

	first := regexp.MustCompile(`ntk_\w+`).FindString(out)

	_, err = command(t, cfg, "nodes", "token", "revoke", first)
	require.NoError(t, err)

	out, err = command(t, cfg, "nodes", "token", "list", "zm")
	require.NoError(t, err)
	assert.Contains(t, out, first+"\tfirst\trevoked ")
	assert.Contains(t, out, "\trotation\tactive")

	out, err = command(t, cfg, "nodes", "drain", "zm")
	require.NoError(t, err)
	assert.Contains(t, out, "drained")

	out, err = command(t, cfg, "nodes", "list")
	require.NoError(t, err)
	assert.Contains(t, out, "\tzm\tdrained\t")

	_, err = command(t, cfg, "nodes", "rm", "zm")
	require.NoError(t, err)

	out, err = command(t, cfg, "nodes", "list")
	require.NoError(t, err)
	assert.Empty(t, out)

	_, err = command(t, cfg, "nodes", "rm", "zm")
	require.ErrorIs(t, err, orchestration.ErrNotFound)

	_, err = command(t, cfg, "nodes", "add", "zm")
	require.NoError(t, err)
}
