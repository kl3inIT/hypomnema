package agentapi_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/agentapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
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

	return m.give, nil
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
		Action: nodeproto.ExecRun, Command: "sudo df -h /", TimeoutSeconds: 60, SudoPassword: "s3cret",
	}, m.sent[0])

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
	assert.Equal(t, nodeproto.Exec{Action: nodeproto.ExecOutput, ProcessID: "proc_1", WaitSeconds: 30}, m.sent[1])

	killed := -1
	m.give = nodeproto.ExecAnswer{ProcessID: "proc_1", ExitCode: &killed}

	_, _, _ = w.use(worker, agenttool.ToolNodeProcessKill, `{"node":"zm","process_id":"proc_1"}`)
	assert.Equal(t, nodeproto.Exec{Action: nodeproto.ExecKill, ProcessID: "proc_1"}, m.sent[2])

	m.give = nodeproto.ExecAnswer{TimedOut: true, Output: "half\n", CutBytes: 10}

	_, text, isErr = w.use(worker, agenttool.ToolNodeExec, `{"node":"zm","command":"sleep 99"}`)
	assert.True(t, isErr)
	assert.Equal(t, "killed: its time ran out\noutput, 10 bytes in the middle left out:\nhalf\n", text)
}

