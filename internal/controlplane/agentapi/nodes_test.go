package agentapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/agentapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/phone"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// machines stands for the nodes and their sockets: it lists two nodes, zm
// connected and old not, and records what each was sent.
type machines struct {
	mu   sync.Mutex
	sent []nodeproto.Exec
	to   []string
	give nodeproto.ExecAnswer
	fail error
}

func (m *machines) ListNodes(context.Context) ([]orchestration.Node, error) {
	return []orchestration.Node{
		{ID: "nod_zm", Name: "zm", Connected: true, Status: orchestration.NodeDrained},
		{ID: "nod_old", Name: "old"},
	}, nil
}

func (m *machines) Exec(_ context.Context, nodeID string, e nodeproto.Exec) (nodeproto.ExecAnswer, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.sent, m.to = append(m.sent, e), append(m.to, nodeID)

	return m.give, m.fail
}

// withMachines serves the world's agent API with the node tools; zm's sudo
// password is in the vault.
func withMachines(w *world, m *machines) {
	mux := http.NewServeMux()
	agentapi.New(w.svc, nil, slog.New(slog.NewTextHandler(w.logs, nil))).
		WithNodeExec(m, m, func(_ context.Context, n orchestration.Node) (string, error) {
			if n.Name == "zm" {
				return "s3cret", nil
			}

			return "", nil
		}).Register(mux)

	w.srv = httptest.NewServer(mux)
	w.t.Cleanup(w.srv.Close)
}

// Scenario: an agent runs commands on a node (issue #543). A worker and a
// Bot's pi are offered the node tools, as is the owner; a coordinator token
// is not. A command reaches the node it names, by name, drained or not, with
// the node's sudo password from the vault, which is neither logged nor
// answered; the answer says the exit code and the output, an error when the
// code is not 0. A node that is not connected, or not known, is refused with
// the names of those that are.
func TestAnAgentRunsACommandOnTheNodeItNames(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	m := &machines{}
	withMachines(w, m)

	ctx := t.Context()

	worker, err := w.svc.AddAgentSession(ctx, "dsp_1", nil)
	require.NoError(t, err)

	_, coordinator, err := w.svc.AddToken(ctx, "pi", bearertoken.Coordinator)
	require.NoError(t, err)
	_, owner, err := w.svc.AddToken(ctx, "local", bearertoken.Owner)
	require.NoError(t, err)

	chat, err := w.svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	pi, err := w.svc.AddBotToken(ctx, "pi", chat.ID, orchestration.DefaultBotID)
	require.NoError(t, err)

	nodeTools := []string{agenttool.ToolNodeExec, agenttool.ToolNodeProcess, agenttool.ToolNodeProcessKill}
	assert.Subset(t, toolNames(w.tools(worker)), nodeTools)
	assert.Subset(t, toolNames(w.tools(pi)), nodeTools)
	assert.Subset(t, toolNames(w.tools(owner)), nodeTools)
	assert.NotContains(t, toolNames(w.tools(coordinator)), agenttool.ToolNodeExec)

	code := 0
	m.give = nodeproto.ExecAnswer{ExitCode: &code, Output: "Filesystem Size\n/dev/sda1 193G\n"}

	status, text, isErr := w.use(pi, agenttool.ToolNodeExec, `{"node":"zm","command":"sudo df -h /"}`)
	require.Equal(t, http.StatusOK, status, text)
	assert.False(t, isErr)
	assert.Equal(t, "exit code 0\noutput:\nFilesystem Size\n/dev/sda1 193G\n", text)

	require.Len(t, m.sent, 1)
	assert.Equal(t, "nod_zm", m.to[0])
	assert.Equal(t, nodeproto.Exec{
		Agent: "b-" + chat.ID, Action: nodeproto.ExecRun, Command: "sudo df -h /", TimeoutSeconds: 60,
		SudoPassword: "s3cret",
	}, m.sent[0], "a process is its chat's (issue #1060)")

	w.use(worker, agenttool.ToolNodeProcess, `{"node":"zm","process_id":"proc_0123456789ab"}`)
	require.Len(t, m.sent, 2)
	assert.Equal(t, "d-dsp_1", m.sent[1].Agent, "a worker's is its Dispatch's")

	m.sent, m.to = m.sent[:1], m.to[:1]

	failed := 2
	m.give = nodeproto.ExecAnswer{ExitCode: &failed, Output: "rm: cannot remove\n"}

	_, text, isErr = w.use(worker, agenttool.ToolNodeExec, `{"node":"zm","command":"rm x","timeout_seconds":900}`)
	assert.True(t, isErr)
	assert.Equal(t, "exit code 2\noutput:\nrm: cannot remove\n", text)
	assert.Equal(t, 300, m.sent[1].TimeoutSeconds, "a command runs five minutes at most")

	// The process id the tools server chose names the command on the node, for its result to be read again after a
	// restart of the control plane (issue #1060); one of another shape is refused and not sent.
	named := nodeproto.NewProcessID()
	w.use(pi, agenttool.ToolNodeExec, `{"node":"zm","command":"make","process_id":"`+named+`"}`)
	assert.Equal(t, named, m.sent[2].ProcessID)

	_, text, isErr = w.use(pi, agenttool.ToolNodeExec, `{"node":"zm","command":"make","process_id":"../x"}`)
	assert.True(t, isErr)
	assert.Contains(t, text, "process_id")

	_, text, isErr = w.use(worker, agenttool.ToolNodeExec, `{"node":"old","command":"id"}`)
	assert.True(t, isErr)
	assert.Equal(t, `node "old" is not connected; connected now: zm`, text)

	_, text, _ = w.use(worker, agenttool.ToolNodeExec, `{"node":"nowhere","command":"id"}`)
	assert.Equal(t, `no node "nowhere"; connected now: zm`, text)
	assert.Len(t, m.sent, 3, "a command for a node that cannot run it is not sent")

	logs := w.logs.String()
	assert.Contains(t, logs, "agentapi.node_exec")
	assert.NotContains(t, logs, "s3cret")
	assert.NotContains(t, logs, "df -h", "what an agent runs is not logged")
}

