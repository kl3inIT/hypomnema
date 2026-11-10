package clientapi

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/coordinator"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

// world is a control plane with the client API over a real database.
type world struct {
	t   *testing.T
	svc *orchestration.Service
	url string
	// stop is the server's shutdown signal.
	stop chan struct{}
	// workers are the Dispatches a test drives, by the id of a question they asked.
	workers map[string]*worker
	// pi plays the coordinator's supervisor.
	pi *fakeCoordinator
}

func newWorld(t *testing.T) *world {
	t.Helper()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: filepath.Join(t.TempDir(), "hypomnema.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, orchestration.Migrate(t.Context(), db))

	svc := orchestration.New(db, platform.SystemClock{}, platform.NewID)

	stop := make(chan struct{})
	pi := &fakeCoordinator{live: make(chan coordinator.Frame, 16)}
	srv := New(svc, slog.New(slog.DiscardHandler), stop).WithCoordinator(pi)
	srv.heartbeat = 50 * time.Millisecond

	mux := http.NewServeMux()
	require.NoError(t, srv.Register(mux))

	hs := httptest.NewServer(mux)
	t.Cleanup(hs.Close)
	t.Cleanup(func() {
		select {
		case <-stop:
		default:
			close(stop)
		}
	})

	return &world{t: t, svc: svc, url: hs.URL, stop: stop, workers: map[string]*worker{}, pi: pi}
}

// token adds a token and returns a client that sends it.
func (w *world) token(name string, kind bearertoken.Kind) (*openapi.ClientWithResponses, orchestration.Token) {
	w.t.Helper()

	tok, secret, err := w.svc.AddToken(w.t.Context(), name, kind)
	require.NoError(w.t, err)

	return w.client(secret), tok
}

func (w *world) client(secret string) *openapi.ClientWithResponses {
	w.t.Helper()

	c, err := openapi.NewClientWithResponses(w.url+BasePath, openapi.WithRequestEditorFn(
		func(_ context.Context, r *http.Request) error {
			if secret != "" {
				r.Header.Set("Authorization", "Bearer "+secret)
			}

			return nil
		}))
	require.NoError(w.t, err)

	return c
}

func (w *world) createRun(c *openapi.ClientWithResponses, objective string) openapi.Run {
	w.t.Helper()

	resp, err := c.CreateRunWithResponse(w.t.Context(), &openapi.CreateRunParams{},
		openapi.NewRun{Objective: objective, Repo: ptr("kl3inIT/demo")})
	require.NoError(w.t, err)
	require.Equal(w.t, http.StatusCreated, resp.StatusCode(), string(resp.Body))

	return *resp.JSON201
}

func errorCode(t *testing.T, body []byte, status, want int) openapi.ErrorCode {
	t.Helper()
	require.Equal(t, want, status, string(body))

	var e struct {
		Code openapi.ErrorCode `json:"code"`
	}
	require.NoError(t, json.Unmarshal(body, &e))

	return e.Code
}

// Every operation in the contract has a policy, and no policy names an
// operation that does not exist (Omnara's openapi_policies_test.go).
func TestEveryOperationHasAPolicy(t *testing.T) {
	t.Parallel()

	spec, err := openapi.GetSpec()
	require.NoError(t, err)

	ops := map[string]bool{}
	for _, item := range spec.Paths.Map() {
		for _, op := range item.Operations() {
			ops[op.OperationID] = true
			assert.Contains(t, policies, op.OperationID)
		}
	}

	for id := range policies {
		assert.True(t, ops[id], "policy for unknown operation %s", id)
	}
}

func TestRequestsNeedAValidToken(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	_, tok := w.token("laptop", bearertoken.Coordinator)

	unknown, err := bearertoken.Generate(bearertoken.Owner)
	require.NoError(t, err)

	revoked, _ := w.token("old", bearertoken.Owner)
	old, err := w.svc.ListTokens(t.Context())
	require.NoError(t, err)

	for _, tk := range old {
		if tk.Name == "old" {
			require.NoError(t, w.svc.RevokeToken(t.Context(), tk.ID))
		}
	}

	for name, c := range map[string]*openapi.ClientWithResponses{
		"none": w.client(""), "malformed": w.client("hyp_owner_v1_x"), "unknown": w.client(unknown), "revoked": revoked,
	} {
		resp, err := c.GetMeWithResponse(t.Context())
		require.NoError(t, err)
		assert.Equal(t, openapi.Unauthorized, errorCode(t, resp.Body, resp.StatusCode(), http.StatusUnauthorized), name)
		assert.Contains(t, resp.HTTPResponse.Header.Get("WWW-Authenticate"), "Bearer", name)
	}

	c, _ := w.token("web", bearertoken.Owner)
	me, err := c.GetMeWithResponse(t.Context())
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, me.StatusCode())
	assert.Equal(t, openapi.Me{Id: me.JSON200.Id, Name: "web", Kind: openapi.TokenKindOwner}, *me.JSON200)
	assert.NotEqual(t, tok.ID, me.JSON200.Id)
}

