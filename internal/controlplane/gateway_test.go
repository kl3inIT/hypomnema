package controlplane

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

// Scenario: the owner sets the gateway up on the server: declares a model
// served by a provider that exists, makes a key for pi limited to it, and
// later edits, reads and revokes the key. Its value is never listed.
func TestOwnerSetsUpTheGateway(t *testing.T) {
	t.Parallel()

	cfg := vaultConfig(t, sqlitetest.Migrated(t, orchestration.Migrate), "k1", keygen(t, "k1"))

	db, err := sqlite.Open(t.Context(), cfg.Database)
	require.NoError(t, err)

	_, err = orchestration.New(db, platform.SystemClock{}, platform.NewID).ApplyProvider(t.Context(), "",
		"name: router\nkind: openai_compatible\nbase_url: https://router.example/v1\n"+
			"api_key_ref: vault:router\nmodels: [cx/gpt-6.1-sol]\n")
	require.NoError(t, err)
	require.NoError(t, db.Close())

	file := filepath.Join(t.TempDir(), "gpt-sol.yaml")
	require.NoError(t, os.WriteFile(file,
		[]byte("name: gpt-sol\ntargets:\n  - provider: router\n    model: cx/gpt-6.1-sol\n"), 0o600))

	out, err := command(t, cfg, "gateway", "models", "apply", file)
	require.NoError(t, err)
	assert.Contains(t, out, "model gpt-sol (chat) served from router:cx/gpt-6.1-sol")

	out, err = command(t, cfg, "gateway", "models", "list")
	require.NoError(t, err)
	assert.Contains(t, out, "\ngpt-sol\tchat\trouter:cx/gpt-6.1-sol\n")
	assert.Contains(t, out, "claude-subscription/claude-opus-5-5\tchat\tclaude-subscription:claude-opus-5-5\n",
		"beside every subscription's models, served with no row (issue #549)")
	assert.NotRegexp(t, `(?m)^claude-opus-5-5\t`, out, "a fresh install seeds no gateway model")

	out, err = command(t, cfg, "gateway", "keys", "add", "pi", "--models", "gpt-sol")
	require.NoError(t, err)
	assert.Contains(t, out, "for gpt-sol")

	secret := regexp.MustCompile(`(?m)^hyp_gateway_v1_\S+$`).FindString(out)
	require.NotEmpty(t, secret)

	id := regexp.MustCompile(`gwk_\w+`).FindString(out)

	out, err = command(t, cfg, "gateway", "keys", "list")
	require.NoError(t, err)
	assert.Contains(t, out, id+"\tpi\tactive\tgpt-sol")
	assert.NotContains(t, out, secret)

	out, err = command(t, cfg, "gateway", "keys", "set", id, "--name", "pi-tool", "--models", "*", "--endpoints", "none")
	require.NoError(t, err)
	assert.Contains(t, out, "key pi-tool ("+id+") for every model; chat only")
	assert.NotContains(t, out, secret)

	out, err = command(t, cfg, "gateway", "keys", "show-value", id)
	require.NoError(t, err)
	assert.Equal(t, secret+"\n", out)

	// A gateway key is not a client API token, and the client API's command
	// cannot make one.
	_, err = command(t, cfg, "tokens", "add", "web", "--kind", "gateway")
	require.ErrorIs(t, err, orchestration.ErrInvalid)

	out, err = command(t, cfg, "gateway", "keys", "revoke", id)
	require.NoError(t, err)
	assert.Contains(t, out, "revoked")

	out, err = command(t, cfg, "gateway", "keys", "list")
	require.NoError(t, err)
	assert.Contains(t, out, "\tpi-tool\trevoked ")

	_, err = command(t, cfg, "gateway", "models", "rm", "gpt-sol")
	require.NoError(t, err)

	_, err = command(t, cfg, "gateway", "models", "rm", "gpt-sol")
	require.ErrorIs(t, err, modelgateway.ErrNotFound)

	for _, bad := range [][]string{
		{"gateway"}, {"gateway", "models"}, {"gateway", "models", "apply"}, {"gateway", "keys", "add"},
		{"gateway", "keys", "add", "x", "extra"}, {"gateway", "teleport", "now"},
	} {
		_, err := command(t, cfg, bad...)
		require.ErrorIs(t, err, ErrUsage, bad)
	}
}

// Scenario: the owner lists the Antigravity rewrites (empty at first), sets two from a file, lists them, then
// clears them (issue #591). A rule with no text to find is refused.
func TestOwnerSetsAntigravityRewrites(t *testing.T) {
	t.Parallel()

	cfg := migratedConfig(t)

	out, err := command(t, cfg, "gateway", "antigravity-rewrites", "list")
	require.NoError(t, err)
	assert.Contains(t, out, "no Antigravity rewrites")

	file := filepath.Join(t.TempDir(), "rewrites.json")
	require.NoError(t, os.WriteFile(file,
		[]byte(`[{"find":"You are Hermes Agent.","with":"You are a coding assistant."},{"find":" Be brief.","with":""}]`),
		0o600))

	out, err = command(t, cfg, "gateway", "antigravity-rewrites", "set", file)
	require.NoError(t, err)
	assert.Contains(t, out, "2 Antigravity rewrite(s) set")

	out, err = command(t, cfg, "gateway", "antigravity-rewrites", "list")
	require.NoError(t, err)
	assert.Contains(t, out, `1	"You are Hermes Agent."	"You are a coding assistant."`)
	assert.Contains(t, out, `2	" Be brief."	""`)

	bad := filepath.Join(t.TempDir(), "bad.json")
	require.NoError(t, os.WriteFile(bad, []byte(`[{"find":"  ","with":"x"}]`), 0o600))
	_, err = command(t, cfg, "gateway", "antigravity-rewrites", "set", bad)
	require.ErrorIs(t, err, modelgateway.ErrInvalid)

	empty := filepath.Join(t.TempDir(), "empty.json")
	require.NoError(t, os.WriteFile(empty, []byte(`[]`), 0o600))
	out, err = command(t, cfg, "gateway", "antigravity-rewrites", "set", empty)
	require.NoError(t, err)
	assert.Contains(t, out, "0 Antigravity rewrite(s) set")
}
