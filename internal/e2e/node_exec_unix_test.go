//go:build unix

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/agentapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/nodeapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/node/daemon"
	"github.com/kl3inIT/hypomnema/internal/node/nodeexec"
	"github.com/kl3inIT/hypomnema/internal/node/statedb"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

// Scenario: a worker runs commands on a node's machine (issue #543). Its
// call goes to the agent API with its Dispatch's key, the control plane
// sends it on the socket of the node named, and the node runs it with sh
// and posts the answer back: the exit code and the output. A command
// started in the background answers with its id, and the id reads its
// output once it has ended.
func TestAWorkersCommandRunsOnTheNodeItNames(t *testing.T) {
	t.Parallel()

	r := newExecRig(t)

	text, isErr := r.use(t, agenttool.ToolNodeExec, `{"node":"box","command":"echo from the box; exit 4"}`)
	assert.True(t, isErr)
	assert.Equal(t, "exit code 4\noutput:\nfrom the box\n", text)

	text, isErr = r.use(t, agenttool.ToolNodeExec, `{"node":"box","command":"sleep 1; echo later","background":true}`)
	require.False(t, isErr, text)

	var id string

	_, err := fmt.Sscanf(text, "running in the background, process_id %s", &id)
	require.NoError(t, err, text)

	text, isErr = r.use(t, agenttool.ToolNodeProcess, `{"node":"box","process_id":"`+id+`","wait_seconds":30}`)
	assert.False(t, isErr)
	assert.Equal(t, "exit code 0 (process_id "+id+")\noutput:\nlater\n", text)
}

// execRig is a control plane with a node named box that runs commands, and
// a worker's agent key.
type execRig struct {
	srv *httptest.Server
	key string
}

func newExecRig(t *testing.T) *execRig {
	t.Helper()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	logger := slog.New(slog.DiscardHandler)
	svc := orchestration.New(db, platform.SystemClock{}, platform.NewID)

	mux := http.NewServeMux()
	nodes := nodeapi.New(svc, logger)
	nodes.Register(mux)
	agentapi.New(svc, nil, logger).
		WithNodeExec(svc, nodes, func(context.Context, orchestration.Node) (string, error) { return "", nil }).
		Register(mux)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	_, token, err := svc.AddNode(t.Context(), "box", 1)
	require.NoError(t, err)

	state, err := statedb.Open(t.Context(), filepath.Join(t.TempDir(), "state.db"), time.Now)
	require.NoError(t, err)

	commands := nodeexec.New()

	d := daemon.New(daemon.Config{
		URL: srv.URL, Token: token, WorktreeRoot: "/srv/wt", Version: "test", Commands: commands,
	}, state, newFakeRunner(), logger)

	ctx, cancel := context.WithCancel(context.WithoutCancel(t.Context()))
	done := make(chan error, 1)

	go func() { done <- d.Run(ctx) }()

	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Logf("the node ended with: %v", err)
		}

		commands.Close()

		_ = state.Close()
	})

	require.Eventually(t, func() bool {
		list, err := svc.ListNodes(t.Context())

		return err == nil && len(list) == 1 && list[0].Connected && nodes.Online(list[0].ID)
	}, wait, 20*time.Millisecond)

	key, err := svc.AddAgentSession(t.Context(), "dsp_1", nil)
	require.NoError(t, err)

	return &execRig{srv: srv, key: key}
}

// use runs one of the worker's tools through the agent API: its text and
// whether it is an error.
func (r *execRig) use(t *testing.T, tool, arguments string) (string, bool) {
	t.Helper()

	body, err := json.Marshal(agenttool.BrowserCall{Tool: tool, Arguments: json.RawMessage(arguments)})
	require.NoError(t, err)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, r.srv.URL+agenttool.PathToolsCall,
		bytes.NewReader(body))
	require.NoError(t, err)

	req.Header.Set("Authorization", "Bearer "+r.key)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	defer resp.Body.Close() //nolint:errcheck // a test's response body

	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))

	var result struct {
		Content []struct{ Text string } `json:"content"`
		IsError bool                    `json:"isError"`
	}

	require.NoError(t, json.Unmarshal(raw, &result))
	require.NotEmpty(t, result.Content)

	return result.Content[0].Text, result.IsError
}