func TestRequestsAreValidatedAgainstTheContract(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	c, _ := w.token("web", bearertoken.Owner)

	resp, err := c.CreateRunWithResponse(t.Context(), nil, openapi.NewRun{Objective: ""})
	require.NoError(t, err)
	assert.Equal(t, openapi.ValidationFailed, errorCode(t, resp.Body, resp.StatusCode(), http.StatusBadRequest))

	resp, err = c.CreateRunWithResponse(t.Context(), nil, openapi.NewRun{Objective: "x", Repo: ptr("not a repo")})
	require.NoError(t, err)
	assert.Equal(t, openapi.ValidationFailed, errorCode(t, resp.Body, resp.StatusCode(), http.StatusBadRequest))

	limit := 1000
	runs, err := c.ListRunsWithResponse(t.Context(), &openapi.ListRunsParams{Limit: &limit})
	require.NoError(t, err)
	assert.Equal(t, openapi.ValidationFailed, errorCode(t, runs.Body, runs.StatusCode(), http.StatusBadRequest))

	missing, err := http.Get(w.url + BasePath + "/nothing") //nolint:noctx // A plain request to an unknown path.
	require.NoError(t, err)

	body, err := io.ReadAll(missing.Body)
	require.NoError(t, err)
	require.NoError(t, missing.Body.Close())
	assert.Equal(t, openapi.NotFound, errorCode(t, body, missing.StatusCode, http.StatusNotFound))

	big := strings.NewReader(`{"objective":"` + strings.Repeat("x", maxBodyBytes) + `"}`)
	tooLarge, err := c.CreateRunWithBodyWithResponse(t.Context(), nil, "application/json", big)
	require.NoError(t, err)
	assert.Equal(t, openapi.RequestTooLarge,
		errorCode(t, tooLarge.Body, tooLarge.StatusCode(), http.StatusRequestEntityTooLarge))
}

// Scenario: a coordinator on the laptop creates a Run and its Tasks. It
// coordinates that Run; another coordinator reads it but cannot change it;
// the owner can.
func TestACoordinatorChangesOnlyItsOwnRuns(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	laptop, laptopTok := w.token("laptop", bearertoken.Coordinator)
	other, _ := w.token("other", bearertoken.Coordinator)
	owner, _ := w.token("web", bearertoken.Owner)
	ctx := t.Context()

	run := w.createRun(laptop, "add a health page")
	assert.Equal(t, laptopTok.ID, val(run.CoordinatorTokenId))
	assert.Equal(t, "laptop", run.CreatedBy)

	tasks, err := laptop.CreateTasksWithResponse(ctx, run.Id, nil, openapi.NewTasks{Tasks: []openapi.NewTask{
		{Ref: "a", Spec: "write the handler"},
		{Ref: "b", Spec: "write its test", DependsOn: &[]string{"a"}},
	}})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, tasks.StatusCode(), string(tasks.Body))
	require.Len(t, tasks.JSON201.Data, 2)
	assert.Equal(t, openapi.TaskStatusReady, tasks.JSON201.Data[0].Status)
	assert.Equal(t, openapi.TaskStatusPending, tasks.JSON201.Data[1].Status)

	got, err := other.GetRunWithResponse(ctx, run.Id)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, got.StatusCode())
	assert.Len(t, got.JSON200.Tasks, 2, "every token reads every Run")

	cancel, err := other.CancelTaskWithResponse(ctx, tasks.JSON201.Data[1].Id)
	require.NoError(t, err)
	assert.Equal(t, openapi.Forbidden, errorCode(t, cancel.Body, cancel.StatusCode(), http.StatusForbidden))

	more, err := other.CreateTasksWithResponse(ctx, run.Id, nil, openapi.NewTasks{Tasks: []openapi.NewTask{
		{Ref: "c", Spec: "x"},
	}})
	require.NoError(t, err)
	assert.Equal(t, openapi.Forbidden, errorCode(t, more.Body, more.StatusCode(), http.StatusForbidden))

	cancel, err = laptop.CancelTaskWithResponse(ctx, tasks.JSON201.Data[1].Id)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, cancel.StatusCode(), string(cancel.Body))
	assert.Equal(t, openapi.TaskStatusCanceled, cancel.JSON200.Status)

	canceled, err := owner.CancelRunWithResponse(ctx, run.Id)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, canceled.StatusCode(), string(canceled.Body))
	assert.Equal(t, openapi.RunStatusCanceled, canceled.JSON200.Status)

	// A Run the owner created has no coordinator: no coordinator token changes it.
	ownerRun := w.createRun(owner, "owner's run")
	assert.Nil(t, ownerRun.CoordinatorTokenId)

	finish, err := laptop.FinishRunWithResponse(ctx, ownerRun.Id)
	require.NoError(t, err)
	assert.Equal(t, openapi.Forbidden, errorCode(t, finish.Body, finish.StatusCode(), http.StatusForbidden))

	gone, err := laptop.FinishRunWithResponse(ctx, "run_missing")
	require.NoError(t, err)
	assert.Equal(t, openapi.NotFound, errorCode(t, gone.Body, gone.StatusCode(), http.StatusNotFound))
}