// A command in the background answers with its id, and the id reads or
// kills it on its node; a process that ended says how, and was killed when
// its time ran out.
func TestABackgroundCommandIsFollowedByItsID(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	m := &machines{}
	withMachines(w, m)

	worker, err := w.svc.AddAgentSession(t.Context(), "dsp_1", nil)
	require.NoError(t, err)

	m.give = nodeproto.ExecAnswer{ProcessID: "proc_1", Running: true}

	_, text, isErr := w.use(worker, agenttool.ToolNodeExec, `{"node":"zm","command":"make build","background":true}`)
	assert.False(t, isErr)
	assert.Equal(t, "running in the background, process_id proc_1", text)
	assert.Equal(t, nodeproto.ExecStart, m.sent[0].Action)
	assert.Zero(t, m.sent[0].TimeoutSeconds, "a background command has no time limit")

	code := 0
	m.give = nodeproto.ExecAnswer{ProcessID: "proc_1", ExitCode: &code, Output: "built\n"}

	_, text, isErr = w.use(worker, agenttool.ToolNodeProcess, `{"node":"zm","process_id":"proc_1","wait_seconds":30}`)
	assert.False(t, isErr)
	assert.Equal(t, "exit code 0 (process_id proc_1)\noutput:\nbuilt\n", text)
	assert.Equal(t, nodeproto.Exec{
		Agent: "d-dsp_1", Action: nodeproto.ExecOutput, ProcessID: "proc_1", WaitSeconds: 30,
	}, m.sent[1], "it is read as the worker's")

	killed := -1
	m.give = nodeproto.ExecAnswer{ProcessID: "proc_1", ExitCode: &killed}

	_, _, _ = w.use(worker, agenttool.ToolNodeProcessKill, `{"node":"zm","process_id":"proc_1"}`)
	assert.Equal(t, nodeproto.Exec{Agent: "d-dsp_1", Action: nodeproto.ExecKill, ProcessID: "proc_1"}, m.sent[2])

	m.give = nodeproto.ExecAnswer{TimedOut: true, Output: "half\n", CutBytes: 10}

	_, text, isErr = w.use(worker, agenttool.ToolNodeExec, `{"node":"zm","command":"sleep 99"}`)
	assert.True(t, isErr)
	assert.Equal(t, "killed: its time ran out\noutput, 10 bytes in the middle left out:\nhalf\n", text)
}

// blockingMachines is a node whose command runs until it is let go.
type blockingMachines struct {
	machines

	running chan struct{}
	release chan struct{}
}

