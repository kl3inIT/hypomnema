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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/profilestore"
	"github.com/kl3inIT/hypomnema/internal/shared/githost"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/skillpack"
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
	reports int
	// tempIsolation is what the node says at hello of its workers' temp isolation (issue #895).
	tempIsolation *nodeproto.TempIsolation
	// questionParts is what the node says at hello: it tells its worker every decision of a question (issue #1219).
	questionParts bool
	// githubApp is what the node says at hello: it gives its workers fresh tokens of the owner's GitHub App (issue
	// #1262).
	githubApp bool
	// memoryLimit is what the node says at hello of its workers' memory ceiling, and cpus and memoryBytes its size
	// (issue #1313).
	memoryLimit *nodeproto.MemoryLimit
	cpus        int
	memoryBytes int64
}

func newPlane(t *testing.T) *plane {
	t.Helper()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	svc := orchestration.New(db, platform.SystemClock{}, platform.NewID)

	// A Claude Code worker runs through the model gateway (issue #381).
	_, err = svc.Profiles().ApplyProvider(t.Context(), "",
		"name: gateway\nkind: model_gateway\nmodels: [claude-opus-5-5]\n")
	require.NoError(t, err)
	_, _, err = svc.Profiles().ApplyProfile(t.Context(), "",
		"name: implementer\ndescription: d\nruntime: claude\nmodel: {provider: gateway, name: claude-opus-5-5}\n")
	require.NoError(t, err)

	mux := http.NewServeMux()
	api := New(svc, slog.New(slog.DiscardHandler))
	api.Register(mux)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	n, token, err := svc.AddNode(t.Context(), "node_a", 2)
	require.NoError(t, err)

	return &plane{t: t, svc: svc, api: api, srv: srv, node: n, token: token}
}