func TestCreatesWithAnIdempotencyKeyHappenOnce(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	c, _ := w.token("laptop", bearertoken.Coordinator)
	ctx := t.Context()
	key := "run-1"

	first, err := c.CreateRunWithResponse(ctx, &openapi.CreateRunParams{IdempotencyKey: &key},
		openapi.NewRun{Objective: "one"})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, first.StatusCode())

	again, err := c.CreateRunWithResponse(ctx, &openapi.CreateRunParams{IdempotencyKey: &key},
		openapi.NewRun{Objective: "one"})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, again.StatusCode())
	assert.Equal(t, first.JSON201.Id, again.JSON201.Id)

	other, err := c.CreateRunWithResponse(ctx, &openapi.CreateRunParams{IdempotencyKey: &key},
		openapi.NewRun{Objective: "two"})
	require.NoError(t, err)
	assert.Equal(t, openapi.IdempotencyKeyConflict, errorCode(t, other.Body, other.StatusCode(), http.StatusConflict))

	runs, err := c.ListRunsWithResponse(ctx, nil)
	require.NoError(t, err)
	assert.Len(t, runs.JSON200.Data, 1)
}

func TestListRunsPages(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	c, _ := w.token("web", bearertoken.Owner)
	ctx := t.Context()

	for _, o := range []string{"one", "two", "three"} {
		w.createRun(c, o)
		time.Sleep(2 * time.Millisecond) // Runs are ordered by their creation time in milliseconds.
	}

	limit := 2
	first, err := c.ListRunsWithResponse(ctx, &openapi.ListRunsParams{Limit: &limit})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, first.StatusCode(), string(first.Body))
	assert.Equal(t, []string{"three", "two"}, objectives(first.JSON200.Data))
	require.NotNil(t, first.JSON200.NextCursor)

	second, err := c.ListRunsWithResponse(ctx, &openapi.ListRunsParams{Limit: &limit, Cursor: first.JSON200.NextCursor})
	require.NoError(t, err)
	assert.Equal(t, []string{"one"}, objectives(second.JSON200.Data))
	assert.Nil(t, second.JSON200.NextCursor)

	bad := "nonsense"
	resp, err := c.ListRunsWithResponse(ctx, &openapi.ListRunsParams{Cursor: &bad})
	require.NoError(t, err)
	assert.Equal(t, openapi.InvalidRequest, errorCode(t, resp.Body, resp.StatusCode(), http.StatusBadRequest))
}