func (m *blockingMachines) Exec(ctx context.Context, nodeID string, e nodeproto.Exec) (nodeproto.ExecAnswer, error) {
	close(m.running)
	<-m.release

	return m.machines.Exec(ctx, nodeID, e)
}

// An agent's tool call counts as running until it is answered, for `hypomnema update` to wait for it before it stops
// the control plane (issue #1060).
func TestAToolCallCountsWhileItRuns(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	m := &blockingMachines{running: make(chan struct{}), release: make(chan struct{})}

	mux := http.NewServeMux()
	api := agentapi.New(w.svc, nil, slog.New(slog.DiscardHandler)).
		WithNodeExec(m, m, func(context.Context, orchestration.Node) (string, error) { return "", nil })
	api.Register(mux)

	w.srv = httptest.NewServer(mux)
	t.Cleanup(w.srv.Close)

	worker, err := w.svc.AddAgentSession(t.Context(), "dsp_1", nil)
	require.NoError(t, err)

	assert.Zero(t, api.ToolCalls())

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, w.srv.URL+agenttool.PathToolsCall,
		strings.NewReader(`{"tool":"node_exec","arguments":{"node":"zm","command":"make"}}`))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+worker)

	answered := make(chan int, 1)

	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			answered <- 0

			return
		}

		_ = resp.Body.Close()
		answered <- resp.StatusCode
	}()

	<-m.running
	assert.Equal(t, 1, api.ToolCalls())

	close(m.release)
	assert.Equal(t, http.StatusOK, <-answered)
	assert.Eventually(t, func() bool { return api.ToolCalls() == 0 }, 5*time.Second, time.Millisecond,
		"the call ends once answered")
}

// A node the control plane lists as connected whose socket is not back yet, as right after the control plane
// restarted (its last heartbeat still counts), answers as a node not connected, for the tools server to ask again
// (issue #1060, review round 1).
func TestANodeWhoseSocketIsNotBackAnswersNotConnected(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	m := &machines{fail: fmt.Errorf("%w: %w", orchestration.ErrUnavailable, orchestration.ErrNodeOffline)}
	withMachines(w, m)

	worker, err := w.svc.AddAgentSession(t.Context(), "dsp_1", nil)
	require.NoError(t, err)

	_, text, isErr := w.use(worker, agenttool.ToolNodeProcess, `{"node":"zm","process_id":"proc_0123456789ab"}`)
	assert.True(t, isErr)
	assert.True(t, agenttool.IsNodeNotConnected(text), text)
	assert.Equal(t, `node "zm" is not connected; connected now: zm`, text)
}

// node_process given no process_id lists the agent's own processes on the node (issue #1060), for a turn the update
// cut to find the command it ran, as Coder's process list does.
func TestNodeProcessWithoutAnIDListsTheAgentsProcesses(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	code := 3
	m := &machines{give: nodeproto.ExecAnswer{Processes: []nodeproto.ExecProcess{
		{ProcessID: "proc_0123456789ab", Command: "make build", Running: true, StartedAt: 1_795_000_000_000},
		{ProcessID: "proc_ba9876543210", Command: "exit 3", ExitCode: &code, StartedAt: 1_794_999_000_000},
	}}}
	withMachines(w, m)

	worker, err := w.svc.AddAgentSession(t.Context(), "dsp_1", nil)
	require.NoError(t, err)

	_, text, isErr := w.use(worker, agenttool.ToolNodeProcess, `{"node":"zm"}`)
	assert.False(t, isErr)
	require.Len(t, m.sent, 1)
	assert.Equal(t, nodeproto.Exec{Agent: "d-dsp_1", Action: nodeproto.ExecList}, m.sent[0])
	assert.Contains(t, text, "process_id proc_0123456789ab, running, started ")
	assert.Contains(t, text, ": make build")
	assert.Contains(t, text, "process_id proc_ba9876543210, exit code 3, started ")

	m.give = nodeproto.ExecAnswer{}
	_, text, _ = w.use(worker, agenttool.ToolNodeProcess, `{"node":"zm"}`)
	assert.Equal(t, "no process of yours on this node", text)

	_, text, isErr = w.use(worker, agenttool.ToolNodeProcessKill, `{"node":"zm"}`)
	assert.True(t, isErr, "a kill names its process")
	assert.Contains(t, text, "process_id")
}

