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
)

// Scenario: the owner sets the gateway up on the server: declares a model
// served by a provider that exists, makes a key for pi limited to it, and
// later removes both. The key is printed once and never listed.
func TestOwnerSetsUpTheGateway(t *testing.T) {
	t.Parallel()

	cfg := migratedConfig(t)

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
	assert.Equal(t, "gpt-sol\tchat\trouter:cx/gpt-6.1-sol\n", out)

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

	// A gateway key is not a client API token, and the client API's command
	// cannot make one.
	_, err = command(t, cfg, "tokens", "add", "web", "--kind", "gateway")
	require.ErrorIs(t, err, orchestration.ErrInvalid)

	out, err = command(t, cfg, "gateway", "keys", "revoke", id)
	require.NoError(t, err)
	assert.Contains(t, out, "revoked")

	out, err = command(t, cfg, "gateway", "keys", "list")
	require.NoError(t, err)
	assert.Contains(t, out, "\tpi\trevoked ")

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