// Scenario: a board asks for the work in flight. It gets every Task of an
// open Run, and a canceled Run's Tasks only while they are recent, each Run
// once.
func TestListTasksFillsABoard(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	c, _ := w.token("web", bearertoken.Owner)
	ctx := t.Context()

	open := w.createRun(c, "open")
	closed := w.createRun(c, "closed")

	for _, r := range []openapi.Run{open, closed} {
		resp, err := c.CreateTasksWithResponse(ctx, r.Id, &openapi.CreateTasksParams{}, openapi.NewTasks{
			Tasks: []openapi.NewTask{{Ref: "a", Spec: r.Objective + " a"}, {Ref: "b", Spec: r.Objective + " b"}},
		})
		require.NoError(t, err)
		require.Equal(t, http.StatusCreated, resp.StatusCode(), string(resp.Body))
	}

	cancel, err := c.CancelRunWithResponse(ctx, closed.Id)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, cancel.StatusCode(), string(cancel.Body))

	board, err := c.ListTasksWithResponse(ctx, &openapi.ListTasksParams{})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, board.StatusCode(), string(board.Body))
	assert.Len(t, board.JSON200.Data, 4, "a canceled Run's Tasks show while they are recent")
	assert.ElementsMatch(t, []string{"open", "closed"}, objectives(board.JSON200.Runs), "each Run once")

	events, err := c.ListEventsWithResponse(ctx, &openapi.ListEventsParams{After: &board.JSON200.After})
	require.NoError(t, err)
	assert.Empty(t, events.JSON200.Data, "after is the newest event when the board was read")
	assert.Positive(t, board.JSON200.After)

	later := time.Now().Add(time.Hour)
	board, err = c.ListTasksWithResponse(ctx, &openapi.ListTasksParams{Since: &later})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, board.StatusCode(), string(board.Body))

	specs := []string{}
	for _, task := range board.JSON200.Data {
		specs = append(specs, task.Spec)
	}

	assert.ElementsMatch(t, []string{"open a", "open b"}, specs, "an open Run's Tasks always show")
	assert.Equal(t, []string{"open"}, objectives(board.JSON200.Runs))
}

func objectives(runs []openapi.Run) []string {
	out := make([]string, 0, len(runs))
	for _, r := range runs {
		out = append(out, r.Objective)
	}

	return out
}

// Scenario: a worker asks its coordinator. The coordinator of the Run sees
// it, answers it, and the next one it forwards to the owner; another
// coordinator can do neither.
func TestTheRunsCoordinatorAnswersAndForwards(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	laptop, _ := w.token("laptop", bearertoken.Coordinator)
	other, _ := w.token("other", bearertoken.Coordinator)
	owner, _ := w.token("web", bearertoken.Owner)
	ctx := t.Context()

	run := w.createRun(laptop, "add a health page")
	first := w.askFromAWorker(laptop, run.Id, "which port?", orchestration.AddressCoordinator)

	coord := openapi.AddresseeCoordinator
	open := openapi.QuestionStatusOpen
	list, err := laptop.ListQuestionsWithResponse(ctx, &openapi.ListQuestionsParams{AddressedTo: &coord, Status: &open})
	require.NoError(t, err)
	require.Len(t, list.JSON200.Data, 1)
	assert.Equal(t, "which port?", list.JSON200.Data[0].Text)

	denied, err := other.AnswerQuestionWithResponse(ctx, first, openapi.Answer{Text: ptr("80")})
	require.NoError(t, err)
	assert.Equal(t, openapi.Forbidden, errorCode(t, denied.Body, denied.StatusCode(), http.StatusForbidden))

	answered, err := laptop.AnswerQuestionWithResponse(ctx, first, openapi.Answer{Text: ptr("8080")})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, answered.StatusCode(), string(answered.Body))
	assert.Equal(t, openapi.QuestionStatusAnswered, answered.JSON200.Status)
	assert.Equal(t, openapi.AddresseeCoordinator, val(answered.JSON200.AnsweredBy))

	second := w.askAgain(first, "may I drop the old table?")

	fwd, err := laptop.ForwardQuestionWithResponse(ctx, second)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, fwd.StatusCode(), string(fwd.Body))
	assert.Equal(t, openapi.AddresseeOwner, fwd.JSON200.AddressedTo)

	notTheirs, err := laptop.AnswerQuestionWithResponse(ctx, second, openapi.Answer{Text: ptr("yes")})
	require.NoError(t, err)
	assert.Equal(t, openapi.Conflict, errorCode(t, notTheirs.Body, notTheirs.StatusCode(), http.StatusConflict),
		"a question for the owner is not the coordinator's to answer")

	byOwner, err := owner.AnswerQuestionWithResponse(ctx, second, openapi.Answer{Text: ptr("no")})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, byOwner.StatusCode(), string(byOwner.Body))
	assert.Equal(t, openapi.AddresseeOwner, val(byOwner.JSON200.AnsweredBy))
}

