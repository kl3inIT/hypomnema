package controlplane

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

func migratedConfig(t *testing.T) Config {
	t.Helper()

	cfg, err := LoadConfig(func(name string) string {
		return map[string]string{"HYPOMNEMA_DATABASE_PATH": filepath.Join(t.TempDir(), "hypomnema.db")}[name]
	})
	require.NoError(t, err)
	require.NoError(t, Migrate(t.Context(), cfg, io.Discard))

	return cfg
}

func command(t *testing.T, cfg Config, args ...string) (string, error) {
	t.Helper()

	var out bytes.Buffer
	err := Command(t.Context(), cfg, args, &out)

	return out.String(), err
}

// Scenario: the owner sets up a node and an account and starts a Run without
// following it. The token is printed once; listing shows the node offline
// until it connects.
func TestOwnerSetsUpAndStartsARun(t *testing.T) {
	t.Parallel()

	cfg := migratedConfig(t)

	out, err := command(t, cfg, "nodes", "add", "zm", "--max", "3")
	require.NoError(t, err)
	assert.Contains(t, out, "up to 3 Dispatches")
	assert.Regexp(t, `(?m)^hyn_[A-Za-z0-9_-]+$`, out)

	out, err = command(t, cfg, "nodes", "list")
	require.NoError(t, err)
	assert.Contains(t, out, "\tzm\tactive\toffline\t0/3\t")

	out, err = command(t, cfg, "accounts", "add", "claude", "personal")
	require.NoError(t, err)
	assert.Contains(t, out, "(claude personal)")

	out, err = command(t, cfg, "run", "--repo", "kl3inIT/second-bite", "--detach", "add", "Google", "login")
	require.NoError(t, err)
	assert.Regexp(t, `^run run_\w+, task tsk_\w+\n$`, out)

	_, err = command(t, cfg, "run", "--detach", "no repository")
	require.ErrorIs(t, err, ErrUsage)

	_, err = command(t, cfg, "run", "--repo", "not a repo", "--detach", "x")
	require.ErrorIs(t, err, orchestration.ErrInvalid)

	_, err = command(t, cfg, "teleport")
	require.ErrorIs(t, err, ErrUsage)
}

// Scenario: the owner follows a Run. Its events are printed until the Task
// ends; then the Run is finished.
func TestFollowEndsWhenTheTaskEnds(t *testing.T) {
	t.Parallel()

	cfg := migratedConfig(t)

	db, err := sqlite.Open(t.Context(), cfg.Database)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	svc := orchestration.New(db, platform.SystemClock{}, platform.NewID)

	run, err := svc.CreateRun(t.Context(), orchestration.NewRun{
		Objective: "add Google login", CreatedBy: "owner", Repo: "kl3inIT/second-bite",
	})
	require.NoError(t, err)

	tasks, err := svc.CreateTasks(t.Context(), run.ID, []orchestration.NewTask{{Ref: "a", Role: "implement", Spec: "x"}})
	require.NoError(t, err)

	var out syncBuffer

	o := &owner{svc: svc, out: &out, every: 10 * time.Millisecond}
	done := make(chan error, 1)

	go func() { done <- o.follow(context.WithoutCancel(t.Context()), run.ID, tasks[0].ID) }()

	require.Eventually(t, func() bool { return strings.Contains(out.String(), "task.created") }, 5*time.Second,
		10*time.Millisecond)
	require.NoError(t, svc.CancelTask(t.Context(), tasks[0].ID))
	require.NoError(t, <-done)

	assert.Contains(t, out.String(), "task canceled")

	finished, err := svc.GetRun(t.Context(), run.ID)
	require.NoError(t, err)
	assert.Equal(t, orchestration.RunFinished, finished.Status)
}

// syncBuffer is a bytes.Buffer safe for one writer and one reader.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}
