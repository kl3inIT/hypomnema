package nodeapi

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
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

// The node API scenarios: a fake node speaks the protocol to a real control
// plane (real SQLite, real WebSocket) and checks what it is offered and how
// its reports are answered.

type plane struct {
	t       *testing.T
	svc     *orchestration.Service
	srv     *httptest.Server
	node    orchestration.Node
	token   string
	account orchestration.Account
	reports int
}

func newPlane(t *testing.T) *plane {
	t.Helper()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: filepath.Join(t.TempDir(), "hypomnema.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, orchestration.Migrate(t.Context(), db))

	svc := orchestration.New(db, platform.SystemClock{}, platform.NewID,
		orchestration.WithRoles(map[string]orchestration.Role{"implement": {Runtime: "claude", Model: "opus"}}))

	mux := http.NewServeMux()
	New(svc, slog.New(slog.DiscardHandler)).Register(mux)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	n, token, err := svc.AddNode(t.Context(), "node_a", 2)
	require.NoError(t, err)

	acc, err := svc.AddAccount(t.Context(), "claude", "acct_1")
	require.NoError(t, err)

	return &plane{t: t, svc: svc, srv: srv, node: n, token: token, account: acc}
}

func (p *plane) hello(token string, claims ...nodeproto.Claim) (int, nodeproto.HelloResponse) {
	p.t.Helper()

	body, err := json.Marshal(nodeproto.HelloRequest{
		InstanceID: "inst_1", DaemonVersion: "test", WorktreeRoot: "/srv/wt",
		Runtimes: []nodeproto.RuntimeInfo{{Runtime: "claude", Path: "/usr/local/bin/claude", Version: "2.1.319"}},
		Claims:   claims,
	})
	require.NoError(p.t, err)

	req, err := http.NewRequestWithContext(p.t.Context(), http.MethodPost, p.srv.URL+nodeproto.PathHello,
		bytes.NewReader(body))
	require.NoError(p.t, err)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := p.srv.Client().Do(req)
	require.NoError(p.t, err)

	defer func() { _ = resp.Body.Close() }()

	var out nodeproto.HelloResponse
	if resp.StatusCode == http.StatusOK {
		require.NoError(p.t, json.NewDecoder(resp.Body).Decode(&out))
	} else {
		_, _ = io.Copy(io.Discard, resp.Body)
	}

	return resp.StatusCode, out
}

// dial opens the socket and returns the HTTP status of the upgrade.
func (p *plane) dial(token string) (*websocket.Conn, int, error) {
	url := "ws" + strings.TrimPrefix(p.srv.URL, "http") + nodeproto.PathSocket

	conn, resp, err := websocket.Dial(p.t.Context(), url, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
	})

	status := 0
	if resp != nil {
		status = resp.StatusCode

		if resp.Body != nil {
			_ = resp.Body.Close()
		}
	}

	return conn, status, err
}

// connect says hello and opens the socket.
func (p *plane) connect() *websocket.Conn {
	p.t.Helper()

	status, _ := p.hello(p.token)
	require.Equal(p.t, http.StatusOK, status)

	conn, _, err := p.dial(p.token)
	require.NoError(p.t, err)

	p.t.Cleanup(func() { _ = conn.CloseNow() })

	return conn
}

// next reads messages until one of type t, skipping the others.
func (p *plane) next(conn *websocket.Conn, t nodeproto.Type) nodeproto.Message {
	p.t.Helper()

	ctx, cancel := context.WithTimeout(p.t.Context(), 5*time.Second)
	defer cancel()

	for {
		var msg nodeproto.Message
		require.NoError(p.t, wsjson.Read(ctx, conn, &msg), "waiting for %s", t)

		if msg.Type == t {
			return msg
		}
	}
}

func (p *plane) send(conn *websocket.Conn, t nodeproto.Type, payload any) {
	p.t.Helper()

	msg, err := nodeproto.Encode(t, payload)
	require.NoError(p.t, err)
	require.NoError(p.t, wsjson.Write(p.t.Context(), conn, msg))
}

// report sends a report with a fresh id and returns its acknowledgement.
func (p *plane) report(conn *websocket.Conn, r nodeproto.Report) nodeproto.ReportAck {
	p.t.Helper()

	if r.ReportID == "" {
		p.reports++
		r.ReportID = fmt.Sprintf("rep_%d", p.reports)
	}

	p.send(conn, nodeproto.TypeReport, r)

	var ack nodeproto.ReportAck
	require.NoError(p.t, json.Unmarshal(p.next(conn, nodeproto.TypeReportAck).Payload, &ack))
	require.Equal(p.t, r.ReportID, ack.ReportID)

	return ack
}

func payload(t *testing.T, v any) json.RawMessage {
	t.Helper()

	raw, err := json.Marshal(v)
	require.NoError(t, err)

	return raw
}