// worker is a Dispatch a test drives through the orchestration core's node calls.
type worker struct {
	call orchestration.NodeCall
	n    int
}

// askFromAWorker puts one Task of the Run on a node and has its worker ask a question.
func (w *world) askFromAWorker(c *openapi.ClientWithResponses, runID, text string, to orchestration.Addressee) string {
	w.t.Helper()
	ctx := w.t.Context()

	_, err := w.svc.AddAccount(ctx, "claude", "main")
	require.NoError(w.t, err)

	node, _, err := w.svc.AddNode(ctx, "box", 0)
	require.NoError(w.t, err)
	require.NoError(w.t, w.svc.NodeHello(ctx, node.ID, orchestration.Hello{
		WorktreeRoot: "/srv/wt", Runtimes: []orchestration.RuntimeInfo{{Runtime: "claude"}},
	}))

	tasks, err := c.CreateTasksWithResponse(ctx, runID, nil, openapi.NewTasks{Tasks: []openapi.NewTask{
		{Ref: "a", Spec: "work"},
	}})
	require.NoError(w.t, err)
	require.Equal(w.t, http.StatusCreated, tasks.StatusCode(), string(tasks.Body))

	scheduled, err := w.svc.Schedule(ctx)
	require.NoError(w.t, err)
	require.Len(w.t, scheduled.Created, 1)

	wk := &worker{call: orchestration.NodeCall{NodeID: node.ID, DispatchID: scheduled.Created[0]}}
	accepted, err := w.svc.AcceptDispatch(ctx, wk.next())
	require.NoError(w.t, err)

	wk.call.Epoch = accepted.Epoch
	require.NoError(w.t, w.svc.ReportStarted(ctx, wk.next(), "pid:1@1", "sess_1"))

	id, err := w.svc.AskQuestion(ctx, wk.next(), orchestration.Ask{Text: text, To: to})
	require.NoError(w.t, err)

	w.workers[id] = wk

	return id
}

// askAgain has the worker that asked previous ask another question, once the answer reached it.
func (w *world) askAgain(previous, text string) string {
	w.t.Helper()
	ctx := w.t.Context()
	wk := w.workers[previous]

	require.NoError(w.t, w.svc.AckAnswer(ctx, wk.next(), previous))

	id, err := w.svc.AskQuestion(ctx, wk.next(), orchestration.Ask{Text: text, To: orchestration.AddressCoordinator})
	require.NoError(w.t, err)

	w.workers[id] = wk

	return id
}

func (wk *worker) next() orchestration.NodeCall {
	wk.n++
	c := wk.call
	c.RequestID = "req_" + c.DispatchID + "_" + strconv.Itoa(wk.n)

	return c
}

// A waiting read returns as soon as an event follows the cursor, and an
// empty page when none comes in time.
func TestListEventsWaitsForTheNextEvent(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	c, _ := w.token("web", bearertoken.Owner)
	ctx := t.Context()

	page, err := c.ListEventsWithResponse(ctx, nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, page.StatusCode())
	assert.Empty(t, page.JSON200.Data)

	waitOne := 1
	start := time.Now()
	empty, err := c.ListEventsWithResponse(ctx, &openapi.ListEventsParams{Wait: &waitOne})
	require.NoError(t, err)
	assert.Empty(t, empty.JSON200.Data)
	assert.GreaterOrEqual(t, time.Since(start), 900*time.Millisecond)

	waitLong := 30
	done := make(chan *openapi.ListEventsResponse, 1)

	go func() {
		resp, err := c.ListEventsWithResponse(context.WithoutCancel(ctx), &openapi.ListEventsParams{Wait: &waitLong})
		assert.NoError(t, err)
		done <- resp
	}()

	time.Sleep(100 * time.Millisecond)
	run := w.createRun(c, "wake the reader")

	select {
	case resp := <-done:
		require.Len(t, resp.JSON200.Data, 1)
		assert.Equal(t, "run.created", resp.JSON200.Data[0].Kind)
		assert.Equal(t, run.Id, val(resp.JSON200.Data[0].RunId))
	case <-time.After(5 * time.Second):
		t.Fatal("the wait did not end when an event came")
	}
}

