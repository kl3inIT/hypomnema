// Package e2e runs both trees together: the control plane's node API over a
// real WebSocket and the node daemon with a fake Runner. It holds tests only,
// so neither binary links it; it is the one place allowed to import both
// trees (docs/decisions/2026-11-07-two-deployables.md).
package e2e

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/nodeapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/node/daemon"
	"github.com/kl3inIT/hypomnema/internal/node/statedb"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

const wait = 5 * time.Second

// fakeRunner records what the daemon asks of it; the test drives each worker.
type fakeRunner struct {
	mu       sync.Mutex
	workers  map[string]daemon.Reporter
	adopted  []string
	stops    []string
	released []string
	answers  map[string][]nodeproto.Question
	asked    map[string]string // report id → question id
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{
		workers: map[string]daemon.Reporter{}, answers: map[string][]nodeproto.Question{}, asked: map[string]string{},
	}
}

func (r *fakeRunner) Start(_ context.Context, d statedb.Dispatch, w daemon.Reporter) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.workers[d.ID] = w

	return nil
}

func (r *fakeRunner) Adopt(_ context.Context, d statedb.Dispatch, w daemon.Reporter) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.workers[d.ID] = w
	r.adopted = append(r.adopted, d.ID)

	return nil
}

func (r *fakeRunner) Stop(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.stops = append(r.stops, id)
}

func (r *fakeRunner) Release(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.released = append(r.released, id)
}

func (r *fakeRunner) Answer(id string, q nodeproto.Question) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.answers[id] = append(r.answers[id], q)
}

func (r *fakeRunner) Asked(_, reportID, questionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.asked[reportID] = questionID
}

func (r *fakeRunner) worker(t *testing.T) daemon.Reporter {
	t.Helper()

	var w daemon.Reporter

	require.Eventually(t, func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()

		for _, x := range r.workers {
			w = x
		}

		return w != nil
	}, wait, 5*time.Millisecond, "a worker starts")

	return w
}

func (r *fakeRunner) snapshot() fakeRunner {
	r.mu.Lock()
	defer r.mu.Unlock()

	answers := map[string][]nodeproto.Question{}
	for k, v := range r.answers {
		answers[k] = append([]nodeproto.Question(nil), v...)
	}

	asked := map[string]string{}
	for k, v := range r.asked {
		asked[k] = v
	}

	return fakeRunner{
		adopted: append([]string(nil), r.adopted...), stops: append([]string(nil), r.stops...),
		released: append([]string(nil), r.released...), answers: answers, asked: asked,
	}
}

// world is a control plane with one node and one account, and the node's state file.
type world struct {
	t     *testing.T
	svc   *orchestration.Service
	url   string
	token string
	state string
}

