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
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

// The node API scenarios: a fake node speaks the protocol to a real control
// plane (real SQLite, real WebSocket) and checks what it is offered and how
// its reports are answered.

type plane struct {
	t       *testing.T
	svc     *orchestration.Service
	api     *Server
	srv     *httptest.Server
	node    orchestration.Node
	token   string
	account orchestration.Account
	reports int
}

func newPlane(t *testing.T) *plane {
	t.Helper()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	svc := orchestration.New(db, platform.SystemClock{}, platform.NewID)

	_, _, err = svc.ApplyProfile(t.Context(), "",
		"name: implementer\ndescription: d\nruntime: claude\nmodel: {name: opus}\n")
	require.NoError(t, err)

	mux := http.NewServeMux()
	api := New(svc, slog.New(slog.DiscardHandler))
	api.Register(mux)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	n, token, err := svc.AddNode(t.Context(), "node_a", 2)
	require.NoError(t, err)

	acc, err := svc.AddAccount(t.Context(), "claude", "acct_1")
	require.NoError(t, err)

	return &plane{t: t, svc: svc, api: api, srv: srv, node: n, token: token, account: acc}
}

func (p *plane) hello(token string, claims ...nodeproto.Claim) (int, nodeproto.HelloResponse) {
	p.t.Helper()

	body, err := json.Marshal(nodeproto.HelloRequest{
		InstanceID: "inst_1", DaemonVersion: "test", WorktreeRoot: "/srv/wt",
		Runtimes: []nodeproto.RuntimeInfo{{Runtime: "claude", Path: "/usr/local/bin/claude", Version: "2.1.319"}},
		System:   nodeproto.SystemInfo{OS: "linux", Arch: "arm64"},
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
		{Ref: "a", Profile: "implementer", Spec: "add the Google login button"},
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

// Scenario: the control plane names its git hosts at hello and with every
// heartbeat's answer; the node says in its heartbeat which it reaches, and a
// Task on a host it does not reach is not given to it until it does.
func TestANodeIsAskedWhichGitHostsItReaches(t *testing.T) {
	t.Parallel()

	p := newPlane(t)

	_, err := p.svc.AddGitHost(t.Context(), "gitlab.company.example", "gitlab")
	require.NoError(t, err)

	status, hello := p.hello(p.token)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, []string{"github.com", "gitlab.com", "gitlab.company.example"}, hello.GitHosts)

	conn := p.connect()

	none := []string{}
	p.send(conn, nodeproto.TypeHeartbeat, nodeproto.Heartbeat{Reaches: &none})

	var ack nodeproto.HeartbeatAck
	require.NoError(t, json.Unmarshal(p.next(conn, nodeproto.TypeHeartbeatAck).Payload, &ack))
	assert.Equal(t, hello.GitHosts, ack.GitHosts)

	run, err := p.svc.CreateRun(t.Context(), orchestration.NewRun{
		Objective: "add Google login", CreatedBy: "owner", Repo: "kl3inIT/second-bite",
	})
	require.NoError(t, err)

	_, err = p.svc.CreateTasks(t.Context(), run.ID, []orchestration.NewTask{
		{Ref: "a", Profile: "implementer", Spec: "add the Google login button"},
	})
	require.NoError(t, err)

	sch, err := p.svc.Schedule(t.Context())
	require.NoError(t, err)
	assert.Empty(t, sch.Created, "the node reaches no host: the Task waits")

	github := []string{"github.com"}
	p.send(conn, nodeproto.TypeHeartbeat, nodeproto.Heartbeat{Reaches: &github})
	p.next(conn, nodeproto.TypeHeartbeatAck)

	sch, err = p.svc.Schedule(t.Context())
	require.NoError(t, err)
	assert.Len(t, sch.Created, 1, "it reaches github.com now")

	// A heartbeat that says nothing about hosts leaves what the node said.
	p.send(conn, nodeproto.TypeHeartbeat, nodeproto.Heartbeat{})
	p.next(conn, nodeproto.TypeHeartbeatAck)

	nodes, err := p.svc.ListNodes(t.Context())
	require.NoError(t, err)
	require.Len(t, nodes, 1)
	assert.Equal(t, []string{"github.com"}, nodes[0].GitHosts)
}

// Scenario: the owner gave github.com a token. The offer of a Task on GitHub
// carries it, read as the offer is sent; a host without a token, or a control
// plane that keeps none, sends the offer without one.
func TestAnOfferCarriesItsHostsToken(t *testing.T) {
	t.Parallel()

	p := newPlane(t)

	asked := make(chan string, 4)
	p.api.WithHostTokens(func(_ context.Context, host, path string) (string, bool) {
		asked <- host + " " + path

		return "ghp_for_this_dispatch", host == "github.com"
	})

	conn := p.connect()
	p.task()

	var offer nodeproto.Offer
	require.NoError(t, json.Unmarshal(p.next(conn, nodeproto.TypeOffer).Payload, &offer))
	assert.Equal(t, "github.com kl3inIT/second-bite", <-asked, "asked by host and repository")
	assert.Equal(t, [3]string{"github.com", "github", "ghp_for_this_dispatch"},
		[3]string{offer.RepoHost, offer.RepoKind, offer.RepoToken})

	stored, err := p.svc.NodePending(t.Context(), p.node.ID)
	require.NoError(t, err)
	require.Len(t, stored.Offers, 1)
	assert.Empty(t, stored.Offers[0].RepoToken, "orchestration never holds the token")

	// Another connection of the node is told again; a host that has no token gives an offer without one.
	p.api.WithHostTokens(func(context.Context, string, string) (string, bool) { return "", false })

	again := p.connect()

	var second nodeproto.Offer
	require.NoError(t, json.Unmarshal(p.next(again, nodeproto.TypeOffer).Payload, &second))
	assert.Equal(t, offer.DispatchID, second.DispatchID)
	assert.Empty(t, second.RepoToken)
}

// Scenario: the node sends a running worker's transcript. Each batch is
// answered with the newest item held, a batch sent again changes nothing, and
// a batch for a Dispatch the node does not have is refused for good.
func TestTranscriptItemsAreStoredAndAcknowledged(t *testing.T) {
	t.Parallel()

	p := newPlane(t)
	conn := p.connect()
	p.task()

	var offer nodeproto.Offer
	require.NoError(t, json.Unmarshal(p.next(conn, nodeproto.TypeOffer).Payload, &offer))
	accept := nodeproto.Report{ReportID: "rep_accept", Kind: nodeproto.ReportAccept, DispatchID: offer.DispatchID}
	require.Equal(t, nodeproto.AckCommitted, p.report(conn, accept).Status)

	items := func(b nodeproto.Items) nodeproto.ItemsAck {
		p.send(conn, nodeproto.TypeItems, b)

		var ack nodeproto.ItemsAck
		require.NoError(t, json.Unmarshal(p.next(conn, nodeproto.TypeItemsAck).Payload, &ack))

		return ack
	}

	batch := nodeproto.Items{DispatchID: offer.DispatchID, Items: []nodeproto.Item{
		{Seq: 1, Kind: nodeproto.ItemMessage, Text: "Adding the button."},
		{
			Seq: 2, Kind: nodeproto.ItemToolCall, CallID: "call_1", Tool: "Write",
			Input: json.RawMessage(`{"file_path":"login.tsx"}`),
		},
	}}
	want := nodeproto.ItemsAck{DispatchID: offer.DispatchID, Seq: 2, Status: nodeproto.AckCommitted}

	assert.Equal(t, want, items(batch))
	assert.Equal(t, want, items(batch), "a batch sent again is acknowledged the same")

	page, err := p.svc.ListItems(t.Context(), offer.DispatchID, 0, 0, 0)
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	assert.Equal(t, "Adding the button.", page.Items[0].Text)
	assert.Equal(t, "Write", page.Items[1].Tool)

	refused := items(nodeproto.Items{DispatchID: "dsp_missing", Items: batch.Items})
	assert.Equal(t, nodeproto.AckRejected, refused.Status)
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

// Scenario: the owner revokes the token a node is connected with. The node's
// next heartbeat ends the connection, and it cannot connect again.
func TestRevokedTokenEndsTheConnection(t *testing.T) {
	t.Parallel()

	p := newPlane(t)
	conn := p.connect()

	tokens, err := p.svc.ListNodeTokens(t.Context(), p.node.ID)
	require.NoError(t, err)
	require.Len(t, tokens, 1)
	require.NoError(t, p.svc.RevokeNodeToken(t.Context(), tokens[0].ID))

	p.send(conn, nodeproto.TypeHeartbeat, nil)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	for {
		var msg nodeproto.Message
		if err := wsjson.Read(ctx, conn, &msg); err != nil {
			require.NoError(t, ctx.Err(), "the socket must close before the wait ends")

			break
		}

		require.NotEqual(t, nodeproto.TypeHeartbeatAck, msg.Type, "a revoked token gets no heartbeat answer")
	}

	status, _ := p.hello(p.token)
	assert.Equal(t, http.StatusUnauthorized, status)
}

// Scenario: a heartbeat carries what the node measured. The control plane
// shows it; a heartbeat whose measurement cannot be true is still answered,
// and the measurement before it stays.
func TestHeartbeatCarriesTheNodesMeasurement(t *testing.T) {
	t.Parallel()

	p := newPlane(t)
	conn := p.connect()

	p.send(conn, nodeproto.TypeHeartbeat, nodeproto.Heartbeat{Stats: &nodeproto.SystemStats{
		CPUPercent: 42, MemoryUsedBytes: 3 << 30, DiskUsedBytes: 20 << 30, Load1: 1.5,
	}})
	p.next(conn, nodeproto.TypeHeartbeatAck)

	p.send(conn, nodeproto.TypeHeartbeat, nodeproto.Heartbeat{Stats: &nodeproto.SystemStats{CPUPercent: 900}})
	p.next(conn, nodeproto.TypeHeartbeatAck)

	n, err := p.svc.GetNode(t.Context(), p.node.ID)
	require.NoError(t, err)
	require.NotNil(t, n.Stats)
	assert.InDelta(t, 42, n.Stats.CPUPercent, 0)
	assert.Equal(t, int64(3<<30), n.Stats.MemoryUsedBytes)
	assert.Equal(t, int64(20<<30), n.Stats.DiskUsedBytes)
	assert.InDelta(t, 1.5, n.Stats.Load1, 0)
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

// Scenario: the control plane tells a node which tools to have, for the
// node's own system, at hello and with every heartbeat's answer; what the
// node says it has, or why an install failed, is kept with the node.
func TestANodeIsToldWhichToolsToHave(t *testing.T) {
	t.Parallel()

	p := newPlane(t)

	gws := nodeproto.Tool{
		Name: "gws", Version: "0.22.5", URL: "https://example.com/gws-arm64.tar.gz",
		SHA256: strings.Repeat("ab", 32), Binary: "gws",
	}

	p.api.WithTools(func(_ context.Context, os, arch string) []nodeproto.Tool {
		if os == "linux" && arch == "arm64" {
			return []nodeproto.Tool{gws}
		}

		return nil
	})

	status, hello := p.hello(p.token)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, []nodeproto.Tool{gws}, hello.Tools)

	nodes, err := p.svc.ListNodes(t.Context())
	require.NoError(t, err)
	assert.Nil(t, nodes[0].Tools, "the node has not said yet")

	conn := p.connect()

	failed := []nodeproto.ToolState{{Name: "gws", Error: "fetching tool gws 0.22.5: status 503"}}
	p.send(conn, nodeproto.TypeHeartbeat, nodeproto.Heartbeat{Tools: &failed})

	var ack nodeproto.HeartbeatAck
	require.NoError(t, json.Unmarshal(p.next(conn, nodeproto.TypeHeartbeatAck).Payload, &ack))
	assert.Equal(t, []nodeproto.Tool{gws}, ack.Tools)

	nodes, err = p.svc.ListNodes(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []orchestration.NodeTool{{Name: "gws", Error: "fetching tool gws 0.22.5: status 503"}},
		nodes[0].Tools)

	installed := []nodeproto.ToolState{{Name: "gws", Version: "0.22.5"}}
	p.send(conn, nodeproto.TypeHeartbeat, nodeproto.Heartbeat{Tools: &installed})
	p.next(conn, nodeproto.TypeHeartbeatAck)

	// A heartbeat that says nothing about tools leaves what the node said.
	p.send(conn, nodeproto.TypeHeartbeat, nodeproto.Heartbeat{})
	p.next(conn, nodeproto.TypeHeartbeatAck)

	nodes, err = p.svc.ListNodes(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []orchestration.NodeTool{{Name: "gws", Version: "0.22.5"}}, nodes[0].Tools)
}

// Scenario: the owner gives a Run his work Google account. Its Task waits
// while no node has Google's tool; once the node says it installed it, the
// Task is offered, and the offer names the account and carries this
// Dispatch's own key, which orchestration never holds. A Run cannot be given
// an account that does not exist, and a coordinator can give only an account
// the owner allowed for chats.
func TestARunsAccountsReachItsWorkerWithAKeyOfItsOwn(t *testing.T) {
	t.Parallel()

	p := newPlane(t)
	ctx := t.Context()

	p.api.WithAgentKeys(func(ctx context.Context, dispatchID string, uses []string) (string, bool) {
		key, err := p.svc.AddAgentSession(ctx, dispatchID, uses)

		return key, err == nil
	})

	_, err := p.svc.SaveServiceConnection(ctx, orchestration.Connection{
		Integration: "google", Label: "work", Account: "owner@example.com", Secret: "conn-google-work",
		Services: []string{"gmail:read"},
	})
	require.NoError(t, err)

	_, err = p.svc.CreateRun(ctx, orchestration.NewRun{
		Objective: "o", CreatedBy: "owner", Uses: []string{"google:nobody"},
	})
	require.ErrorIs(t, err, orchestration.ErrNotFound)

	pi, _, err := p.svc.AddToken(ctx, "pi", bearertoken.Coordinator)
	require.NoError(t, err)

	_, err = p.svc.CreateRun(ctx, orchestration.NewRun{
		Objective: "o", CreatedBy: "pi", CoordinatorTokenID: pi.ID, Uses: []string{"google:work"},
	})
	require.ErrorIs(t, err, orchestration.ErrInvalid, "the coordinator may not use it, so it cannot pass it on")

	_, err = p.svc.SetConnectionChats(ctx, "google", "work", true)
	require.NoError(t, err)

	_, err = p.svc.CreateRun(ctx, orchestration.NewRun{
		Objective: "o", CreatedBy: "pi", CoordinatorTokenID: pi.ID, Uses: []string{"google:work"},
	})
	require.NoError(t, err)

	run, err := p.svc.CreateRun(ctx, orchestration.NewRun{
		Objective: "what needs an answer in my mail today", CreatedBy: "owner", Uses: []string{"google:work"},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"google:work"}, run.Uses)

	_, err = p.svc.CreateTasks(ctx, run.ID, []orchestration.NewTask{
		{Ref: "a", Profile: "implementer", Spec: "list today's unread mail"},
	})
	require.NoError(t, err)

	conn := p.connect()

	sch, err := p.svc.Schedule(ctx)
	require.NoError(t, err)
	assert.Empty(t, sch.Created, "no node has gws yet: the Task waits")

	failed := []nodeproto.ToolState{{Name: "gws", Error: "fetching tool gws 0.22.5: status 503"}}
	p.send(conn, nodeproto.TypeHeartbeat, nodeproto.Heartbeat{Tools: &failed})
	p.next(conn, nodeproto.TypeHeartbeatAck)

	sch, err = p.svc.Schedule(ctx)
	require.NoError(t, err)
	assert.Empty(t, sch.Created, "a failed install is not the tool")

	installed := []nodeproto.ToolState{{Name: "gws", Version: "0.22.5"}}
	p.send(conn, nodeproto.TypeHeartbeat, nodeproto.Heartbeat{Tools: &installed})
	p.next(conn, nodeproto.TypeHeartbeatAck)

	sch, err = p.svc.Schedule(ctx)
	require.NoError(t, err)
	require.Len(t, sch.Created, 1)

	var offer nodeproto.Offer
	require.NoError(t, json.Unmarshal(p.next(conn, nodeproto.TypeOffer).Payload, &offer))
	assert.Equal(t, []string{"google:work"}, offer.Uses)
	require.NotEmpty(t, offer.AgentKey)

	session, err := p.svc.AgentSessionOf(ctx, offer.AgentKey)
	require.NoError(t, err)
	assert.Equal(t, offer.DispatchID, session.DispatchID)
	assert.Equal(t, []string{"google:work"}, session.Uses)
	assert.False(t, session.ByCoordinator, "the owner created this Run: it keeps what he gave it")

	stored, err := p.svc.NodePending(ctx, p.node.ID)
	require.NoError(t, err)
	assert.Empty(t, stored.Offers[0].AgentKey, "orchestration never holds the key")

	// When the Dispatch is over its key stops working.
	require.NoError(t, p.svc.RevokeAgentSessions(ctx, offer.DispatchID))

	_, err = p.svc.AgentSessionOf(ctx, offer.AgentKey)
	require.ErrorIs(t, err, orchestration.ErrNotFound)
}

// Scenario: the account a Task runs under is a token the control plane keeps
// (issue #150). The offer carries the token, read as the offer is sent; an
// account a node holds gets none.
func TestAnOfferCarriesTheAccountsToken(t *testing.T) {
	t.Parallel()

	p := newPlane(t)

	var asked []string

	p.api.WithAccountTokens(func(_ context.Context, accountID string) (string, bool) {
		asked = append(asked, accountID)

		return "sk-ant-oat01-kept", accountID == p.account.ID
	})

	conn := p.connect()
	p.task()

	var offer nodeproto.Offer
	require.NoError(t, json.Unmarshal(p.next(conn, nodeproto.TypeOffer).Payload, &offer))
	assert.Equal(t, p.account.ID, offer.AccountRef)
	assert.Equal(t, "sk-ant-oat01-kept", offer.AccountToken)
	assert.Equal(t, []string{p.account.ID}, asked)

	other := newPlane(t)
	other.api.WithAccountTokens(func(context.Context, string) (string, bool) { return "", false })

	conn = other.connect()
	other.task()

	var held nodeproto.Offer
	require.NoError(t, json.Unmarshal(other.next(conn, nodeproto.TypeOffer).Payload, &held))
	assert.Empty(t, held.AccountToken)
}
