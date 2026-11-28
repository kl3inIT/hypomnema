package e2e

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/nodeapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/node/daemon"
	"github.com/kl3inIT/hypomnema/internal/node/memlimit"
	"github.com/kl3inIT/hypomnema/internal/node/statedb"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

// Scenario: a node of 8 GiB says hello. The node is given its workers' memory ceiling, its allocatable memory (8 GiB
// less the 1 GiB floor), and what it said of its limit is kept: one whose user manager does not delegate memory
// says so, and the Machines page shows it not limited (issue #1313 rows 2, 6).
func TestTheNodeAppliesTheCeilingItIsSent(t *testing.T) {
	t.Parallel()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	svc := orchestration.New(db, platform.SystemClock{}, platform.NewID)

	mux := http.NewServeMux()
	nodeapi.New(svc, slog.New(slog.DiscardHandler)).Register(mux)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	node, token, err := svc.AddNode(t.Context(), "node_a", 2)
	require.NoError(t, err)

	state, err := statedb.Open(t.Context(), filepath.Join(t.TempDir(), "node", "state.db"), time.Now)
	require.NoError(t, err)
	t.Cleanup(func() { _ = state.Close() })

	var (
		mu      sync.Mutex
		applied []int64
	)

	d := daemon.New(daemon.Config{
		URL: srv.URL, Token: token, WorktreeRoot: "/srv/wt", Version: "test",
		System:      nodeproto.SystemInfo{OS: "linux", Arch: "amd64", CPUs: 4, MemoryBytes: 8 << 30},
		MemoryLimit: &nodeproto.MemoryLimit{Reason: "the systemd user manager does not delegate the memory controller"},
		WorkerMemoryMax: func(_ context.Context, memoryMax int64) {
			mu.Lock()
			defer mu.Unlock()

			applied = append(applied, memoryMax)
		},
	}, state, newFakeRunner(), slog.New(slog.DiscardHandler))

	ctx, cancel := context.WithCancel(context.WithoutCancel(t.Context()))
	done := make(chan error, 1)

	go func() { done <- d.Run(ctx) }()

	t.Cleanup(func() {
		cancel()

		if err := <-done; err != nil {
			t.Logf("the node ended with: %v", err)
		}
	})

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()

		return len(applied) > 0
	}, wait, 20*time.Millisecond)

	mu.Lock()
	assert.Equal(t, int64(7<<30), applied[0])
	mu.Unlock()

	n, err := svc.GetNode(t.Context(), node.ID)
	require.NoError(t, err)
	assert.Equal(t, &orchestration.MemoryLimit{
		Reason: "the systemd user manager does not delegate the memory controller",
	}, n.MemoryLimit)
}

// Scenario: the owner reserves 7 GiB of a node of 8 GiB, so its allocatable memory is 0 (issue #1313, review round
// 1). The control plane says 0 at the next heartbeat; the node keeps the ceiling it applied and never lifts it to
// infinity.
func TestAnAllocatableOfZeroKeepsTheCeiling(t *testing.T) {
	t.Parallel()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	svc := orchestration.New(db, platform.SystemClock{}, platform.NewID)

	mux := http.NewServeMux()
	nodeapi.New(svc, slog.New(slog.DiscardHandler)).Register(mux)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	node, token, err := svc.AddNode(t.Context(), "node_a", 2)
	require.NoError(t, err)

	state, err := statedb.Open(t.Context(), filepath.Join(t.TempDir(), "node", "state.db"), time.Now)
	require.NoError(t, err)
	t.Cleanup(func() { _ = state.Close() })

	var (
		mu    sync.Mutex
		sent  []int64
		slice [][]string
	)

	ceiling := &memlimit.Ceiling{
		Limit: memlimit.Limit{SystemdRun: "systemd-run", Systemctl: "systemctl"},
		Run: func(_ context.Context, argv ...string) ([]byte, error) {
			mu.Lock()
			defer mu.Unlock()

			slice = append(slice, argv[4:])

			return nil, nil
		},
	}

	d := daemon.New(daemon.Config{
		URL: srv.URL, Token: token, WorktreeRoot: "/srv/wt", Version: "test",
		System:      nodeproto.SystemInfo{OS: "linux", Arch: "amd64", CPUs: 4, MemoryBytes: 8 << 30},
		MemoryLimit: &nodeproto.MemoryLimit{On: true},
		WorkerMemoryMax: func(ctx context.Context, memoryMax int64) {
			mu.Lock()
			sent = append(sent, memoryMax)
			mu.Unlock()

			ceiling.Apply(ctx, memoryMax)
		},
	}, state, newFakeRunner(), slog.New(slog.DiscardHandler))

	ctx, cancel := context.WithCancel(context.WithoutCancel(t.Context()))
	done := make(chan error, 1)

	go func() { done <- d.Run(ctx) }()

	t.Cleanup(func() {
		cancel()

		if err := <-done; err != nil {
			t.Logf("the node ended with: %v", err)
		}
	})

	said := func(v int64) func() bool {
		return func() bool {
			mu.Lock()
			defer mu.Unlock()

			return slices.Contains(sent, v)
		}
	}

	require.Eventually(t, said(7<<30), wait, 20*time.Millisecond)

	reserve := int64(7 << 30)
	_, err = svc.UpdateNode(t.Context(), node.ID, orchestration.NodeChange{ReservedMemoryBytes: &reserve})
	require.NoError(t, err)

	// The next heartbeat's acknowledgement says 0.
	require.Eventually(t, said(0), 3*nodeproto.HeartbeatEvery, 50*time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	assert.Equal(t, [][]string{{"MemoryMax=7516192768", "MemoryHigh=7516192768", "MemorySwapMax=0"}}, slice)
}