func (p *plane) hello(token string, claims ...nodeproto.Claim) (int, nodeproto.HelloResponse) {
	p.t.Helper()

	body, err := json.Marshal(nodeproto.HelloRequest{
		InstanceID: "inst_1", DaemonVersion: "test", WorktreeRoot: "/srv/wt",
		Runtimes: []nodeproto.RuntimeInfo{{Runtime: "claude", Path: "/usr/local/bin/claude", Version: "2.1.319"}},
		System:   nodeproto.SystemInfo{OS: "linux", Arch: "arm64", CPUs: p.cpus, MemoryBytes: p.memoryBytes},
		Claims:   claims, TempIsolation: p.tempIsolation, QuestionParts: p.questionParts, GitHubApp: p.githubApp,
		MemoryLimit: p.memoryLimit,
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

func TestOversizedCleanupReplyAsksNodeToSplit(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("d", nodeproto.MaxMessageBytes/2)
	trees := []nodeproto.CleanupWorktree{{Dispatches: []string{long}}, {Dispatches: []string{long}}}
	protected := []string{"/srv/wt/open/task"}
	assert.Equal(t, nodeproto.CleanupAck{ID: "check", Split: true}, cleanupReply("check", trees, protected))
	assert.Equal(t, nodeproto.CleanupAck{ID: "half", Worktrees: trees[:1], Protected: &protected},
		cleanupReply("half", trees[:1], protected))
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
	p.api.WithAgentKeys(func(ctx context.Context, dispatchID string, uses []string) (string, bool) {
		key, err := p.svc.AddAgentSession(ctx, dispatchID, uses)

		return key, err == nil
	})
	// The tools the worker gets, read with its own key, for the "Tools" block of its rules (issues #723, #789).
	keyed := make(chan string, 8)

	p.api.WithToolNames(func(_ context.Context, dispatchID, key string) []string {
		keyed <- key

		return []string{"web_fetch", "tools of " + dispatchID}
	})

	conn := p.connect()
	task := p.task()

	var offer nodeproto.Offer
	require.NoError(t, json.Unmarshal(p.next(conn, nodeproto.TypeOffer).Payload, &offer))
	assert.Equal(t, task.ID, offer.TaskID)
	assert.Equal(t, []string{"web_fetch", "tools of " + offer.DispatchID}, offer.Tools)
	assert.Equal(t, offer.AgentKey, <-keyed, "the tools are read with the key the offer carries")

	// Its Run was given no account, and the worker still has a key of its own: the shared browser knows it by it.
	session, err := p.svc.AgentSessionOf(t.Context(), offer.AgentKey)
	require.NoError(t, err)
	assert.Equal(t, offer.DispatchID, session.DispatchID)
	assert.Empty(t, session.Uses)
	assert.Equal(t, "add the Google login button", offer.Spec)
	assert.Equal(t, "kl3inIT/second-bite", offer.Repo)
	assert.Equal(t, orchestration.DefaultBase, offer.Base)
	assert.Equal(t, "/srv/wt/"+task.RunID+"/"+task.ID, offer.Worktree)
	assert.Equal(t, "claude", offer.Runtime)
	assert.Equal(t, profilestore.GatewayAccountRef, offer.AccountRef)
	assert.Equal(t, "claude-opus-5-5", offer.Model)

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

// Scenario (issue #991): the first offer after a token is given, while its account is still being read. The offer is
// held, and the node's heartbeats are answered meanwhile; once the read ends the offer goes out with the account.
func TestAnOfferWaitsForItsTokensAccount(t *testing.T) {
	t.Parallel()

	p := newPlane(t)

	pending := make(chan struct{})
	var read atomic.Bool

	p.api.WithHostTokens(func(context.Context, string, string) (HostCredential, bool) {
		if !read.Load() {
			return HostCredential{Token: "ghp_new", Pending: pending}, true
		}

		return HostCredential{
			Token: "ghp_new", Identity: githost.Identity{Name: "octo", Email: "1+octo@users.noreply.github.com"},
		}, true
	})

	conn := p.connect()
	p.task()

	p.send(conn, nodeproto.TypeHeartbeat, nodeproto.Heartbeat{})

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	for {
		var msg nodeproto.Message
		require.NoError(t, wsjson.Read(ctx, conn, &msg))
		require.NotEqual(t, nodeproto.TypeOffer, msg.Type, "the offer is held while its account is read")

		if msg.Type == nodeproto.TypeHeartbeatAck {
			break
		}
	}

	read.Store(true)
	close(pending)

	var offer nodeproto.Offer
	require.NoError(t, json.Unmarshal(p.next(conn, nodeproto.TypeOffer).Payload, &offer))
	assert.Equal(t, [3]string{"ghp_new", "octo", "1+octo@users.noreply.github.com"},
		[3]string{offer.RepoToken, offer.GitName, offer.GitEmail})
}

// Scenario (issue #991, review round 3): the token is replaced while its offer waits for the old token's account.
// The old read ending does not end the offer's wait: the offer goes out with the new token only once the new
// token's account is read, and carries that account.
func TestReviewPendingOfferTokenRotation(t *testing.T) {
	t.Parallel()

	p := newPlane(t)

	oldRead, newRead, askedNew := make(chan struct{}), make(chan struct{}), make(chan struct{}, 1)

	var state atomic.Int32 // 0: the old token, being read; 1: the new token, being read; 2: the new token, read

	p.api.WithHostTokens(func(context.Context, string, string) (HostCredential, bool) {
		switch state.Load() {
		case 0:
			return HostCredential{Token: "ghp_old", Pending: oldRead}, true
		case 1:
			select {
			case askedNew <- struct{}{}:
			default:
			}

			return HostCredential{Token: "ghp_new", Pending: newRead}, true
		default:
			return HostCredential{
				Token: "ghp_new", Identity: githost.Identity{Name: "hubot", Email: "2+hubot@users.noreply.github.com"},
			}, true
		}
	})

	conn := p.connect()
	p.task()

	// The offer is held for the old token's account; then the token is replaced and the old read ends.
	heartbeat := func() {
		p.send(conn, nodeproto.TypeHeartbeat, nodeproto.Heartbeat{})

		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()

		for {
			var msg nodeproto.Message
			require.NoError(t, wsjson.Read(ctx, conn, &msg))
			require.NotEqual(t, nodeproto.TypeOffer, msg.Type, "the offer is held while its account is read")

			if msg.Type == nodeproto.TypeHeartbeatAck {
				return
			}
		}
	}

	heartbeat()
	state.Store(1)
	close(oldRead)

	select {
	case <-askedNew:
	case <-time.After(5 * time.Second):
		t.Fatal("the offer was not looked at again after the old read ended")
	}

	// The end of the old read may reach the session after a heartbeat: the offer must stay unsent for a while, with
	// the new read still pending, not only until the next heartbeat.
	for end := time.Now().Add(300 * time.Millisecond); time.Now().Before(end); {
		heartbeat()
	}

	state.Store(2)
	close(newRead)

	var offer nodeproto.Offer
	require.NoError(t, json.Unmarshal(p.next(conn, nodeproto.TypeOffer).Payload, &offer))
	assert.Equal(t, [3]string{"ghp_new", "hubot", "2+hubot@users.noreply.github.com"},
		[3]string{offer.RepoToken, offer.GitName, offer.GitEmail})
}

// Scenario (issue #1262): the owner's GitHub App is installed on the Task's repository. A node that said at hello
// that it asks for the App's token at each use gets an offer marked as the App's and carrying no token, so none is
// kept with the offer on the node (review round 1); a node of an earlier release, which would need a token in the
// offer for the whole Dispatch, gets the owner's, as before.
func TestAnOfferCarriesTheAppsTokenOnlyToANodeThatRefreshesIt(t *testing.T) {
	t.Parallel()

	for _, refreshes := range []bool{true, false} {
		p := newPlane(t)
		p.githubApp = refreshes

		p.api.WithHostTokens(func(context.Context, string, string) (HostCredential, bool) {
			return HostCredential{ //nolint:gosec // a test's made-up token
				Token: "ghp_the_owners", Identity: githost.Identity{Name: "octo", Email: "1+octo@users.noreply.github.com"},
			}, true
		}).WithGitHubApp(func(_ context.Context, host, path string) bool {
			return host == "github.com" && path == "kl3inIT/second-bite"
		})

		conn := p.connect()
		p.task()

		var offer nodeproto.Offer
		require.NoError(t, json.Unmarshal(p.next(conn, nodeproto.TypeOffer).Payload, &offer))

		if refreshes {
			assert.Empty(t, offer.RepoToken, "the node asks for the App's token at each use")
			assert.True(t, offer.GitHubApp)
		} else {
			assert.Equal(t, "ghp_the_owners", offer.RepoToken)
			assert.False(t, offer.GitHubApp)
		}

		assert.Equal(t, "octo", offer.GitName, "the commits still name the owner's account")
	}
}

// Scenario: the owner gave github.com a token. The offer of a Task on GitHub
// carries it, read as the offer is sent; a host without a token, or a control
// plane that keeps none, sends the offer without one.
func TestAnOfferCarriesItsHostsToken(t *testing.T) {
	t.Parallel()

	p := newPlane(t)

	asked := make(chan string, 4)
	p.api.WithHostTokens(func(_ context.Context, host, path string) (HostCredential, bool) {
		asked <- host + " " + path

		return HostCredential{ //nolint:gosec // a test's made-up token
			Token:    "ghp_for_this_dispatch",
			Identity: githost.Identity{Name: "octo", Email: "1+octo@users.noreply.github.com"},
		}, host == "github.com"
	})

	conn := p.connect()
	p.task()

	var offer nodeproto.Offer
	require.NoError(t, json.Unmarshal(p.next(conn, nodeproto.TypeOffer).Payload, &offer))
	assert.Equal(t, "github.com kl3inIT/second-bite", <-asked, "asked by host and repository")
	assert.Equal(t, [3]string{"github.com", "github", "ghp_for_this_dispatch"},
		[3]string{offer.RepoHost, offer.RepoKind, offer.RepoToken})
	assert.Equal(t, [2]string{"octo", "1+octo@users.noreply.github.com"}, [2]string{offer.GitName, offer.GitEmail},
		"with the account the token belongs to (issue #991)")

	stored, err := p.svc.NodePending(t.Context(), p.node.ID)
	require.NoError(t, err)
	require.Len(t, stored.Offers, 1)
	assert.Empty(t, stored.Offers[0].RepoToken, "orchestration never holds the token")

	// Another connection of the node is told again; a host that has no token gives an offer without one.
	p.api.WithHostTokens(func(context.Context, string, string) (HostCredential, bool) {
		return HostCredential{}, false
	})

	again := p.connect()

	var second nodeproto.Offer
	require.NoError(t, json.Unmarshal(p.next(again, nodeproto.TypeOffer).Payload, &second))
	assert.Equal(t, offer.DispatchID, second.DispatchID)
	assert.Empty(t, second.RepoToken)
	assert.Empty(t, second.GitName+second.GitEmail, "no token, no account")
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

// Scenario: a worker asks two decisions at once, with headers and options
// that say what they change (issue #1219). The control plane keeps them, and
// says so in the offer. A node that said at hello that it reads several
// decisions is pushed the answer with each decision's choice. A node of an
// earlier release, which tells its worker only a question's text and one
// answer, is pushed the question folded into one, every decision in its text
// and every answer in its answer's text, through the push and the
// heartbeat's reply alike, so none is lost (review of PR #1228).
func TestAQuestionOfTwoDecisionsCrossesTheWire(t *testing.T) {
	t.Parallel()

	// before is a Question as a node before issue #1219 reads it.
	type before struct {
		Text    string `json:"text"`
		Options []struct {
			Label string `json:"label"`
		} `json:"options"`
		Answer struct {
			Option *int   `json:"option"`
			Text   string `json:"text"`
		} `json:"answer"`
	}

	for _, current := range []bool{true, false} {
		t.Run(map[bool]string{true: "a node that reads them", false: "a node of an earlier release"}[current],
			func(t *testing.T) {
				t.Parallel()

				p := newPlane(t)
				p.questionParts = current
				conn := p.connect()
				p.task()

				var offer nodeproto.Offer
				require.NoError(t, json.Unmarshal(p.next(conn, nodeproto.TypeOffer).Payload, &offer))
				assert.True(t, offer.QuestionParts, "this control plane says it keeps several decisions")
				require.Equal(t, nodeproto.AckCommitted,
					p.report(conn, nodeproto.Report{Kind: nodeproto.ReportAccept, DispatchID: offer.DispatchID}).Status)
				require.Equal(t, nodeproto.AckCommitted, p.report(conn, nodeproto.Report{
					Kind: nodeproto.ReportStarted, DispatchID: offer.DispatchID, Epoch: 1,
					Payload: payload(t, nodeproto.Started{ProcessRef: "pid:42@1", CLISessionRef: "sess_1"}),
				}).Status)

				options := []nodeproto.Option{
					{Label: "goth", Description: "A library. From: Gitea."}, {Label: "Other", AllowsText: true},
				}
				ack := p.report(conn, nodeproto.Report{
					Kind: nodeproto.ReportQuestion, DispatchID: offer.DispatchID, Epoch: 1,
					Payload: payload(t, nodeproto.Ask{
						Header: "OAuth", Text: "goth or hand-written OAuth?", Options: options, To: "owner",
						More: []nodeproto.Part{{Header: "Scope", Text: "Ask for email too?", Options: options}},
					}),
				})
				require.Equal(t, nodeproto.AckCommitted, ack.Status, ack.Error)

				var asked struct {
					QuestionID string `json:"question_id"`
				}
				require.NoError(t, json.Unmarshal(ack.Response, &asked))

				stored, err := p.svc.GetQuestion(t.Context(), asked.QuestionID)
				require.NoError(t, err)
				assert.Equal(t, "OAuth", stored.Header)
				assert.Equal(t, "A library. From: Gitea.", stored.Options[0].Description)
				require.Len(t, stored.More, 1)
				assert.Equal(t, "Scope", stored.More[0].Header)

				first, other := 0, 1
				_, err = p.svc.AnswerQuestion(t.Context(), asked.QuestionID, orchestration.AddressOwner,
					orchestration.Answer{Option: &first, More: []orchestration.Choice{{Option: &other, Text: "only the name"}}})
				require.NoError(t, err)

				pushed := p.next(conn, nodeproto.TypeAnswer).Payload
				beat := p.report(conn, nodeproto.Report{
					Kind: nodeproto.ReportHeartbeat, DispatchID: offer.DispatchID, Epoch: 1,
				})
				require.Equal(t, nodeproto.AckCommitted, beat.Status, beat.Error)

				var replied struct {
					Answers []json.RawMessage `json:"answers"`
				}
				require.NoError(t, json.Unmarshal(beat.Response, &replied))
				require.Len(t, replied.Answers, 1)

				for _, raw := range []json.RawMessage{pushed, replied.Answers[0]} {
					if current {
						var answer nodeproto.Question
						require.NoError(t, json.Unmarshal(raw, &answer))
						assert.Equal(t, []nodeproto.Part{{Header: "Scope", Text: "Ask for email too?", Options: options}},
							answer.More)
						assert.Equal(t, &nodeproto.Answer{
							Option: &first, More: []nodeproto.Choice{{Option: &other, Text: "only the name"}},
						}, answer.Answer)

						continue
					}

					var old before
					require.NoError(t, json.Unmarshal(raw, &old))
					assert.Equal(t, "1. OAuth: goth or hand-written OAuth?\n2. Scope: Ask for email too?", old.Text)
					assert.Empty(t, old.Options, "nothing to pick: the answer is the text")
					assert.Nil(t, old.Answer.Option)
					assert.Equal(t, "1. OAuth: goth\n2. Scope: Other only the name", old.Answer.Text)
				}
			})
	}
}

// Scenario: review of PR #1228. A resolved Gate of several decisions is sent to the node that takes the next
// Dispatch: whole to a node that reads them, folded into its text and its answer's text for one that does not, and a
// Gate of one decision as it is to both.
func TestAResolvedGateReachesANodeOfEitherRelease(t *testing.T) {
	t.Parallel()

	s := &Server{questionParts: map[string]bool{"node_new": true}}
	first, second := 0, 1
	options := []orchestration.Option{{Label: "Yes", Description: "Do it now."}, {Label: "No"}}
	gate := orchestration.Question{
		ID: "qst_1", Mode: orchestration.QuestionGate, Header: "Items", Text: "One item each?", Options: options,
		More:   []orchestration.Part{{Header: "Server", Text: "Fix it now?", Options: options}},
		Answer: &orchestration.Answer{Option: &first, More: []orchestration.Choice{{Option: &second}}},
	}

	whole := s.acceptedFor("node_new", orchestration.Accepted{Epoch: 2, Gate: &gate})
	assert.Equal(t, &gate, whole.Gate)

	old := s.acceptedFor("node_old", orchestration.Accepted{Epoch: 2, Gate: &gate})
	require.NotNil(t, old.Gate)
	assert.Equal(t, "1. Items: One item each?\n2. Server: Fix it now?", old.Gate.Text)
	assert.Equal(t, &orchestration.Answer{Text: "1. Items: Yes\n2. Server: No"}, old.Gate.Answer)
	assert.Equal(t, 2, old.Epoch)

	one := gate
	one.More, one.Answer = nil, &orchestration.Answer{Option: &first}
	assert.Equal(t, &one, s.acceptedFor("node_old", orchestration.Accepted{Gate: &one}).Gate)
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

	stale := p.report(conn, nodeproto.Report{Kind: nodeproto.ReportHeartbeat, DispatchID: offer.DispatchID, Epoch: 7})
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

// Scenario: a node still below its disk floor after trimming says so in its heartbeats (issue #1017). The owner is
// told once for the episode and once when it recovers.
func TestBlockedDiskInHeartbeatsTellsTheOwnerOncePerEpisode(t *testing.T) {
	t.Parallel()

	p := newPlane(t)
	alerts := make(chan orchestration.DiskAlert, 8)
	p.api.WithDiskAlerts(func(_ context.Context, alert orchestration.DiskAlert) { alerts <- alert })
	conn := p.connect()
	beat := func(free int64, blocked bool) {
		p.send(conn, nodeproto.TypeHeartbeat, nodeproto.Heartbeat{Disk: &nodeproto.DiskSpace{
			FreeBytes: &free, FloorBytes: 5 << 30, TrimBytes: 15 << 30, Blocked: blocked,
		}})
		p.next(conn, nodeproto.TypeHeartbeatAck)
	}

	for range 3 {
		beat(1<<30, true)
	}
	beat(20<<30, false)

	told := []orchestration.DiskAlert{<-alerts, <-alerts}
	assert.Equal(t, orchestration.DiskAlert{NodeID: p.node.ID, Node: p.node.Name, FreeBytes: 1 << 30,
		FloorBytes: 5 << 30}, told[0])
	assert.True(t, told[1].Recovered)
	assert.Empty(t, alerts, "the episode is told once")
}

// Scenario: telling the owner of a drop is slow, and the node recovers meanwhile (review round 2 of #1017). He is
// told of the drop first and of the recovery after it.
func TestDiskAlertsOfANodeAreToldInOrder(t *testing.T) {
	t.Parallel()

	p := newPlane(t)
	release := make(chan struct{})
	var mu sync.Mutex
	var told []bool
	done := make(chan struct{}, 2)
	p.api.WithDiskAlerts(func(_ context.Context, alert orchestration.DiskAlert) {
		if !alert.Recovered {
			<-release
		}
		mu.Lock()
		told = append(told, alert.Recovered)
		mu.Unlock()
		done <- struct{}{}
	})
	conn := p.connect()
	beat := func(free int64, blocked bool) {
		p.send(conn, nodeproto.TypeHeartbeat, nodeproto.Heartbeat{Disk: &nodeproto.DiskSpace{
			FreeBytes: &free, FloorBytes: 5 << 30, TrimBytes: 15 << 30, Blocked: blocked,
		}})
		p.next(conn, nodeproto.TypeHeartbeatAck)
	}

	beat(1<<30, true)
	beat(20<<30, false)
	select {
	case <-done:
		close(release)
		t.Fatal("the recovery was told while the drop was still being told")
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	<-done
	<-done

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []bool{false, true}, told, "the drop, then the recovery")
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

// Scenario: a node says at hello that its workers' temp isolation is off, and why (issue #895). The control
// plane keeps what it said; a node of an earlier release, which says nothing, is shown saying nothing.
func TestHelloKeepsWhatTheNodeSaysOfTempIsolation(t *testing.T) {
	t.Parallel()

	p := newPlane(t)
	p.tempIsolation = &nodeproto.TempIsolation{Reason: "bubblewrap is not installed"}
	status, _ := p.hello(p.token)
	require.Equal(t, http.StatusOK, status)

	n, err := p.svc.GetNode(t.Context(), p.node.ID)
	require.NoError(t, err)
	assert.Equal(t, &orchestration.TempIsolation{Reason: "bubblewrap is not installed"}, n.TempIsolation)

	p.tempIsolation = &nodeproto.TempIsolation{On: true}
	status, _ = p.hello(p.token)
	require.Equal(t, http.StatusOK, status)
	n, err = p.svc.GetNode(t.Context(), p.node.ID)
	require.NoError(t, err)
	assert.Equal(t, &orchestration.TempIsolation{On: true}, n.TempIsolation)

	p.tempIsolation = nil
	status, _ = p.hello(p.token)
	require.Equal(t, http.StatusOK, status)
	n, err = p.svc.GetNode(t.Context(), p.node.ID)
	require.NoError(t, err)
	assert.Nil(t, n.TempIsolation)
}

// Scenario: a node says at hello whether its workers run under a memory ceiling, and why not (issue #1313 row 6).
// The control plane keeps what it said; a node of an earlier release, which says nothing, is shown saying nothing.
func TestHelloKeepsWhatTheNodeSaysOfItsMemoryLimit(t *testing.T) {
	t.Parallel()

	p := newPlane(t)
	p.memoryLimit = &nodeproto.MemoryLimit{Reason: "the systemd user manager does not delegate the memory controller"}
	status, _ := p.hello(p.token)
	require.Equal(t, http.StatusOK, status)

	n, err := p.svc.GetNode(t.Context(), p.node.ID)
	require.NoError(t, err)
	assert.Equal(t, &orchestration.MemoryLimit{
		Reason: "the systemd user manager does not delegate the memory controller",
	}, n.MemoryLimit)

	p.memoryLimit = &nodeproto.MemoryLimit{On: true}
	status, _ = p.hello(p.token)
	require.Equal(t, http.StatusOK, status)
	n, err = p.svc.GetNode(t.Context(), p.node.ID)
	require.NoError(t, err)
	assert.Equal(t, &orchestration.MemoryLimit{On: true}, n.MemoryLimit)

	p.memoryLimit = nil
	status, _ = p.hello(p.token)
	require.Equal(t, http.StatusOK, status)
	n, err = p.svc.GetNode(t.Context(), p.node.ID)
	require.NoError(t, err)
	assert.Nil(t, n.MemoryLimit)
}

// Scenario: the control plane tells a node the memory all its workers may use, its allocatable memory (its memory
// less the owner's reserve and the 1 GiB floor), at hello and with each heartbeat's answer, so a reserve the owner
// changes reaches the node without a new hello (issue #1313 row 2). A node that never said its memory gets none.
func TestANodeIsToldItsWorkersMemoryCeiling(t *testing.T) {
	t.Parallel()

	p := newPlane(t)

	status, hello := p.hello(p.token)
	require.Equal(t, http.StatusOK, status)
	require.NotNil(t, hello.WorkerMemoryMax)
	assert.Zero(t, *hello.WorkerMemoryMax, "a node that never said its memory has no ceiling")

	p.cpus, p.memoryBytes = 8, 16<<30
	status, hello = p.hello(p.token)
	require.Equal(t, http.StatusOK, status)
	require.NotNil(t, hello.WorkerMemoryMax)
	assert.Equal(t, int64(15<<30), *hello.WorkerMemoryMax)

	n, err := p.svc.GetNode(t.Context(), p.node.ID)
	require.NoError(t, err)
	assert.Equal(t, n.Allocatable.MemoryBytes, *hello.WorkerMemoryMax, "the ceiling is the node's allocatable memory")

	conn := p.connect()

	reserve := int64(4 << 30)
	_, err = p.svc.UpdateNode(t.Context(), p.node.ID, orchestration.NodeChange{ReservedMemoryBytes: &reserve})
	require.NoError(t, err)

	p.send(conn, nodeproto.TypeHeartbeat, nodeproto.Heartbeat{})

	var ack nodeproto.HeartbeatAck
	require.NoError(t, json.Unmarshal(p.next(conn, nodeproto.TypeHeartbeatAck).Payload, &ack))
	require.NotNil(t, ack.WorkerMemoryMax)
	assert.Equal(t, int64(11<<30), *ack.WorkerMemoryMax)
}

// Scenario: a node says hello. The control plane names where the node's
// releases are and its own version, so a node older than it looks for its
// release at once (issue #783).
func TestHelloSaysTheReleaseAndTheControlPlanesVersion(t *testing.T) {
	t.Parallel()

	p := newPlane(t)
	p.api.WithReleaseURL("")

	status, out := p.hello(p.token)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, DefaultReleaseURL, out.ReleaseURL)
	assert.Equal(t, platform.Version, out.ControlPlaneVersion)
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

// A node is told to have the agent CLIs before the Integrations' tools (issue #107).
func TestANodeIsToldToHaveTheAgentCLIsFirst(t *testing.T) {
	t.Parallel()

	omp := nodeproto.Tool{Name: "omp", Version: "18.18.6"}
	gws := nodeproto.Tool{Name: "gws", Version: "0.22.5"}

	s := New(nil, nil).
		WithTools(func(context.Context, string, string) []nodeproto.Tool { return []nodeproto.Tool{gws} }).
		WithRuntimeTools(func(string, string) []nodeproto.Tool { return []nodeproto.Tool{omp} })

	assert.Equal(t, []nodeproto.Tool{omp, gws}, s.toolsFor(t.Context(), "linux", "amd64"))
}

// Scenario: a node installs the agent CLIs the control plane named, then says the runtimes it now has in a
// heartbeat; the node's runtimes are those from then on (issue #107). A heartbeat that says none leaves them.
func TestANodeSaysTheRuntimesItInstalled(t *testing.T) {
	t.Parallel()

	p := newPlane(t)

	status, _ := p.hello(p.token)
	require.Equal(t, http.StatusOK, status)

	conn := p.connect()

	runtimes := []nodeproto.RuntimeInfo{{Runtime: "omp", Path: "/home/n/tools/omp/18.18.6/omp", Version: "18.18.6"}}
	p.send(conn, nodeproto.TypeHeartbeat, nodeproto.Heartbeat{Runtimes: &runtimes})
	p.next(conn, nodeproto.TypeHeartbeatAck)

	p.send(conn, nodeproto.TypeHeartbeat, nodeproto.Heartbeat{})
	p.next(conn, nodeproto.TypeHeartbeatAck)

	nodes, err := p.svc.ListNodes(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []orchestration.RuntimeInfo{
		{Runtime: "omp", Path: "/home/n/tools/omp/18.18.6/omp", Version: "18.18.6"},
	}, nodes[0].Runtimes)
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

// Scenario: a skill for every worker is offered with the Dispatch, and the
// node fetches its archive over HTTP with its token. Without the token, or
// for a digest no offer of the node names, nothing is served (issue #21).
func TestANodeFetchesTheSkillsItsOfferNames(t *testing.T) {
	t.Parallel()

	p := newPlane(t)
	ctx := t.Context()

	archive, err := skillpack.Skill{Files: []skillpack.File{
		{Path: "SKILL.md", Data: []byte("---\nname: review-go\ndescription: d.\n---\n")},
	}}.Archive()
	require.NoError(t, err)

	sk, err := skillpack.FromArchive(archive)
	require.NoError(t, err)

	_, _, err = p.svc.AddSkill(ctx, sk, orchestration.SkillSource{Source: orchestration.SkillUpload})
	require.NoError(t, err)
	_, err = p.svc.SetSkillEveryone(ctx, "review-go", true)
	require.NoError(t, err)

	conn := p.connect()
	p.task()

	var offer nodeproto.Offer
	require.NoError(t, json.Unmarshal(p.next(conn, nodeproto.TypeOffer).Payload, &offer))
	require.Equal(t, []nodeproto.Skill{{Name: "review-go", Revision: 1, Digest: sk.Digest}}, offer.Skills)

	get := func(token, digest string) (int, []byte) {
		t.Helper()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.srv.URL+nodeproto.PathSkills+digest, nil)
		require.NoError(t, err)

		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}

		resp, err := p.srv.Client().Do(req)
		require.NoError(t, err)

		defer func() { _ = resp.Body.Close() }()

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		return resp.StatusCode, body
	}

	status, body := get(p.token, sk.Digest)
	require.Equal(t, http.StatusOK, status)

	got, err := skillpack.FromArchive(body)
	require.NoError(t, err)
	assert.Equal(t, sk.Digest, got.Digest)

	status, _ = get("", sk.Digest)
	assert.Equal(t, http.StatusUnauthorized, status)

	status, _ = get(p.token, strings.Repeat("0", 64))
	assert.Equal(t, http.StatusNotFound, status)
}

// Scenario: a Run given a Superset account is offered the Superset guide, a
// skill that requires its Integration (issue #536).
func TestAnOfferCarriesTheGuideOfItsIntegration(t *testing.T) {
	t.Parallel()

	p := newPlane(t)
	ctx := t.Context()

	archive, err := skillpack.Skill{Files: []skillpack.File{{Path: "SKILL.md", Data: []byte(
		"---\nname: superset\ndescription: d.\nmetadata:\n  hypomnema:\n    requires:\n      integration: superset\n" +
			"---\n")}}}.Archive()
	require.NoError(t, err)

	sk, err := skillpack.FromArchive(archive)
	require.NoError(t, err)

	_, err = p.svc.PublishBuiltinSkills(ctx, []skillpack.Skill{sk}, "builtin:v1")
	require.NoError(t, err)

	_, err = p.svc.SaveServiceConnection(ctx, orchestration.Connection{
		Integration: "superset", Label: "vmaps", Secret: "s",
	})
	require.NoError(t, err)

	run, err := p.svc.CreateRun(ctx, orchestration.NewRun{
		Objective: "read a chart", CreatedBy: "owner", Repo: "kl3inIT/second-bite", Uses: []string{"superset:vmaps"},
	})
	require.NoError(t, err)

	_, err = p.svc.CreateTasks(ctx, run.ID, []orchestration.NewTask{{Ref: "a", Profile: "implementer", Spec: "read it"}})
	require.NoError(t, err)

	conn := p.connect()

	sch, err := p.svc.Schedule(ctx)
	require.NoError(t, err)
	require.Len(t, sch.Created, 1)

	var offer nodeproto.Offer
	require.NoError(t, json.Unmarshal(p.next(conn, nodeproto.TypeOffer).Payload, &offer))
	assert.Equal(t, []string{"superset:vmaps"}, offer.Uses)
	assert.Equal(t, []nodeproto.Skill{
		{Name: "superset", Revision: 1, Digest: sk.Digest, Requires: []string{"integration:superset"}},
	}, offer.Skills)
}

// Scenario: telling the owner hangs while a node's disk keeps changing (fix round 4 of #1017). Its alerts do not
// pile up: what waits is the node's latest state, as client-go's workqueue keeps an item dirty once however often
// it is added while it is processed (util/workqueue/queue.go:210-221,243-264,306-319). The owner is told the drop
// in flight, then the latest state when it differs from what he was told.
func TestDiskAlertsOfANodeWaitAsItsLatestStateOnly(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		flips []bool
		told  []bool
	}{
		{name: "ends low", flips: []bool{true, false, true, false}, told: []bool{false}},
		{name: "ends recovered", flips: []bool{true, false, true}, told: []bool{false, true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			release := make(chan struct{})
			var mu sync.Mutex
			var told []bool
			s := New(nil, slog.New(slog.DiscardHandler)).WithDiskAlerts(
				func(_ context.Context, alert orchestration.DiskAlert) {
					if !alert.Recovered {
						<-release
					}
					mu.Lock()
					told = append(told, alert.Recovered)
					mu.Unlock()
				})
			queue := func(recovered bool) {
				s.diskReports.Lock()
				s.queueDiskAlert(t.Context(), "node_a", orchestration.DiskAlert{NodeID: "node_a", Recovered: recovered})
				s.diskReports.Unlock()
			}

			queue(false)
			for _, recovered := range tc.flips {
				queue(recovered)
			}
			s.diskReports.Lock()
			assert.LessOrEqual(t, len(s.diskAlerts["node_a"]), 2, "the alert in flight and the latest state")
			s.diskReports.Unlock()
			close(release)
			require.Eventually(t, func() bool {
				s.diskReports.Lock()
				defer s.diskReports.Unlock()
				_, busy := s.diskAlerts["node_a"]
				return !busy
			}, 5*time.Second, time.Millisecond, "the sender ends once the latest state is told")

			mu.Lock()
			defer mu.Unlock()
			assert.Equal(t, tc.told, told)
		})
	}
}
