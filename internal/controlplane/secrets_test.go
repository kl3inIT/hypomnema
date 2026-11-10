package controlplane

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

const routerKey = "sk-live-0123456789abcdef-never-printed"

// vaultConfig is a migrated database whose environment holds the root keys
// that `secrets keygen` printed.
func vaultConfig(t *testing.T, path, active string, entries ...string) Config {
	t.Helper()

	cfg, err := LoadConfig(func(name string) string {
		return map[string]string{
			"HYPOMNEMA_DATABASE_PATH":       path,
			"HYPOMNEMA_VAULT_KEYS":          strings.Join(entries, ","),
			"HYPOMNEMA_VAULT_ACTIVE_KEY_ID": active,
		}[name]
	})
	require.NoError(t, err)

	return cfg
}

func keygen(t *testing.T, id string) string {
	t.Helper()

	var out bytes.Buffer

	require.NoError(t, Command(t.Context(), Config{}, []string{"secrets", "keygen", id}, strings.NewReader(""), &out))

	entry, _, _ := strings.Cut(out.String(), "\n")
	require.True(t, strings.HasPrefix(entry, id+":"), out.String())

	return entry
}

// Scenario: the owner sets the vault up and keeps a provider's key in it:
// makes a root key, adds the key from a file, lists it, rotates it from
// standard input, replaces the root key, and can delete the secret only once
// no provider names it. The value is never printed.
func TestOwnerKeepsAProviderKeyInTheVault(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "hypomnema.db")
	k1 := keygen(t, "k1")
	cfg := vaultConfig(t, path, "k1", k1)
	require.NoError(t, Migrate(t.Context(), cfg, io.Discard))

	file := filepath.Join(t.TempDir(), "router.key")
	require.NoError(t, os.WriteFile(file, []byte(routerKey+"\n"), 0o600))

	var printed strings.Builder

	run := func(cfg Config, stdin string, args ...string) (string, error) {
		var out bytes.Buffer
		err := Command(t.Context(), cfg, args, strings.NewReader(stdin), &out)
		printed.WriteString(out.String())

		if err != nil {
			printed.WriteString(err.Error())
		}

		return out.String(), err
	}

	out, err := run(cfg, "", "secrets", "add", "router", "--from-file", file, "--meta", "plan=team")
	require.NoError(t, err)
	assert.Contains(t, out, "refer to it as vault:router")

	_, err = run(cfg, "", "secrets", "add", "router", "--from-file", file)
	require.ErrorIs(t, err, vault.ErrConflict)

	_, err = run(cfg, "", "secrets", "add", "router")
	require.ErrorIs(t, err, ErrUsage)

	out, err = run(cfg, "", "secrets", "list")
	require.NoError(t, err)
	assert.Contains(t, out, "router\tgeneric\tv1\tkey k1\t")
	assert.Contains(t, out, "plan=team")

	_, err = run(cfg, "sk-live-second-value\n", "secrets", "rotate", "router", "--from-file", "-")
	require.NoError(t, err)

	// The owner replaces the root key: both keys configured, the new one active.
	both := vaultConfig(t, path, "k2", k1, keygen(t, "k2"))

	out, err = run(both, "", "secrets", "rewrap")
	require.NoError(t, err)
	assert.Contains(t, out, "2 versions rewrapped")

	out, err = run(both, "", "secrets", "list")
	require.NoError(t, err)
	assert.Contains(t, out, "router\tgeneric\tv2\tkey k2\t")

	// What the control plane would send to the provider is the rotated value,
	// without the line break the file ended with.
	db, err := sqlite.Open(t.Context(), both.Database)
	require.NoError(t, err)

	defer db.Close() //nolint:errcheck // a test's second handle

	svc := orchestration.New(db, platform.SystemClock{}, platform.NewID)

	p, err := vault.New(db, platform.SystemClock{}, platform.NewID, both.VaultKeys, nil).Open(t.Context(), "router")
	require.NoError(t, err)
	assert.Equal(t, "sk-live-second-value", p[vault.KeyValue])

	_, err = svc.ApplyProvider(t.Context(), "",
		"name: router\nkind: openai_compatible\nbase_url: https://router.example/v1\n"+
			"api_key_ref: vault:router\nmodels: [m1]\n")
	require.NoError(t, err)

	_, err = run(both, "", "secrets", "rm", "router")
	require.ErrorIs(t, err, vault.ErrInUse)
	require.ErrorContains(t, err, "provider router")

	_, err = svc.ApplyProvider(t.Context(), "",
		"name: router\nkind: openai_compatible\nbase_url: https://router.example/v1\n"+
			"api_key_ref: ROUTER_KEY\nmodels: [m1]\n")
	require.NoError(t, err)

	_, err = run(both, "", "secrets", "rm", "router")
	require.NoError(t, err)

	assert.NotContains(t, printed.String(), routerKey)
	assert.NotContains(t, printed.String(), "sk-live-second-value")
}

// Scenario: a token set is added from a JSON file. A file that is not a JSON
// object of strings is refused without repeating its content.
func TestOwnerAddsATokenSet(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "hypomnema.db")
	cfg := vaultConfig(t, path, "k1", keygen(t, "k1"))
	require.NoError(t, Migrate(t.Context(), cfg, io.Discard))

	var out bytes.Buffer

	err := Command(t.Context(), cfg,
		[]string{"secrets", "add", "codex", "--kind", "oauth_token_set", "--from-file", "-"},
		strings.NewReader(`{"refresh_token":"`+routerKey+`","access_token":"at"}`), &out)
	require.NoError(t, err)
	assert.Contains(t, out.String(), "secret codex (")
	assert.Contains(t, out.String(), "oauth_token_set")

	err = Command(t.Context(), cfg,
		[]string{"secrets", "add", "broken", "--kind", "oauth_token_set", "--from-file", "-"},
		strings.NewReader(`{"refresh_token": `+routerKey), &out)
	require.ErrorIs(t, err, vault.ErrInvalid)
	assert.NotContains(t, err.Error(), routerKey)
}

// Scenario: the vault's settings are half set, or no keys are configured.
// The first is a configuration error at start; the second leaves every other
// command working and refuses to seal.
func TestVaultConfiguration(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "hypomnema.db")

	_, err := LoadConfig(func(name string) string {
		return map[string]string{"HYPOMNEMA_DATABASE_PATH": path, "HYPOMNEMA_VAULT_ACTIVE_KEY_ID": "k1"}[name]
	})
	require.ErrorContains(t, err, "set together")

	cfg := migratedConfig(t)

	_, err = command(t, cfg, "secrets", "add", "router", "--from-file", "-")
	require.ErrorIs(t, err, vault.ErrNotConfigured)

	out, err := command(t, cfg, "secrets", "list")
	require.NoError(t, err)
	assert.Empty(t, out)
}