// Scenario: the owner opens a running worker's Task on the web. The page
// reads the transcript's newest items, then waits for the next one, which comes
// as soon as the node sends it; the board's Task says what the worker does.
func TestAWorkersTranscriptIsReadAndFollowed(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	c, _ := w.token("web", bearertoken.Owner)
	ctx := t.Context()
	run := w.createRun(c, "add a login")
	wk := w.workers[w.askFromAWorker(c, run.Id, "which port?", orchestration.AddressOwner)]
	node, dispatch := wk.call.NodeID, wk.call.DispatchID

	_, err := w.svc.AddItems(ctx, node, dispatch, []orchestration.Item{
		{Seq: 1, Kind: orchestration.ItemMessage, Text: "Reading the server."},
		{
			Seq: 2, Kind: orchestration.ItemToolCall, CallID: "call_1", Tool: "Read",
			Input: json.RawMessage(`{"file_path":"server.go"}`),
		},
		{Seq: 3, Kind: orchestration.ItemToolResult, CallID: "call_1", Text: "package main", Failed: true},
	})
	require.NoError(t, err)

	two, before := 2, int64(4)
	newest, err := c.ListDispatchItemsWithResponse(ctx, dispatch,
		&openapi.ListDispatchItemsParams{Before: &before, Limit: &two})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, newest.StatusCode(), string(newest.Body))
	require.Len(t, newest.JSON200.Data, 2)
	assert.True(t, newest.JSON200.HasMore)

	call, result := newest.JSON200.Data[0], newest.JSON200.Data[1]
	assert.Equal(t, int64(2), call.Seq)
	assert.Equal(t, openapi.TranscriptItemKind("tool_call"), call.Kind)
	assert.Equal(t, "Read", val(call.Tool))
	assert.Equal(t, map[string]any{"file_path": "server.go"}, val(call.Input))
	assert.Equal(t, "call_1", val(result.CallId))
	assert.True(t, val(result.Failed))
	assert.Equal(t, "package main", val(result.Text))

	after, waitLong := int64(3), 30
	done := make(chan *openapi.ListDispatchItemsResponse, 1)

	go func() {
		resp, err := c.ListDispatchItemsWithResponse(context.WithoutCancel(ctx), dispatch,
			&openapi.ListDispatchItemsParams{After: &after, Wait: &waitLong})
		assert.NoError(t, err)
		done <- resp
	}()

	time.Sleep(100 * time.Millisecond)

	_, err = w.svc.AddItems(ctx, node, dispatch, []orchestration.Item{
		{
			Seq: 4, Kind: orchestration.ItemToolCall, CallID: "call_2", Tool: "Bash",
			Input: json.RawMessage(`{"command":"go test ./..."}`),
		},
	})
	require.NoError(t, err)

	select {
	case resp := <-done:
		require.Len(t, resp.JSON200.Data, 1)
		assert.Equal(t, int64(4), resp.JSON200.Data[0].Seq)
	case <-time.After(5 * time.Second):
		t.Fatal("the wait did not end when an item came")
	}

	board, err := c.ListTasksWithResponse(ctx, nil)
	require.NoError(t, err)
	require.Len(t, board.JSON200.Data, 1)
	assert.Equal(t, "Bash: go test ./...", val(board.JSON200.Data[0].LatestDispatch.Activity))

	missing, err := c.ListDispatchItemsWithResponse(ctx, "dsp_missing", nil)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, missing.StatusCode())

	// The transcript holds what the worker's tools printed: a coordinator reads only its own Runs'.
	stranger, _ := w.token("laptop", bearertoken.Coordinator)
	refused, err := stranger.ListDispatchItemsWithResponse(ctx, dispatch, nil)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, refused.StatusCode(), string(refused.Body))
}

