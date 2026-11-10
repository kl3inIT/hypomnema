package controlplane

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

//nolint:gosec // G101: a made-up token for the test
const claudeToken = "sk-ant-oat01-0123456789abcdef-never-printed"

// Scenario: the owner gives the control plane the token of `claude
// setup-token`. It comes on standard input and is never printed; it is sealed
// in the database, never in the clear; the node API gets it for that account
// and for no other; removing the account removes the token.
func TestOwnerGivesTheControlPlaneAClaudeToken(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "hypomnema.db")
	cfg := vaultConfig(t, path, "k1", keygen(t, "k1"))
	require.NoError(t, Migrate(t.Context(), cfg, io.Discard))

	run := func(stdin string, args ...string) (string, error) {
		var out bytes.Buffer
		err := Command(t.Context(), cfg, args, strings.NewReader(stdin), &out)

		return out.String(), err
	}

	_, err := run("two words\n", "accounts", "add", "claude", "personal", "--token", "-")
	require.ErrorIs(t, err, ErrUsage)

	_, err = run(claudeToken+"\n", "accounts", "add", "codex", "work", "--token", "-")
	require.ErrorIs(t, err, orchestration.ErrInvalid)

	out, err := run("  "+claudeToken+"\r\n", "accounts", "add", "claude", "personal", "--token", "-")
	require.NoError(t, err)
	assert.Contains(t, out, "(claude personal); its token is kept here")
	assert.NotContains(t, out, claudeToken)

	id := regexp.MustCompile(`acc_[a-z0-9]+`).FindString(out)
	require.NotEmpty(t, id)

	out, err = run("", "accounts", "list")
	require.NoError(t, err)
	assert.Contains(t, out, "claude\tpersonal\tactive")
	assert.NotContains(t, out, claudeToken)

	raw, err := os.ReadFile(path) //nolint:gosec // the test's own database
	require.NoError(t, err)
	assert.NotContains(t, string(raw), claudeToken, "the token is sealed")

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: path})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	svc := orchestration.New(db, platform.SystemClock{}, platform.NewID)
	tokens := accountTokens{
		svc: svc, vault: vault.New(db, platform.SystemClock{}, platform.NewID, cfg.VaultKeys, secretReferrers(svc)),
		logger: slog.New(slog.DiscardHandler),
	}

	got, ok := tokens.Token(t.Context(), id)
	require.True(t, ok)
	assert.Equal(t, claudeToken, got)

	onNode, err := svc.AddAccount(t.Context(), "claude", "on a node")
	require.NoError(t, err)

	_, ok = tokens.Token(t.Context(), onNode.ID)
	assert.False(t, ok, "an account a node holds has no token here")

	_, err = run("", "secrets", "rm", accountSecretName(id))
	require.Error(t, err, "the vault keeps a secret an account names")

	_, err = run("", "accounts", "rm", id)
	require.NoError(t, err)

	_, ok = tokens.Token(t.Context(), id)
	assert.False(t, ok)

	// A file on the server works as standard input does.
	file := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(file, []byte(claudeToken+"\n"), 0o600))

	_, err = run("", "accounts", "add", "claude", "second", "--token", file)
	require.NoError(t, err)
}