// task creates a Run on second-bite with one Task and lets the scheduler place it.
func (p *plane) task() orchestration.Task {
	p.t.Helper()

	run, err := p.svc.CreateRun(p.t.Context(), orchestration.NewRun{
		Objective: "add Google login", CreatedBy: "owner", Repo: "kl3inIT/second-bite",
	})
	require.NoError(p.t, err)

	tasks, err := p.svc.CreateTasks(p.t.Context(), run.ID, []orchestration.NewTask{
		{Ref: "a", Role: "implement", Spec: "add the Google login button"},
	})
	require.NoError(p.t, err)

	sch, err := p.svc.Schedule(p.t.Context())
	require.NoError(p.t, err)
	require.Len(p.t, sch.Created, 1)

	return tasks[0]
}

func (p *plane) taskStatus(id string) orchestration.TaskStatus {
	p.t.Helper()

	task, err := p.svc.GetTask(p.t.Context(), id)
	require.NoError(p.t, err)

	return task.Status
}

// Scenario: a token the control plane did not mint. Neither hello nor the
// socket is served.
func TestUnknownTokenIsRefused(t *testing.T) {
	t.Parallel()

	p := newPlane(t)

	status, _ := p.hello(p.token + "x")
	assert.Equal(t, http.StatusUnauthorized, status)

	_, status, err := p.dial("")
	require.Error(t, err)
	assert.Equal(t, http.StatusUnauthorized, status)
}

// Scenario: the whole life of a Dispatch over the socket. The node is offered
// it with what it needs, accepts it, starts it and reports success; a resent
// report gets the same answer.
func TestDispatchRunsOverTheSocket(t *testing.T) {
	t.Parallel()

	p := newPlane(t)
	conn := p.connect()
	task := p.task()

	var offer nodeproto.Offer
	require.NoError(t, json.Unmarshal(p.next(conn, nodeproto.TypeOffer).Payload, &offer))
	assert.Equal(t, task.ID, offer.TaskID)
	assert.Equal(t, "add the Google login button", offer.Spec)
	assert.Equal(t, "kl3inIT/second-bite", offer.Repo)
	assert.Equal(t, orchestration.DefaultBase, offer.Base)
	assert.Equal(t, "/srv/wt/"+task.RunID+"/"+task.ID, offer.Worktree)
	assert.Equal(t, "claude", offer.Runtime)
	assert.Equal(t, p.account.ID, offer.AccountRef)
	assert.Equal(t, "opus", offer.Model)

	accept := nodeproto.Report{ReportID: "rep_accept", Kind: nodeproto.ReportAccept, DispatchID: offer.DispatchID}
	ack := p.report(conn, accept)
	require.Equal(t, nodeproto.AckCommitted, ack.Status, ack.Error)

	var accepted orchestration.Accepted
	require.NoError(t, json.Unmarshal(ack.Response, &accepted))
	assert.Equal(t, 1, accepted.Epoch)

	again := p.report(conn, accept)
	assert.Equal(t, ack, again, "a resent report gets the same answer")

	ack = p.report(conn, nodeproto.Report{
		Kind: nodeproto.ReportStarted, DispatchID: offer.DispatchID, Epoch: 1,
		Payload: payload(t, nodeproto.Started{ProcessRef: "pid:42@1", CLISessionRef: "sess_1"}),
	})
	require.Equal(t, nodeproto.AckCommitted, ack.Status, ack.Error)

	p.send(conn, nodeproto.TypeHeartbeat, nil)

	var beat nodeproto.HeartbeatAck
	require.NoError(t, json.Unmarshal(p.next(conn, nodeproto.TypeHeartbeatAck).Payload, &beat))
	assert.Equal(t, nodeproto.HeartbeatEvery.Milliseconds(), beat.HeartbeatAfterMs)

	code := 0
	ack = p.report(conn, nodeproto.Report{
		Kind: nodeproto.ReportFinished, DispatchID: offer.DispatchID, Epoch: 1,
		Payload: payload(t, nodeproto.Finished{
			Summary:  "added the button; PR #7 is green",
			Evidence: []nodeproto.Evidence{{Kind: "pull_request", Ref: "kl3inIT/second-bite#7"}},
			Exit:     nodeproto.Exit{Kind: "exited", Code: &code},
		}),
	})
	require.Equal(t, nodeproto.AckCommitted, ack.Status, ack.Error)
	assert.Equal(t, orchestration.TaskSucceeded, p.taskStatus(task.ID))
}

