package controlplane

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/subscription"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

//nolint:gosec // G101: a made-up token for the test
const claudeToken = "sk-ant-oat01-0123456789abcdef-never-printed"

// Scenario: the owner gives the control plane the token of `claude
// setup-token`. It comes on standard input and is never printed; it is sealed
// in the database, never in the clear; the model gateway serves the account
// with it (no node gets it, issue #381); removing the account removes the token.
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
	assert.Contains(t, out, "(claude personal); its token is kept here and the model gateway serves it")
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
	subs := newSubscriptions(Config{}, svc,
		vault.New(db, platform.SystemClock{}, platform.NewID, cfg.VaultKeys, secretReferrers(svc)),
		slog.New(slog.DiscardHandler))

	got, err := subs.Token(t.Context(), "claude", "")
	require.NoError(t, err)
	assert.Equal(t, id, got.AccountID)
	assert.Equal(t, claudeToken, got.AccessToken)

	onNode, err := svc.AddAccount(t.Context(), "codex", "on a node")
	require.NoError(t, err)

	// The owner turns an account off and on: placement passes a disabled one over.
	out, err = run("", "accounts", "disable", onNode.ID)
	require.NoError(t, err)
	assert.Contains(t, out, "disabled")

	out, err = run("", "accounts", "list")
	require.NoError(t, err)
	assert.Contains(t, out, "codex\ton a node\tdisabled")

	_, err = run("", "accounts", "enable", onNode.ID)
	require.NoError(t, err)

	_, err = run("", "accounts", "disable", "acc_nobody")
	require.ErrorIs(t, err, orchestration.ErrNotFound)

	_, err = run("", "secrets", "rm", accountSecretName(id))
	require.Error(t, err, "the vault keeps a secret an account names")

	_, err = run("", "accounts", "rm", id)
	require.NoError(t, err)

	_, err = subs.Token(t.Context(), "claude", "")
	require.ErrorIs(t, err, subscription.ErrNoAccount, "the gateway has no Claude account left")

	// A file on the server works as standard input does.
	file := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(file, []byte(claudeToken+"\n"), 0o600))

	_, err = run("", "accounts", "add", "claude", "second", "--token", file)
	require.NoError(t, err)
}

// Scenario: the owner adds a Claude account on the Models page by pasting the
// token of `claude setup-token` (issue #199). The account is there under the
// name given, the token is never answered and the model gateway serves it.
// A token that is not one line, a vendor with no token to paste, and a
// coordinator are refused, and nothing is kept for them.
func TestOwnerPastesAClaudeTokenOnTheModelsPage(t *testing.T) {
	t.Parallel()

	api := newVaultAPI(t)
	add := func(token string, body map[string]any) (int, map[string]any, string) {
		status, out, raw, _ := api.call(token, http.MethodPost, "/accounts", body)

		return status, out, raw
	}

	status, _, _ := add(api.coordinator, map[string]any{"runtime": "claude", "label": "kept", "token": claudeToken})
	assert.Equal(t, http.StatusForbidden, status)

	status, _, raw := add(api.owner, map[string]any{"runtime": "claude", "label": "kept", "token": "two words"})
	assert.Equal(t, http.StatusBadRequest, status, raw)
	assert.Contains(t, raw, "claude setup-token")

	status, _, _ = add(api.owner, map[string]any{"runtime": "codex", "label": "kept", "token": claudeToken})
	assert.Equal(t, http.StatusBadRequest, status)

	pasted := " " + claudeToken + "\n"
	status, out, raw := add(api.owner, map[string]any{"runtime": "claude", "label": "kept", "token": pasted})
	require.Equal(t, http.StatusCreated, status, raw)
	assert.Equal(t, "kept", out["label"])
	assert.Equal(t, "claude", out["runtime"])
	assert.NotContains(t, raw, claudeToken, "the token is never answered")

	accounts, err := api.svc.ListAccounts(t.Context())
	require.NoError(t, err)
	require.Len(t, accounts, 1, "nothing is kept for a refused token")

	got, err := newSubscriptions(Config{}, api.svc, api.tokens.vault, slog.New(slog.DiscardHandler)).
		Token(t.Context(), "claude", "")
	require.NoError(t, err)
	assert.Equal(t, out["id"], got.AccountID)
	assert.Equal(t, claudeToken, got.AccessToken)
}
