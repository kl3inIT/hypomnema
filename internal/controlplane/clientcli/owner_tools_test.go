package clientcli

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/agentapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
)

func TestOwnerMCPListsToolsWithTheSavedLogin(t *testing.T) {
	t.Parallel()

	w := ownerToolsWorld(t)
	out, err := w.cmdIn(t, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`+"\n", "mcp")
	require.NoError(t, err)
	assert.Contains(t, out, `"name":"memory"`)
}

func TestOwnerToolsCLIListsAndCallsTools(t *testing.T) {
	t.Parallel()

	w := ownerToolsWorld(t)
	out, err := w.cmd(t, "tools", "list", "--json")
	require.NoError(t, err)
	assert.Contains(t, out, `"name":"memory"`)
	assert.Contains(t, out, `"inputSchema"`)

	file := filepath.Join(t.TempDir(), "insert.json")
	require.NoError(t, os.WriteFile(file, []byte(`{"command":"insert","path":"/memories/USER.md",`+
		`"insert_line":0,"insert_text":"Prefers short replies."}`), 0o600))
	_, err = w.cmd(t, "tools", "call", "memory", "--args-file", file)
	require.NoError(t, err)

	out, err = w.cmdIn(t, `{"command":"view","path":"/memories/USER.md"}`,
		"tools", "call", "memory", "--args-file", "-", "--json")
	require.NoError(t, err)
	assert.Contains(t, out, `"isError":false`)
	assert.Contains(t, out, "Prefers short replies.")

	out, err = w.cmdIn(t, `{"command":"view","path":"/memories/MEMORY.md"}`,
		"tools", "call", "memory", "--args-file", "-", "--json")
	require.Error(t, err)
	assert.Contains(t, out, `"isError":true`)
	_, err = w.cmdIn(t, `{} {}`, "tools", "call", "memory", "--args-file", "-")
	require.ErrorIs(t, err, ErrUsage)
}

func ownerToolsWorld(t *testing.T) *world {
	t.Helper()

	w := newWorld(t)
	w.login(t, bearertoken.Owner)
	mux := http.NewServeMux()
	agentapi.New(w.svc, nil, slog.New(slog.DiscardHandler)).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cfg, err := LoadConfig(w.home)
	require.NoError(t, err)
	cfg.URL = srv.URL
	require.NoError(t, saveConfig(w.home, cfg))

	return w
}
