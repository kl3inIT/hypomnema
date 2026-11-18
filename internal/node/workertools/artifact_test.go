package workertools

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/node/bridge"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// Scenario: a worker shows a folder of its worktree as an artifact (issue
// #536). Its tools server reads the folder in the worktree its runner named,
// whatever folder its CLI started it in, by the rules `hypomnema artifact
// show` reads by, and sends the files whole with the call; the answer is the
// control plane's, with what the folder left out. A page sent as html goes
// as it is; a path out of the worktree is refused there and reaches no
// control plane.
func TestAWorkerShowsAFolderOfItsWorktree(t *testing.T) {
	t.Parallel()

	cp := &controlPlane{result: `{"content":[{"type":"text","text":"Shown as artifact art_1 (revision 1)."}],` +
		`"isError":false}`}
	srv := httptest.NewServer(cp)
	t.Cleanup(srv.Close)

	worktree := t.TempDir()
	files := map[string]string{
		"dashboard/index.html":          "<h1>Sales</h1>",
		"dashboard/data.json":           `[{"region":"North"}]`,
		"dashboard/.env":                "TOKEN=x",
		"dashboard/node_modules/a.js":   "x",
		"dashboard/notes.docm":          "x",
		"dashboard/charts/regions.json": "[]",
	}

	for name, content := range files {
		path := filepath.Join(worktree, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	}

	dir := t.TempDir()
	require.NoError(t, bridge.SetAgent(dir, bridge.Agent{URL: srv.URL, Key: dispatchKey, Worktree: worktree}))

	c := newClientIn(t, dir)

	c.send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{
		"name": "show_artifact", "arguments": map[string]any{"title": "Sales", "path": "dashboard"},
	}})
	assert.Equal(t, "Shown as artifact art_1 (revision 1).\nLeft out .env (hidden), node_modules/ (packages), "+
		"notes.docm (not a kind of file a page loads).", text(t, c.read()))

	require.Len(t, cp.calls, 1)
	assert.Equal(t, "show_artifact", cp.calls[0].Tool)

	var sent agenttool.ArtifactCall
	require.NoError(t, json.Unmarshal(cp.calls[0].Arguments, &sent))
	assert.Equal(t, agenttool.ArtifactCall{Title: "Sales", Path: "dashboard", Files: []agenttool.ArtifactFile{
		{Path: "charts/regions.json", Data: []byte("[]")},
		{Path: "data.json", Data: []byte(`[{"region":"North"}]`)},
		{Path: "index.html", Data: []byte("<h1>Sales</h1>")},
	}}, sent)

	c.send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{
		"name": "show_artifact", "arguments": map[string]any{"id": "art_1", "html": "<h1>Sales</h1>"},
	}})
	assert.Equal(t, "Shown as artifact art_1 (revision 1).", text(t, c.read()))
	require.Len(t, cp.calls, 2)
	assert.JSONEq(t, `{"id":"art_1","html":"<h1>Sales</h1>"}`, string(cp.calls[1].Arguments))

	c.send(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": map[string]any{
		"name": "show_artifact", "arguments": map[string]any{"title": "Sales", "path": filepath.Dir(worktree)},
	}})
	assert.Contains(t, text(t, c.read()), "only a file or folder in your directory")
	assert.Len(t, cp.calls, 2, "nothing is sent")
}
