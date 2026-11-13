package e2e

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/nodeapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/node/daemon"
	"github.com/kl3inIT/hypomnema/internal/node/statedb"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
	"github.com/kl3inIT/hypomnema/internal/shared/workspacefs"
)

// Scenario: the owner reads a Task's worktree (issue #255). The control plane
// asks the node on its socket; the node reads the worktree and posts its answer
// back. A folder outside the node's worktree root is refused there, one that is
// gone says so, and a node that is not connected is not waited for.
func TestAWorktreeIsReadThroughItsNode(t *testing.T) {
	t.Parallel()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	logger := slog.New(slog.DiscardHandler)
	svc := orchestration.New(db, platform.SystemClock{}, platform.NewID)

	mux := http.NewServeMux()
	nodes := nodeapi.New(svc, logger)
	nodes.Register(mux)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	node, token, err := svc.AddNode(t.Context(), "node_a", 2)
	require.NoError(t, err)

	away, _, err := svc.AddNode(t.Context(), "node_b", 2)
	require.NoError(t, err)

	root := t.TempDir()
	worktree := filepath.Join(root, "run_1", "tsk_1")
	require.NoError(t, os.MkdirAll(filepath.Join(worktree, "src"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(worktree, "src", "health.go"), []byte("package api\n"), 0o600))

	state, err := statedb.Open(t.Context(), filepath.Join(t.TempDir(), "state.db"), time.Now)
	require.NoError(t, err)

	d := daemon.New(daemon.Config{URL: srv.URL, Token: token, WorktreeRoot: root, Version: "test"},
		state, newFakeRunner(), logger)

	ctx, cancel := context.WithCancel(context.WithoutCancel(t.Context()))
	done := make(chan error, 1)

	go func() { done <- d.Run(ctx) }()

	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Logf("the node ended with: %v", err)
		}

		_ = state.Close()
	})

	// The node is asked once its socket is up.
	require.Eventually(t, func() bool {
		_, err := nodes.Files(t.Context(), node.ID, worktree, workspacefs.Request{Op: workspacefs.OpList})

		return err == nil
	}, wait, 20*time.Millisecond)

	res, err := nodes.Files(t.Context(), node.ID, worktree, workspacefs.Request{Op: workspacefs.OpRead,
		Path: "src/health.go"})
	require.NoError(t, err)
	require.Nil(t, res.Error)
	assert.Equal(t, "package api\n", res.File.Content)

	// The owner saves over the text the file was read as (issue #297); a second save from that text is refused, as
	// the file changed; a text larger than a node's message is not sent.
	saved, err := nodes.Files(t.Context(), node.ID, worktree, workspacefs.Request{Op: workspacefs.OpWrite,
		Path: "src/health.go", Content: "package health\n", Expect: res.File.SHA256})
	require.NoError(t, err)
	require.Nil(t, saved.Error)

	written, err := os.ReadFile(filepath.Join(worktree, "src", "health.go")) //nolint:gosec // the test's folder
	require.NoError(t, err)
	assert.Equal(t, "package health\n", string(written))

	stale, err := nodes.Files(t.Context(), node.ID, worktree, workspacefs.Request{Op: workspacefs.OpWrite,
		Path: "src/health.go", Content: "package other\n", Expect: res.File.SHA256})
	require.NoError(t, err)
	require.NotNil(t, stale.Error)
	assert.Equal(t, workspacefs.CodeChanged, stale.Error.Code)

	big, err := nodes.Files(t.Context(), node.ID, worktree, workspacefs.Request{Op: workspacefs.OpWrite,
		Path: "big.txt", Content: strings.Repeat("x", workspacefs.MaxWrite)})
	require.NoError(t, err)
	require.NotNil(t, big.Error)
	assert.Equal(t, workspacefs.CodeTooLarge, big.Error.Code)

	res, err = nodes.Files(t.Context(), node.ID, worktree, workspacefs.Request{Op: workspacefs.OpList})
	require.NoError(t, err)
	require.NotNil(t, res.Listing)
	assert.Equal(t, "src", res.Listing.Entries[0].Path)

	res, err = nodes.Files(t.Context(), node.ID, t.TempDir(), workspacefs.Request{Op: workspacefs.OpList})
	require.NoError(t, err)
	require.NotNil(t, res.Error)
	assert.Equal(t, workspacefs.CodeInvalidPath, res.Error.Code, "a folder outside the worktree root")

	res, err = nodes.Files(t.Context(), node.ID, root, workspacefs.Request{Op: workspacefs.OpList})
	require.NoError(t, err)
	require.NotNil(t, res.Error)
	assert.Equal(t, workspacefs.CodeInvalidPath, res.Error.Code, "the worktree root itself is no worktree")

	res, err = nodes.Files(t.Context(), node.ID, filepath.Join(root, "run_1", "tsk_9"),
		workspacefs.Request{Op: workspacefs.OpList})
	require.NoError(t, err)
	require.NotNil(t, res.Error)
	assert.Equal(t, workspacefs.CodeGone, res.Error.Code)

	_, err = nodes.Files(t.Context(), away.ID, worktree, workspacefs.Request{Op: workspacefs.OpList})
	require.ErrorIs(t, err, orchestration.ErrUnavailable, "a node that is not connected")
}