// phoneMachine is the phone's node, its machine stopped because the phone was idle, beside the nodes of machines:
// starting it connects its node after helloAfter, unless it never wakes; it fails after startDelay with startErr.
type phoneMachine struct {
	machines

	never      bool
	helloAfter time.Duration
	startDelay time.Duration
	startErr   error
	online     atomic.Bool
	started    atomic.Int32
}

func (m *phoneMachine) ListNodes(ctx context.Context) ([]orchestration.Node, error) {
	nodes, err := m.machines.ListNodes(ctx)

	return append(nodes, orchestration.Node{
		ID: "nod_phone", Name: "phone", Device: nodeproto.DeviceAndroid,
		Machine: &orchestration.Machine{
			ResourceID: "i-0123", State: orchestration.MachineReady, Power: orchestration.MachineStopped,
		},
	}), err
}

func (m *phoneMachine) Online(nodeID string) bool { return nodeID == "nod_zm" || m.online.Load() }

func (m *phoneMachine) DeviceCall(context.Context, string, string, string, json.RawMessage,
) (nodeproto.DeviceAnswer, error) {
	return nodeproto.DeviceAnswer{}, nil
}

func (m *phoneMachine) DeviceState(string) (nodeproto.DeviceState, bool) {
	return nodeproto.DeviceState{}, false
}

func (m *phoneMachine) Start(_ context.Context, nodeID string) (orchestration.Node, error) {
	m.started.Add(1)
	time.Sleep(m.startDelay)

	if m.startErr != nil {
		return orchestration.Node{}, m.startErr
	}

	if !m.never {
		time.AfterFunc(m.helloAfter, func() { m.online.Store(true) })
	}

	return orchestration.Node{ID: nodeID}, nil
}

func (m *phoneMachine) Stop(_ context.Context, nodeID string) (orchestration.Node, error) {
	return orchestration.Node{ID: nodeID}, nil
}

func (m *phoneMachine) Exec(ctx context.Context, nodeID string, e nodeproto.Exec) (nodeproto.ExecAnswer, error) {
	if !m.Online(nodeID) {
		return nodeproto.ExecAnswer{}, fmt.Errorf("%w: %w", orchestration.ErrUnavailable, orchestration.ErrNodeOffline)
	}

	return m.machines.Exec(ctx, nodeID, e)
}

// phoneWorld is a world whose agent API has the node tools and the phone, wired as the control plane wires them, on
// a server that ends an answer not written within writeTimeout, as the control plane's does (platform/http.go); the
// phone waits wakeWithin for its machine's hello.
func phoneWorld(t *testing.T, m *phoneMachine, writeTimeout, wakeWithin time.Duration) *world {
	t.Helper()

	shared := phone.New(phone.Config{
		Nodes: m, Link: m, Machines: m, WakeWithin: wakeWithin, Poll: 5 * time.Millisecond,
	})

	return newWorldWith(t, writeTimeout, func(api *agentapi.Server) {
		api.WithNodeExec(m, m, func(context.Context, orchestration.Node) (string, error) { return "", nil }).
			WithNodeUse(shared)
	})
}

// Scenario: a Bot runs a command on the phone's node, whose machine the control plane stopped because the phone was
// idle (issue #1083). The machine is started, as a phone call starts it, and the command runs once the node said
// hello; another node that is not connected is still answered as one, for the tools server to ask again.
func TestACommandStartsThePhonesStoppedMachine(t *testing.T) {
	t.Parallel()

	code := 0
	m := &phoneMachine{machines: machines{give: nodeproto.ExecAnswer{ExitCode: &code, Output: "emulator-5554\n"}}}
	w := phoneWorld(t, m, 0, 300*time.Millisecond)

	worker, err := w.svc.AddAgentSession(t.Context(), "dsp_1", nil)
	require.NoError(t, err)

	_, text, isErr := w.use(worker, agenttool.ToolNodeExec, `{"node":"phone","command":"adb devices"}`)
	assert.False(t, isErr, text)
	assert.Equal(t, "exit code 0\noutput:\nemulator-5554\n", text)
	assert.Equal(t, int32(1), m.started.Load())
	require.Len(t, m.sent, 1)
	assert.Equal(t, "nod_phone", m.to[0])

	_, text, isErr = w.use(worker, agenttool.ToolNodeExec, `{"node":"old","command":"id"}`)
	assert.True(t, isErr)
	assert.True(t, agenttool.IsNodeNotConnected(text), text)
	assert.Equal(t, int32(1), m.started.Load(), "only the phone's machine is started")
}