// The stream sends the events after Last-Event-ID, then new ones as they
// are committed, with heartbeats in between; it ends when the server stops.
func TestStreamEventsResumesAndFollows(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	c, _ := w.token("web", bearertoken.Owner)
	ctx := t.Context()

	first := w.createRun(c, "one")
	w.createRun(c, "two")

	page, err := c.ListEventsWithResponse(ctx, nil)
	require.NoError(t, err)
	require.Len(t, page.JSON200.Data, 2)

	secret := bearerOf(t, w, "stream")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.url+BasePath+"/events/stream", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Last-Event-ID", "1")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

	lines := make(chan string, 100)

	go func() {
		defer close(lines)

		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()

	next := func(want string) string {
		t.Helper()

		for {
			select {
			case l, ok := <-lines:
				require.True(t, ok, "the stream ended before %q", want)

				if strings.HasPrefix(l, want) {
					return l
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("no %q in time", want)
			}
		}
	}

	assert.Equal(t, "id: 2", next("id: "), "the stream resumes after Last-Event-ID")
	assert.Equal(t, "event: run.created", next("event: "))
	next(": heartbeat")

	third := w.createRun(c, "three")
	assert.Equal(t, "id: 3", next("id: "))
	assert.Contains(t, next("data: "), third.Id)
	assert.NotEqual(t, first.Id, third.Id)

	close(w.stop)

	for range lines {
		// Drain until the server ends the stream.
	}
}

func bearerOf(t *testing.T, w *world, name string) string {
	t.Helper()

	_, secret, err := w.svc.AddToken(t.Context(), name, bearertoken.Owner)
	require.NoError(t, err)

	return secret
}

// Scenario: a coordinator reads the profiles, providers and models to choose
// from, but only the owner changes them. Applying the same source twice
// makes one version; a name that differs from the path is refused.
func TestOnlyTheOwnerChangesProfiles(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	laptop, _ := w.token("laptop", bearertoken.Coordinator)
	owner, _ := w.token("web", bearertoken.Owner)
	ctx := t.Context()

	list, err := laptop.ListProfilesWithResponse(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, list.StatusCode())
	assert.Len(t, list.JSON200.Data, 4)

	models, err := laptop.ListModelsWithResponse(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, models.StatusCode())
	assert.NotEmpty(t, models.JSON200.Data)

	body := openapi.Source{Source: "name: reviewer\ndescription: d\nruntime: claude\nmodel: {name: opus, effort: max}\n"}

	refused, err := laptop.ApplyProfileWithResponse(ctx, "reviewer", body)
	require.NoError(t, err)
	assert.Equal(t, openapi.Forbidden, errorCode(t, refused.Body, refused.StatusCode(), http.StatusForbidden))

	created, err := owner.ApplyProfileWithResponse(ctx, "reviewer", body)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, created.StatusCode(), string(created.Body))
	assert.Equal(t, 3, created.JSON201.Version)

	same, err := owner.ApplyProfileWithResponse(ctx, "reviewer", body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, same.StatusCode())
	assert.Equal(t, created.JSON201.VersionId, same.JSON200.VersionId)

	wrong, err := owner.ApplyProfileWithResponse(ctx, "explorer", body)
	require.NoError(t, err)
	assert.Equal(t, openapi.InvalidRequest, errorCode(t, wrong.Body, wrong.StatusCode(), http.StatusBadRequest))

	provider, err := laptop.ApplyProviderWithResponse(ctx, "router",
		openapi.Source{Source: "name: router\nkind: openai_compatible\nmodels: [m1]\n"})
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, provider.StatusCode())
}

// fakeCoordinator says pi runs and is never working; a test sends live
// frames on live and reads which chats were stopped.
type fakeCoordinator struct {
	live    chan coordinator.Frame
	aborted atomic.Value
}

func (*fakeCoordinator) Enabled() bool         { return true }
func (*fakeCoordinator) Working(_ string) bool { return false }

func (f *fakeCoordinator) Watch(_ string) (<-chan coordinator.Frame, func()) {
	return f.live, func() {}
}

func (f *fakeCoordinator) Abort(_ context.Context, chatID string) error {
	f.aborted.Store(chatID)

	return nil
}