func newWorld(t *testing.T) *world {
	t.Helper()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: filepath.Join(t.TempDir(), "hypomnema.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, orchestration.Migrate(t.Context(), db))

	svc := orchestration.New(db, platform.SystemClock{}, platform.NewID)

	mux := http.NewServeMux()
	nodeapi.New(svc, slog.New(slog.DiscardHandler)).Register(mux)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	_, token, err := svc.AddNode(t.Context(), "node_a", 2)
	require.NoError(t, err)

	_, err = svc.AddAccount(t.Context(), "claude", "acct_1")
	require.NoError(t, err)

	return &world{t: t, svc: svc, url: srv.URL, token: token, state: filepath.Join(t.TempDir(), "node", "state.db")}
}

// startNode runs a daemon over the node's state file until the returned stop is called.
func (w *world) startNode(r daemon.Runner, token string) (stop func() error) {
	w.t.Helper()

	db, err := statedb.Open(w.t.Context(), w.state, time.Now)
	require.NoError(w.t, err)

	d := daemon.New(daemon.Config{
		URL: w.url, Token: token, WorktreeRoot: "/srv/wt", Version: "test",
		Runtimes: []nodeproto.RuntimeInfo{{Runtime: "claude", Path: "/usr/local/bin/claude", Version: "2.1.319"}},
	}, db, r, slog.New(slog.DiscardHandler))

	ctx, cancel := context.WithCancel(context.WithoutCancel(w.t.Context()))
	done := make(chan error, 1)

	go func() { done <- d.Run(ctx) }()

	stopped := false
	stop = func() error {
		if stopped {
			return nil
		}

		stopped = true

		cancel()
		err := <-done

		return errorsJoin(err, db.Close())
	}

	w.t.Cleanup(func() { _ = stop() })

	return stop
}

func errorsJoin(a, b error) error {
	if a != nil {
		return a
	}

	return b
}

// task creates a Run with one Task and lets the scheduler place it.
func (w *world) task() orchestration.Task {
	w.t.Helper()

	run, err := w.svc.CreateRun(w.t.Context(), orchestration.NewRun{
		Objective: "add Google login", CreatedBy: "owner", Repo: "kl3inIT/second-bite",
	})
	require.NoError(w.t, err)

	tasks, err := w.svc.CreateTasks(w.t.Context(), run.ID, []orchestration.NewTask{
		{Ref: "a", Profile: "implementer", Spec: "add the Google login button"},
	})
	require.NoError(w.t, err)

	require.Eventually(w.t, func() bool {
		s, err := w.svc.Schedule(w.t.Context())
		require.NoError(w.t, err)

		return len(s.Created) == 1
	}, wait, 10*time.Millisecond, "the node connects and the Task is placed")

	return tasks[0]
}

func (w *world) status(id string) orchestration.TaskStatus {
	w.t.Helper()

	task, err := w.svc.GetTask(w.t.Context(), id)
	require.NoError(w.t, err)

	return task.Status
}

func (w *world) eventually(id string, want orchestration.TaskStatus) {
	w.t.Helper()

	require.Eventually(w.t, func() bool { return w.status(id) == want }, wait, 5*time.Millisecond,
		"task %s becomes %s", id, want)
}

func success() nodeproto.Finished {
	code := 0

	return nodeproto.Finished{
		Summary:  "added the button; PR #7 is green",
		Evidence: []nodeproto.Evidence{{Kind: "pull_request", Ref: "kl3inIT/second-bite#7"}},
		Exit:     nodeproto.Exit{Kind: "exited", Code: &code},
	}
}

// Scenario: the whole path. The node is offered the Dispatch, accepts it,
// the worker starts and succeeds, and the node forgets it.
func TestNodeRunsATaskToSuccess(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	r := newFakeRunner()
	stop := w.startNode(r, w.token)
	task := w.task()

	worker := r.worker(t)
	require.NoError(t, worker.Started(t.Context(), "pid:42@1", "sess_1"))
	w.eventually(task.ID, orchestration.TaskRunning)

	require.NoError(t, worker.Finished(t.Context(), success()))
	w.eventually(task.ID, orchestration.TaskSucceeded)

	// A second handle on the node's state, read while the daemon runs.
	db, err := statedb.Open(t.Context(), w.state, time.Now)
	require.NoError(t, err)

	defer func() { _ = db.Close() }()

	require.Eventually(t, func() bool {
		held, err := db.List(t.Context())
		require.NoError(t, err)

		pending, err := db.Pending(t.Context())
		require.NoError(t, err)

		return len(held) == 0 && len(pending) == 0
	}, wait, 5*time.Millisecond, "the node forgets the Dispatch once its final report is acknowledged")

	require.NoError(t, stop())
}

// Scenario: the daemon restarts while its worker runs, and the worker
// finishes while no daemon is connected. The new daemon claims the
// Dispatch, is told to retain it, adopts the worker, and the queued final
// report is delivered from the outbox.
func TestRestartAdoptsTheWorkerAndReplaysTheOutbox(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	first := newFakeRunner()
	stop := w.startNode(first, w.token)
	task := w.task()

	worker := first.worker(t)
	require.NoError(t, worker.Started(t.Context(), "pid:42@1", "sess_1"))
	w.eventually(task.ID, orchestration.TaskRunning)

	require.NoError(t, stop())

	second := newFakeRunner()
	w.startNode(second, w.token)
	adopted := second.worker(t)
	assert.Len(t, second.snapshot().adopted, 1)

	require.NoError(t, adopted.Finished(t.Context(), success()))
	w.eventually(task.ID, orchestration.TaskSucceeded)
}

// Scenario: the worker asks the owner. It learns the question's id, the
// answer reaches it, and once it acknowledges the answer it finishes.
func TestWorkerAsksAndGetsTheAnswer(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	r := newFakeRunner()
	w.startNode(r, w.token)
	task := w.task()

	worker := r.worker(t)
	require.NoError(t, worker.Started(t.Context(), "pid:42@1", "sess_1"))

	reportID, err := worker.Ask(t.Context(), nodeproto.Ask{
		Text: "goth or hand-written OAuth?", Options: []nodeproto.Option{{Label: "goth"}, {Label: "hand-written"}},
		To: "owner",
	})
	require.NoError(t, err)

	var questionID string

	require.Eventually(t, func() bool {
		questionID = r.snapshot().asked[reportID]

		return questionID != ""
	}, wait, 5*time.Millisecond, "the worker learns its question's id")

	first := 0
	_, err = w.svc.AnswerQuestion(t.Context(), questionID, orchestration.AddressOwner,
		orchestration.Answer{Option: &first})
	require.NoError(t, err)

	var got nodeproto.Question

	require.Eventually(t, func() bool {
		answers := r.snapshot().answers[worker.ID()]
		if len(answers) == 0 {
			return false
		}

		got = answers[0]

		return true
	}, wait, 5*time.Millisecond, "the answer reaches the worker")

	assert.Equal(t, questionID, got.ID)
	require.NotNil(t, got.Answer)
	assert.Equal(t, &first, got.Answer.Option)

	require.NoError(t, worker.AnswerReceived(t.Context(), questionID))
	require.NoError(t, worker.Finished(t.Context(), success()))
	w.eventually(task.ID, orchestration.TaskSucceeded)
}

// Scenario: the owner cancels the Task. The stop reaches the worker, the
// worker confirms it, and the Task is canceled.
func TestCancelStopsTheWorker(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	r := newFakeRunner()
	w.startNode(r, w.token)
	task := w.task()

	worker := r.worker(t)
	require.NoError(t, worker.Started(t.Context(), "pid:42@1", "sess_1"))
	w.eventually(task.ID, orchestration.TaskRunning)

	require.NoError(t, w.svc.CancelTask(t.Context(), task.ID))
	require.Eventually(t, func() bool { return len(r.snapshot().stops) > 0 }, wait, 5*time.Millisecond)

	require.NoError(t, worker.Stopped(t.Context()))
	w.eventually(task.ID, orchestration.TaskCanceled)
}

// Scenario: a token the control plane does not know. The daemon stops.
func TestDaemonStopsOnARefusedToken(t *testing.T) {
	t.Parallel()

	w := newWorld(t)

	db, err := statedb.Open(t.Context(), w.state, time.Now)
	require.NoError(t, err)

	defer func() { _ = db.Close() }()

	d := daemon.New(daemon.Config{URL: w.url, Token: "hyn_wrong"}, db, newFakeRunner(), slog.New(slog.DiscardHandler))

	ctx, cancel := context.WithTimeout(t.Context(), wait)
	defer cancel()

	require.ErrorIs(t, d.Run(ctx), daemon.ErrUnauthorized)
}
