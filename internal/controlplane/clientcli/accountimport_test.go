package clientcli

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
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

// Scenario: a control plane behind a proxy that answers 307 to another server
// does not get the CLI to resend what it posted there: Go replays the body of
// a 307, which for an import is the credentials (issue #828).
func TestTheCLIDoesNotFollowARedirectToAnotherServer(t *testing.T) {
	t.Parallel()

	var reached atomic.Int32

	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(elsewhere.Close)

	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/api/v1/account-imports", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(proxy.Close)

	client := &http.Client{CheckRedirect: sameOrigin}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, proxy.URL+"/api/v1/account-imports",
		strings.NewReader(`{"credentials":"sk-ant-ort-secret"}`))
	require.NoError(t, err)

	resp, err := client.Do(req)
	if resp != nil {
		_ = resp.Body.Close()
	}

	require.Error(t, err)
	assert.Contains(t, err.Error(), "another server")
	assert.Zero(t, reached.Load(), "nothing reached the other server")
}