// Scenario: a worker asks the owner and waits. The answer is pushed as soon
// as the owner gives it; a cancel of the Task pushes a stop.
func TestAnswerAndStopArePushed(t *testing.T) {
	t.Parallel()

	p := newPlane(t)
	conn := p.connect()
	task := p.task()

	var offer nodeproto.Offer
	require.NoError(t, json.Unmarshal(p.next(conn, nodeproto.TypeOffer).Payload, &offer))
	require.Equal(t, nodeproto.AckCommitted,
		p.report(conn, nodeproto.Report{Kind: nodeproto.ReportAccept, DispatchID: offer.DispatchID}).Status)
	require.Equal(t, nodeproto.AckCommitted, p.report(conn, nodeproto.Report{
		Kind: nodeproto.ReportStarted, DispatchID: offer.DispatchID, Epoch: 1,
		Payload: payload(t, nodeproto.Started{ProcessRef: "pid:42@1", CLISessionRef: "sess_1"}),
	}).Status)

	ack := p.report(conn, nodeproto.Report{
		Kind: nodeproto.ReportQuestion, DispatchID: offer.DispatchID, Epoch: 1,
		Payload: payload(t, nodeproto.Ask{
			Text: "goth or hand-written OAuth?", Options: []nodeproto.Option{{Label: "goth"}, {Label: "hand-written"}},
			To: "owner",
		}),
	})
	require.Equal(t, nodeproto.AckCommitted, ack.Status, ack.Error)

	var asked struct {
		QuestionID string `json:"question_id"`
	}
	require.NoError(t, json.Unmarshal(ack.Response, &asked))

	first := 0
	_, err := p.svc.AnswerQuestion(t.Context(), asked.QuestionID, orchestration.AddressOwner,
		orchestration.Answer{Option: &first})
	require.NoError(t, err)

	var answer orchestration.Question
	require.NoError(t, json.Unmarshal(p.next(conn, nodeproto.TypeAnswer).Payload, &answer))
	assert.Equal(t, asked.QuestionID, answer.ID)
	require.NotNil(t, answer.Answer)
	assert.Equal(t, &first, answer.Answer.Option)

	require.Equal(t, nodeproto.AckCommitted, p.report(conn, nodeproto.Report{
		Kind: nodeproto.ReportAnswerAck, DispatchID: offer.DispatchID, Epoch: 1,
		Payload: payload(t, nodeproto.AnswerAck{QuestionID: asked.QuestionID}),
	}).Status)

	require.NoError(t, p.svc.CancelTask(t.Context(), task.ID))

	var stop nodeproto.Stop
	require.NoError(t, json.Unmarshal(p.next(conn, nodeproto.TypeStop).Payload, &stop))
	assert.Equal(t, nodeproto.Stop{DispatchID: offer.DispatchID, Epoch: 1}, stop)
}

// Scenario: reports that can never apply are rejected for good, so the node
// stops resending them.
func TestReportsThatCannotApplyAreRejected(t *testing.T) {
	t.Parallel()

	p := newPlane(t)
	conn := p.connect()
	p.task()

	var offer nodeproto.Offer
	require.NoError(t, json.Unmarshal(p.next(conn, nodeproto.TypeOffer).Payload, &offer))

	stale := p.report(conn, nodeproto.Report{Kind: nodeproto.ReportProgress, DispatchID: offer.DispatchID, Epoch: 7})
	assert.Equal(t, nodeproto.AckRejected, stale.Status)

	unknown := p.report(conn, nodeproto.Report{Kind: "teleport", DispatchID: offer.DispatchID})
	assert.Equal(t, nodeproto.AckRejected, unknown.Status)

	bad := p.report(conn, nodeproto.Report{
		Kind: nodeproto.ReportStarted, DispatchID: offer.DispatchID, Epoch: 0, Payload: json.RawMessage(`[1]`),
	})
	assert.Equal(t, nodeproto.AckRejected, bad.Status)
}

// Scenario: the node connects again while its old socket is still open. The
// old one is told it was replaced; the new one is offered the work again.
func TestNewConnectionReplacesTheOld(t *testing.T) {
	t.Parallel()

	p := newPlane(t)
	old := p.connect()
	p.task()
	p.next(old, nodeproto.TypeOffer)

	fresh := p.connect()

	var e nodeproto.Error
	require.NoError(t, json.Unmarshal(p.next(old, nodeproto.TypeError).Payload, &e))
	assert.Equal(t, nodeproto.ErrorReplaced, e.Code)

	p.next(fresh, nodeproto.TypeOffer)
}

// Scenario: hello after a restart. Each claimed Dispatch is answered.
func TestHelloAnswersEachClaim(t *testing.T) {
	t.Parallel()

	p := newPlane(t)
	conn := p.connect()
	p.task()

	var offer nodeproto.Offer
	require.NoError(t, json.Unmarshal(p.next(conn, nodeproto.TypeOffer).Payload, &offer))
	require.Equal(t, nodeproto.AckCommitted,
		p.report(conn, nodeproto.Report{Kind: nodeproto.ReportAccept, DispatchID: offer.DispatchID}).Status)

	status, out := p.hello(p.token,
		nodeproto.Claim{DispatchID: offer.DispatchID, Epoch: 1},
		nodeproto.Claim{DispatchID: "dsp_gone", Epoch: 1},
	)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, p.node.ID, out.NodeID)
	assert.Equal(t, []nodeproto.Disposition{
		{DispatchID: offer.DispatchID, Kind: nodeproto.DispositionRetain},
		{DispatchID: "dsp_gone", Kind: nodeproto.DispositionRelease},
	}, out.Dispositions)
}
