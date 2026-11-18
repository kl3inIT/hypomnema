package clientcli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
)

// Scenario: `accounts import` is this package's, while the control plane's own
// `accounts` commands stay its own (issue #828).
func TestOnlyAccountsImportIsAClientCommand(t *testing.T) {
	t.Parallel()

	assert.True(t, Handles([]string{"accounts", "import", "claude"}))
	assert.False(t, Handles([]string{"accounts", "list"}), "the control plane's")
	assert.False(t, Handles([]string{"accounts", "add", "claude", "x", "--token", "-"}))
	assert.True(t, Handles([]string{"runs", "list"}))
	assert.True(t, Handles([]string{"runs", "nothing"}), "a client word still answers with its usage")
	assert.False(t, Handles([]string{"serve"}))
}

// Scenario: the owner asks which Claude Code sessions this machine has. The
// one in $CLAUDE_CONFIG_DIR is listed with the e-mail Claude Code recorded,
// and no token is printed; a directory without a session is left out.
//
//nolint:paralleltest // it sets CLAUDE_CONFIG_DIR
func TestTheOwnerSeesTheClaudeSessionsOnThisMachine(t *testing.T) {
	w := newWorld(t)
	w.login(t, bearertoken.Owner)

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".credentials.json"),
		[]byte(`{"claudeAiOauth":{"accessToken":"sk-ant-oat-secret","refreshToken":"sk-ant-ort-secret"}}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".claude.json"),
		[]byte(`{"oauthAccount":{"emailAddress":"laptop@example.com"}}`), 0o600))
	t.Setenv("CLAUDE_CONFIG_DIR", dir)

	out, err := w.cmd(t, "accounts", "import", "claude", "--detect")
	require.NoError(t, err)
	assert.Contains(t, out, "laptop@example.com")
	assert.Contains(t, out, filepath.Join(dir, ".credentials.json"))
	assert.NotContains(t, out, "sk-ant-", "no token is printed")

	_, err = w.cmd(t, "accounts", "import", "claude", "--detect", "--email", "nobody@example.com")
	require.ErrorIs(t, err, ErrUsage)

	_, err = w.cmd(t, "accounts", "import", "codex", "--file", "x")
	require.ErrorIs(t, err, ErrUsage, "only Claude Code's file is read")

	_, err = w.cmd(t, "accounts", "import", "claude")
	require.ErrorIs(t, err, ErrUsage, "a file or --detect")
}