// A phone's machine that does not wake is answered with why it was stopped and how it starts, never as a node not
// connected, which would leave an agent waiting for it (issue #1083: a Bot waited 5 and 9 hours).
func TestAPhonesMachineThatDoesNotWakeSaysWhy(t *testing.T) {
	t.Parallel()

	m := &phoneMachine{never: true}
	w := phoneWorld(t, m, 0, 300*time.Millisecond)

	worker, err := w.svc.AddAgentSession(t.Context(), "dsp_1", nil)
	require.NoError(t, err)

	_, text, isErr := w.use(worker, agenttool.ToolNodeProcess, `{"node":"phone","process_id":"proc_0123456789ab"}`)
	assert.True(t, isErr)
	assert.False(t, agenttool.IsNodeNotConnected(text), text)
	assert.Contains(t, text, "the phone was idle")
	assert.Contains(t, text, "device_screen")
	assert.Empty(t, m.sent)
}

// A wake that takes longer than the server's write timeout still answers the agent (review of PR #1085, round 1):
// the command's result, once the node said hello late, and why the machine is stopped, when the provider refuses to
// start it late. Without it the answer is lost, and the tools server reads that as a restart of the control plane.
func TestAWakeLongerThanTheWriteTimeoutStillAnswers(t *testing.T) {
	t.Parallel()

	t.Run("a late hello", func(t *testing.T) {
		t.Parallel()

		code := 0
		m := &phoneMachine{
			machines:   machines{give: nodeproto.ExecAnswer{ExitCode: &code, Output: "up\n"}},
			helloAfter: 400 * time.Millisecond,
		}
		w := phoneWorld(t, m, 100*time.Millisecond, 5*time.Second)

		worker, err := w.svc.AddAgentSession(t.Context(), "dsp_1", nil)
		require.NoError(t, err)

		status, text, isErr := w.use(worker, agenttool.ToolNodeExec, `{"node":"phone","command":"uptime"}`)
		require.Equal(t, http.StatusOK, status, text)
		assert.False(t, isErr, text)
		assert.Equal(t, "exit code 0\noutput:\nup\n", text)
		require.Len(t, m.sent, 1, "the command was sent once")
	})

	t.Run("a late refusal", func(t *testing.T) {
		t.Parallel()

		m := &phoneMachine{startDelay: 400 * time.Millisecond, startErr: errors.New("InsufficientInstanceCapacity")}
		w := phoneWorld(t, m, 100*time.Millisecond, 5*time.Second)

		worker, err := w.svc.AddAgentSession(t.Context(), "dsp_1", nil)
		require.NoError(t, err)

		status, text, isErr := w.use(worker, agenttool.ToolNodeExec, `{"node":"phone","command":"uptime"}`)
		require.Equal(t, http.StatusOK, status, text)
		assert.True(t, isErr)
		assert.Contains(t, text, "InsufficientInstanceCapacity")
		assert.Contains(t, text, "device_screen")
		assert.Empty(t, m.sent)
	})
}

// slowMachines is a node whose command answers after a while.
type slowMachines struct {
	machines

	after time.Duration
}

func (m *slowMachines) Exec(ctx context.Context, nodeID string, e nodeproto.Exec) (nodeproto.ExecAnswer, error) {
	time.Sleep(m.after)

	return m.machines.Exec(ctx, nodeID, e)
}

// A command that runs longer than the server's write timeout, on any node, still answers the agent (review of PR
// #1085, round 1), as a phone call's answer does: commands run up to five minutes and the server's timeout is 30
// seconds.
func TestACommandLongerThanTheWriteTimeoutStillAnswers(t *testing.T) {
	t.Parallel()

	code := 0
	m := &slowMachines{machines: machines{give: nodeproto.ExecAnswer{ExitCode: &code}}, after: 400 * time.Millisecond}
	w := newWorldWith(t, 100*time.Millisecond, func(api *agentapi.Server) {
		api.WithNodeExec(m, m, func(context.Context, orchestration.Node) (string, error) { return "", nil })
	})

	worker, err := w.svc.AddAgentSession(t.Context(), "dsp_1", nil)
	require.NoError(t, err)

	status, text, isErr := w.use(worker, agenttool.ToolNodeExec, `{"node":"zm","command":"make"}`)
	require.Equal(t, http.StatusOK, status, text)
	assert.False(t, isErr, text)
	assert.Equal(t, "exit code 0\nno output", text)
}