// Scenario: the owner chats with pi from the web. Only the owner's token
// reads or writes chats; a message read with wait arrives as it is kept; a
// Run created from the chat records it.
func TestOnlyTheOwnerChats(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	owner, _ := w.token("web", bearertoken.Owner)
	pi, _ := w.token("pi", bearertoken.Coordinator)
	ctx := t.Context()

	created, err := owner.CreateChatWithResponse(ctx, openapi.NewChat{})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, created.StatusCode(), string(created.Body))
	chat := created.JSON201
	assert.True(t, chat.Coordinator)
	assert.False(t, chat.Working)

	refused, err := pi.ListChatsWithResponse(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, refused.StatusCode(), "a coordinator token does not read the owner's chats")

	sent, err := owner.SendChatMessageWithResponse(ctx, chat.Id, openapi.NewChatMessage{Text: "add a health page"})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, sent.StatusCode())
	assert.Equal(t, openapi.ChatRoleOwner, sent.JSON201.Role)
	assert.Nil(t, sent.JSON201.DeliveredAt)

	go func() {
		time.Sleep(100 * time.Millisecond)
		_, _ = w.svc.AddChatMessage(context.WithoutCancel(ctx), chat.Id, orchestration.ChatCoordinator, "On it.")
	}()

	wait := 5
	after := sent.JSON201.Seq

	page, err := owner.ListChatMessagesWithResponse(ctx, chat.Id,
		&openapi.ListChatMessagesParams{After: &after, Wait: &wait})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, page.StatusCode())
	require.Len(t, page.JSON200.Data, 1, "the wait returns the reply as it is kept")
	assert.Equal(t, "On it.", page.JSON200.Data[0].Text)
	assert.Equal(t, page.JSON200.Data[0].Seq, page.JSON200.NextAfter)

	run, err := pi.CreateRunWithResponse(ctx, nil, openapi.NewRun{Objective: "health page", ChatId: &chat.Id})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, run.StatusCode(), string(run.Body))
	assert.Equal(t, chat.Id, val(run.JSON201.ChatId))

	unknown := "cht_none"
	missing, err := pi.CreateRunWithResponse(ctx, nil, openapi.NewRun{Objective: "x", ChatId: &unknown})
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, missing.StatusCode())
}

// Scenario: the web opens a chat while pi answers. It gets pi's state, the
// kept messages after its cursor with their seq as the id, pi's text as it is
// written (no id: not kept), then the kept reply with its parts. Stop goes to
// pi.
func TestStreamChatShowsPiLive(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	owner, _ := w.token("web", bearertoken.Owner)
	ctx := t.Context()

	chat, err := w.svc.CreateChat(ctx, "")
	require.NoError(t, err)

	old, err := w.svc.AddChatMessage(ctx, chat.ID, orchestration.ChatOwner, "first")
	require.NoError(t, err)
	_, err = w.svc.AddChatMessage(ctx, chat.ID, orchestration.ChatOwner, "add a health page")
	require.NoError(t, err)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.url+BasePath+"/chats/"+chat.ID+"/stream", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+bearerOf(t, w, "stream"))
	req.Header.Set("Last-Event-ID", strconv.FormatInt(old.Seq, 10))

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)

	lines := make(chan string, 100)

	go func() {
		defer close(lines)

		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if l := sc.Text(); l != "" && !strings.HasPrefix(l, ":") {
				lines <- l
			}
		}
	}()

	next := func() string {
		t.Helper()

		select {
		case l, ok := <-lines:
			require.True(t, ok, "the stream ended")

			return l
		case <-time.After(5 * time.Second):
			t.Fatal("no frame in time")

			return ""
		}
	}

	assert.Equal(t, "event: state", next())
	assert.JSONEq(t, `{"working":false}`, strings.TrimPrefix(next(), "data: "))
	assert.Equal(t, "id: "+strconv.FormatInt(old.Seq+1, 10), next(), "resumes after Last-Event-ID")
	assert.Equal(t, "event: message", next())
	assert.Contains(t, next(), "add a health page")

	w.pi.live <- coordinator.Frame{Event: coordinator.FrameDelta, Data: map[string]any{
		"kind": "text", "index": 0, "delta": "On ",
	}}
	assert.Equal(t, "event: delta", next(), "a live frame has no id")
	assert.JSONEq(t, `{"kind":"text","index":0,"delta":"On "}`, strings.TrimPrefix(next(), "data: "))

	_, err = w.svc.AddChatMessageParts(ctx, chat.ID, orchestration.ChatCoordinator, "On it.",
		json.RawMessage(`[{"type":"thinking","text":"plan"},{"type":"text","text":"On it."}]`))
	require.NoError(t, err)

	assert.True(t, strings.HasPrefix(next(), "id: "))
	assert.Equal(t, "event: message", next())

	var kept openapi.ChatMessage
	require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(next(), "data: ")), &kept))
	assert.Equal(t, openapi.ChatRoleCoordinator, kept.Role)
	require.NotNil(t, kept.Parts)
	assert.Equal(t, "plan", val((*kept.Parts)[0].Text))

	stopped, err := owner.AbortChatWithResponse(ctx, chat.ID)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNoContent, stopped.StatusCode())
	assert.Equal(t, chat.ID, w.pi.aborted.Load())

	close(w.stop)

	for range lines {
		// Drain until the server ends the stream.
	}
}
