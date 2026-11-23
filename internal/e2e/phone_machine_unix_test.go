//go:build unix

package e2e

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/agentapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/nodeapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/phone"
	"github.com/kl3inIT/hypomnema/internal/node/daemon"
	"github.com/kl3inIT/hypomnema/internal/node/nodeexec"
	"github.com/kl3inIT/hypomnema/internal/node/statedb"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

// Scenario: the phone's machine was stopped because the phone was idle, but its node's socket from before the stop
// is still open, as it is while the provider shuts the machine down (issue #1083, review of PR #1085, round 2). A
// worker's node_exec on the phone's node starts the machine and is sent only once the node said hello again after
// the start: never on the old socket. When the node does not come back, the worker is told why the machine is off
// and how it starts, and the command has not run.
func TestACommandWaitsForThePhonesNodeToComeBackAfterItsStart(t *testing.T) {
	t.Parallel()

	t.Run("the node does not come back", func(t *testing.T) {
		t.Parallel()

		r := newWakeRig(t, false)

		ran := filepath.Join(t.TempDir(), "ran")
		text, isErr := r.exec.use(t, agenttool.ToolNodeExec, `{"node":"phone","command":"touch `+ran+`"}`)
		assert.True(t, isErr)
		assert.Contains(t, text, "the phone was idle")
		assert.Contains(t, text, "device_screen")
		assert.Equal(t, int32(1), r.machines.started.Load())
		assert.NoFileExists(t, ran, "the command was not sent on the socket from before the stop")
	})

	t.Run("the node comes back", func(t *testing.T) {
		t.Parallel()

		r := newWakeRig(t, true)

		text, isErr := r.exec.use(t, agenttool.ToolNodeExec, `{"node":"phone","command":"echo back"}`)
		assert.False(t, isErr, text)
		assert.Equal(t, "exit code 0\noutput:\nback\n", text)
		assert.Equal(t, int32(1), r.machines.started.Load())
		assert.Equal(t, int32(2), r.hellos.Load(), "it ran after the node's hello since the start")
	})
}

// wakeMachines powers the phone's machine as machines.Manager does in the registry, with no provider: a start
// records `starting`, and then, when comeBack is set, the node goes away and says hello again, as a machine that
// was shut down and booted does.
type wakeMachines struct {
	svc      *orchestration.Service
	comeBack func()
	started  atomic.Int32
}

func (m *wakeMachines) Start(ctx context.Context, nodeID string) (orchestration.Node, error) {
	m.started.Add(1)

	if err := m.svc.BeginMachineStart(ctx, nodeID); err != nil {
		return orchestration.Node{}, err
	}

	if m.comeBack != nil {
		go m.comeBack()
	}

	return m.svc.GetNode(ctx, nodeID)
}

func (m *wakeMachines) Stop(ctx context.Context, nodeID string) (orchestration.Node, error) {
	if err := m.svc.BeginMachineStop(ctx, nodeID); err != nil {
		return orchestration.Node{}, err
	}

	if err := m.svc.MachineStopped(ctx, nodeID); err != nil {
		return orchestration.Node{}, err
	}

	return m.svc.GetNode(ctx, nodeID)
}

type wakeRig struct {
	exec     *execRig
	machines *wakeMachines
	// hellos counts the node's processes that said hello.
	hellos atomic.Int32
}

// newWakeRig is a control plane whose phone's node, a machine a provider made, is connected and then recorded as
// stopped for idleness while its socket stays open; comeBack says whether its node says hello again once the
// machine is started.
func newWakeRig(t *testing.T, comeBack bool) *wakeRig {
	t.Helper()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	ctx := t.Context()
	logger := slog.New(slog.DiscardHandler)
	svc := orchestration.New(db, platform.SystemClock{}, platform.NewID)

	r := &wakeRig{machines: &wakeMachines{svc: svc}}

	mux := http.NewServeMux()
	nodes := nodeapi.New(svc, logger)
	nodes.Register(mux)

	shared := phone.New(phone.Config{
		Nodes: svc, Link: nodes, Machines: r.machines, Logger: logger,
		WakeWithin: 2 * time.Second, Poll: 20 * time.Millisecond,
	})
	agentapi.New(svc, nil, logger).
		WithNodeExec(svc, nodes, func(context.Context, orchestration.Node) (string, error) { return "", nil }).
		WithNodeUse(shared).Register(mux)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	provider, err := svc.AddMachineProvider(ctx, orchestration.NewMachineProvider{
		Name: "aws", Kind: orchestration.MachineProviderAWS, KeyRef: "aws-key", Region: "ap-southeast-1",
		Size: "m8i.large",
	})
	require.NoError(t, err)

	n, token, _, err := svc.BeginMachine(ctx, orchestration.NewMachine{
		Name: "phone", ProviderID: provider.ID, Region: "ap-southeast-1", Size: "m8i.large", MaxDispatches: 1,
	})
	require.NoError(t, err)
	require.NoError(t, svc.MachineProvisioned(ctx, n.ID, "i-0123"))

	// run starts the node's process; leave ends it, which closes its socket.
	run := func() func() {
		state, err := statedb.Open(ctx, filepath.Join(t.TempDir(), "state.db"), time.Now)
		require.NoError(t, err)

		commands := nodeexec.New()
		d := daemon.New(daemon.Config{
			URL: srv.URL, Token: token, WorktreeRoot: "/srv/wt", Version: "test", Device: nodeproto.DeviceAndroid,
			KVM: func() bool { return true }, Phone: newFakePhone(), Commands: commands,
		}, state, newFakeRunner(), logger)

		runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		done := make(chan error, 1)

		r.hellos.Add(1)

		go func() { done <- d.Run(runCtx) }()

		var ending sync.Once

		leave := func() {
			ending.Do(func() {
				cancel()

				if err := <-done; err != nil {
					t.Logf("the node ended with: %v", err)
				}

				commands.Close()

				_ = state.Close()
			})
		}
		t.Cleanup(leave)

		return leave
	}

	leave := run()

	online := func() bool {
		got, err := svc.GetNode(ctx, n.ID)

		return err == nil && got.Machine != nil && got.Machine.State == orchestration.MachineReady &&
			got.Machine.Power == "" && nodes.Online(n.ID)
	}
	require.Eventually(t, online, wait, 20*time.Millisecond)

	if comeBack {
		r.machines.comeBack = func() {
			leave()
			run()
		}
	}

	// Stopped for idleness, as the phone's sweep stops it; the provider has not shut the machine down yet.
	_, err = r.machines.Stop(ctx, n.ID)
	require.NoError(t, err)
	require.True(t, nodes.Online(n.ID), "the socket from before the stop is still open")

	key, err := svc.AddAgentSession(ctx, "dsp_1", nil)
	require.NoError(t, err)

	r.exec = &execRig{srv: srv, key: key}

	return r
}
