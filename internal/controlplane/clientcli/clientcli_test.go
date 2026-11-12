package clientcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/coordinator"
	"github.com/kl3inIT/hypomnema/internal/controlplane/integration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

// world is a control plane serving the client API, and a home for the CLI.
type world struct {
	svc  *orchestration.Service
	url  string
	home string
}

func newWorld(t *testing.T) *world {
	t.Helper()

	return newWorldWith(t, nil)
}

// newWorldWith serves the API with a coordinator, or none when nil.
func newWorldWith(t *testing.T, coord clientapi.Coordinator, with ...func(*clientapi.Server)) *world {
	t.Helper()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	svc := orchestration.New(db, platform.SystemClock{}, platform.NewID)
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })

	mux := http.NewServeMux()
	srv := clientapi.New(svc, slog.New(slog.DiscardHandler), stop).WithHostTokens(&keptTokens{svc: svc})
	if coord != nil {
		srv.WithCoordinator(coord)
	}

	for _, option := range with {
		option(srv)
	}

	require.NoError(t, srv.Register(mux))

	hs := httptest.NewServer(mux)
	t.Cleanup(hs.Close)

	return &world{svc: svc, url: hs.URL, home: filepath.Join(t.TempDir(), "home")}
}

// login makes a token of kind and logs the CLI in with it.
func (w *world) login(t *testing.T, kind bearertoken.Kind) string {
	t.Helper()

	_, secret, err := w.svc.AddToken(t.Context(), "cli-"+string(kind), kind)
	require.NoError(t, err)

	return w.loginWith(t, secret)
}

// loginWith logs the CLI in with a token's secret.
func (w *world) loginWith(t *testing.T, secret string) string {
	t.Helper()

	file := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(file, []byte(secret+"\n"), 0o600))

	out, err := w.cmd(t, "login", "--url", w.url+"/", "--token-file", file)
	require.NoError(t, err)

	return out
}

func (w *world) cmd(t *testing.T, args ...string) (string, error) {
	t.Helper()

	return w.cmdIn(t, "", args...)
}

func (w *world) cmdIn(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()

	var out bytes.Buffer
	err := Command(t.Context(), w.home, args, strings.NewReader(stdin), &out)

	return out.String(), err
}

// jsonCmd runs a command with --json and decodes its output into v.
func (w *world) jsonCmd(t *testing.T, v any, stdin string, args ...string) {
	t.Helper()

	out, err := w.cmdIn(t, stdin, append(args, "--json")...)
	require.NoError(t, err, out)
	require.NoError(t, json.Unmarshal([]byte(out), v), out)
}

// Scenario: the owner logs in from the laptop; the token is checked and kept
// in a private file. A token the control plane does not know is refused and
// nothing is saved.
func TestLoginChecksAndKeepsTheToken(t *testing.T) {
	t.Parallel()

	w := newWorld(t)

	unknown, err := bearertoken.Generate(bearertoken.Owner)
	require.NoError(t, err)

	file := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(file, []byte(unknown), 0o600))

	_, err = w.cmd(t, "login", "--url", w.url, "--token-file", file)
	require.ErrorContains(t, err, "unauthorized")

	_, err = LoadConfig(w.home)
	require.Error(t, err, "a refused login saves nothing")

	out := w.login(t, bearertoken.Owner)
	assert.Equal(t, "logged in to "+w.url+" as cli-owner (owner)\n", out)

	cfg, err := LoadConfig(w.home)
	require.NoError(t, err)
	assert.Equal(t, w.url, cfg.URL, "the trailing slash is dropped")

	var me openapi.Me
	w.jsonCmd(t, &me, "", "whoami")
	assert.Equal(t, openapi.TokenKindOwner, me.Kind)

	_, err = w.cmd(t, "login", "--url", "ftp://x", "--token-file", file)
	require.Error(t, err)

	err = Command(t.Context(), filepath.Join(t.TempDir(), "nobody"), []string{"questions"}, nil, io.Discard)
	require.ErrorContains(t, err, "hypomnema login")
}

func TestUnknownCommandsAndHelp(t *testing.T) {
	t.Parallel()

	w := newWorld(t)

	_, err := w.cmd(t, "runs", "explode")
	require.ErrorIs(t, err, ErrUsage)
	assert.Contains(t, err.Error(), "hypomnema runs wait <run_id>", "the usage lists the runs commands")

	out, err := w.cmd(t, "runs", "wait", "--help")
	require.NoError(t, err, "help needs no login")
	assert.Contains(t, out, "-after")

	_, err = w.cmd(t, "runs", "show")
	require.ErrorIs(t, err, ErrUsage, "a missing run id")
}

// Scenario: the owner starts a Run from the laptop and follows it. Events
// are printed as they come; when the Task ends the Run is finished.
func TestRunFollowsUntilTheTaskEnds(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.login(t, bearertoken.Owner)

	out, err := w.cmd(t, "run", "--repo", "kl3inIT/second-bite", "--detach", "add", "Google", "login")
	require.NoError(t, err)
	assert.Regexp(t, `^run run_\w+, task tsk_\w+\n$`, out)

	_, err = w.cmd(t, "run", "--detach", "no repository")
	require.ErrorIs(t, err, ErrUsage)

	_, err = w.cmd(t, "run", "--repo", "not a repo", "--detach", "x")
	require.ErrorContains(t, err, "is not owner/name or the URL of a repository")

	var buf syncBuffer

	done := make(chan error, 1)

	go func() {
		done <- Command(context.WithoutCancel(t.Context()), w.home,
			[]string{"run", "--repo", "kl3inIT/demo", "follow me"}, nil, &buf)
	}()

	require.Eventually(t, func() bool { return strings.Contains(buf.String(), "task.created") }, 5*time.Second,
		10*time.Millisecond)

	taskID := regexp.MustCompile(`task (tsk_\w+)`).FindStringSubmatch(buf.String())[1]
	require.NoError(t, w.svc.CancelTask(t.Context(), taskID))

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("run did not stop following when its Task ended")
	}

	assert.Contains(t, buf.String(), "task canceled")

	task, err := w.svc.GetTask(t.Context(), taskID)
	require.NoError(t, err)

	run, err := w.svc.GetRun(t.Context(), task.RunID)
	require.NoError(t, err)
	assert.Equal(t, orchestration.RunFinished, run.Status)
	assert.Equal(t, "cli-owner", run.CreatedBy)
}

// Scenario: a coordinator on the laptop follows the skill's recipe. It
// creates a Run and two Tasks from files, waits, answers a worker's question,
// forwards the next to the owner, sees the Task fail, and is told to finish.
func TestACoordinatorFollowsTheRecipe(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.login(t, bearertoken.Coordinator)

	objective := filepath.Join(t.TempDir(), "objective.md")
	require.NoError(t, os.WriteFile(objective, []byte("Add a health page.\nIt answers 200 with \"ok\"."), 0o600))

	var run openapi.Run
	w.jsonCmd(t, &run, "", "runs", "create", "--repo", "kl3inIT/demo", "--objective-file", objective, "--key", "k1")
	assert.Equal(t, "Add a health page.\nIt answers 200 with \"ok\".", run.Objective, "text from a file is kept as is")

	var again openapi.Run
	w.jsonCmd(t, &again, "", "runs", "create", "--repo", "kl3inIT/demo", "--objective-file", objective, "--key", "k1")
	assert.Equal(t, run.Id, again.Id, "the same --key returns the first Run")

	var tasks openapi.TaskList
	w.jsonCmd(t, &tasks, `{"tasks": [
		{"ref": "api", "profile": "implementer", "spec": "add GET /health"},
		{"ref": "docs", "profile": "implementer", "spec": "document it", "depends_on": ["api"]}
	]}`, "tasks", "create", run.Id, "--file", "-")
	require.Len(t, tasks.Data, 2)
	assert.Equal(t, openapi.TaskStatusPending, tasks.Data[1].Status)

	wk := startWorker(t, w)
	first := wk.ask(t, w, "Which port?")

	var waited Waited
	w.jsonCmd(t, &waited, "", "runs", "wait", run.Id, "--after", "0")
	require.Len(t, waited.Wake, 1)
	assert.Equal(t, "question.opened", waited.Wake[0].Kind)
	assert.Contains(t, waited.Next, "hypomnema questions show "+first+" --json")
	assert.Contains(t, waited.Next,
		"hypomnema runs wait "+run.Id+" --after "+strconv.FormatInt(waited.After, 10)+" --json")

	answer := filepath.Join(t.TempDir(), "answer.md")
	require.NoError(t, os.WriteFile(answer, []byte("8080"), 0o600))

	var answered openapi.Question
	w.jsonCmd(t, &answered, "", "answer", first, "--text-file", answer)
	assert.Equal(t, openapi.AddresseeCoordinator, val(answered.AnsweredBy))

	second := wk.askAgain(t, w, first, "May I drop the old table?")

	var open openapi.QuestionList
	w.jsonCmd(t, &open, "", "questions", "--to", "coordinator", "--run", run.Id)
	require.Len(t, open.Data, 1)
	assert.Equal(t, second, open.Data[0].Id)

	var forwarded openapi.Question
	w.jsonCmd(t, &forwarded, "", "questions", "forward", second)
	assert.Equal(t, openapi.AddresseeOwner, forwarded.AddressedTo)

	// The worker finishes; its Task succeeds and the coordinator reads the evidence.
	wk.succeed(t, w)

	w.jsonCmd(t, &waited, "", "runs", "wait", run.Id, "--after", strconv.FormatInt(waited.After, 10))
	require.NotEmpty(t, waited.Wake)
	assert.Contains(t, waited.Next, "hypomnema tasks show "+tasks.Data[0].Id+" --json")

	var shown openapi.TaskDetail
	w.jsonCmd(t, &shown, "", "tasks", "show", tasks.Data[0].Id)
	require.NotNil(t, shown.LatestDispatch)
	require.NotNil(t, shown.LatestDispatch.Result)
	assert.Equal(t, "pull_request", shown.LatestDispatch.Result.Evidence[0].Kind)

	var cancel openapi.Task
	w.jsonCmd(t, &cancel, "", "tasks", "cancel", tasks.Data[1].Id)

	for {
		w.jsonCmd(t, &waited, "", "runs", "wait", run.Id, "--after", strconv.FormatInt(waited.After, 10),
			"--timeout", "2s")

		if has(waited.Next, "hypomnema runs finish "+run.Id+" --json") || waited.TimedOut {
			break
		}
	}

	assert.Contains(t, waited.Next, "hypomnema runs finish "+run.Id+" --json")

	var finished openapi.Run
	w.jsonCmd(t, &finished, "", "runs", "finish", run.Id)
	assert.Equal(t, openapi.RunStatusFinished, finished.Status)
}

func has(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}

	return false
}

func TestRunsWaitTimesOut(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.login(t, bearertoken.Owner)

	var run openapi.Run
	w.jsonCmd(t, &run, "", "runs", "create", "--repo", "kl3inIT/demo", "quiet")

	var waited Waited
	w.jsonCmd(t, &waited, "", "runs", "wait", run.Id, "--timeout", "1s")
	assert.True(t, waited.TimedOut)
	assert.Empty(t, waited.Wake)
}

// worker drives one Dispatch through the orchestration core's node calls.
type worker struct {
	call orchestration.NodeCall
	n    int
}

// startWorker places the first ready Task on a node and starts its worker.
func startWorker(t *testing.T, w *world) *worker {
	t.Helper()
	ctx := t.Context()

	_, err := w.svc.AddAccount(ctx, "claude", "main")
	require.NoError(t, err)

	node, _, err := w.svc.AddNode(ctx, "box", 0)
	require.NoError(t, err)
	require.NoError(t, w.svc.NodeHello(ctx, node.ID, orchestration.Hello{
		WorktreeRoot: "/srv/wt", Runtimes: []orchestration.RuntimeInfo{{Runtime: "claude"}},
	}))

	scheduled, err := w.svc.Schedule(ctx)
	require.NoError(t, err)
	require.Len(t, scheduled.Created, 1)

	wk := &worker{call: orchestration.NodeCall{NodeID: node.ID}}
	wk.begin(t, w, scheduled.Created[0])

	return wk
}

func (wk *worker) begin(t *testing.T, w *world, dispatchID string) {
	t.Helper()

	wk.call.DispatchID, wk.call.Epoch = dispatchID, 0

	accepted, err := w.svc.AcceptDispatch(t.Context(), wk.next())
	require.NoError(t, err)

	wk.call.Epoch = accepted.Epoch
	require.NoError(t, w.svc.ReportStarted(t.Context(), wk.next(), "pid:1@1", "sess_1"))
}

func (wk *worker) next() orchestration.NodeCall {
	wk.n++
	c := wk.call
	c.RequestID = "req_" + strconv.Itoa(wk.n)

	return c
}

func (wk *worker) ask(t *testing.T, w *world, text string) string {
	t.Helper()

	id, err := w.svc.AskQuestion(t.Context(), wk.next(),
		orchestration.Ask{Text: text, To: orchestration.AddressCoordinator})
	require.NoError(t, err)

	return id
}

func (wk *worker) askAgain(t *testing.T, w *world, previous, text string) string {
	t.Helper()
	require.NoError(t, w.svc.AckAnswer(t.Context(), wk.next(), previous))

	return wk.ask(t, w, text)
}

// succeed reports the work done, with a pull request as evidence.
func (wk *worker) succeed(t *testing.T, w *world) {
	t.Helper()

	require.NoError(t, w.svc.ReportFinished(t.Context(), wk.next(), orchestration.Outcome{
		Result: orchestration.Result{
			Summary:  "added GET /health",
			Evidence: []orchestration.Evidence{{Kind: "pull_request", Ref: "https://github.com/kl3inIT/demo/pull/7"}},
		},
	}))
}

// keptTokens stands in for the vault: it remembers what the API handed it and
// tells orchestration which hosts have a token.
type keptTokens struct {
	svc *orchestration.Service
	mu  sync.Mutex
	got map[string]string
}

func (k *keptTokens) Connect(ctx context.Context, host, label, token string) error {
	k.mu.Lock()
	defer k.mu.Unlock()

	if k.got == nil {
		k.got = map[string]string{}
	}

	k.got[host+":"+label] = token

	_, err := k.svc.SetConnection(ctx, host, label, "", "kept")

	return err
}

func (k *keptTokens) Disconnect(ctx context.Context, host, label string) error {
	k.mu.Lock()
	defer k.mu.Unlock()

	if _, err := k.svc.RemoveConnection(ctx, host, label); err != nil && !errors.Is(err, orchestration.ErrNotFound) {
		return err
	}

	delete(k.got, host+":"+label)

	return nil
}

func (k *keptTokens) RemoveHost(ctx context.Context, host string) error {
	h, err := k.svc.GitHostOf(ctx, host)
	if err != nil {
		return err
	}

	for _, label := range h.Connections {
		if err := k.Disconnect(ctx, host, label); err != nil {
			return err
		}
	}

	return k.svc.RemoveGitHost(ctx, host)
}

// Scenario: the owner connects two accounts of github.com, registers a
// repository with one of them, and sees both; the account a repository uses
// cannot be disconnected until the repository names another or is forgotten.
// A token is never an argument and never printed.
func TestSeveralAccountsOfAHostFromTheCLI(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.login(t, bearertoken.Owner)

	out, err := w.cmd(t, "integrations", "list")
	require.NoError(t, err)
	assert.Equal(t, "github.com\tgithub\tnot connected\ngitlab.com\tgitlab\tnot connected\n", out)

	_, err = w.cmd(t, "integrations", "connect", "github.com", "--label", "work")
	require.ErrorContains(t, err, "--token-file")

	out, err = w.cmdIn(t, "ghp_personal\n", "integrations", "connect", "https://github.com/", "--label", "personal",
		"--token-file", "-")
	require.NoError(t, err)
	assert.Equal(t, "github.com:personal is connected\n", out)

	_, err = w.cmdIn(t, "ghp_work\n", "integrations", "connect", "github.com", "--label", "work", "--token-file", "-")
	require.NoError(t, err)

	_, err = w.cmdIn(t, "ghp_x\n", "integrations", "connect", "github.com", "--label", "My Work", "--token-file", "-")
	require.Error(t, err, "a label is lower-case letters, digits and dashes")

	out, err = w.cmd(t, "integrations", "list")
	require.NoError(t, err)
	assert.Equal(t, "github.com\tgithub\tpersonal,work\ngitlab.com\tgitlab\tnot connected\n", out)

	out, err = w.cmd(t, "integrations", "list", "--json")
	require.NoError(t, err)
	assert.NotContains(t, out, "ghp_", "the API never returns a token")

	_, err = w.cmd(t, "repos", "add", "acme/shop", "--connection", "nobody")
	require.Error(t, err, "a repository names a connection its host has")

	_, err = w.cmd(t, "repos", "add", "acme/shop", "--connection", "work")
	require.NoError(t, err)

	out, err = w.cmd(t, "repos", "list")
	require.NoError(t, err)
	assert.Equal(t, "shop\tacme/shop\t\twork\n", out)

	_, err = w.cmd(t, "integrations", "disconnect", "github.com")
	require.ErrorContains(t, err, "<integration>:<label>")

	_, err = w.cmd(t, "integrations", "disconnect", "github.com:work")
	require.Error(t, err, "a repository still uses it")

	out, err = w.cmd(t, "integrations", "disconnect", "github.com:personal")
	require.NoError(t, err)
	assert.Equal(t, "github.com:personal is forgotten\n", out)

	_, err = w.cmd(t, "repos", "rm", "shop")
	require.NoError(t, err)

	_, err = w.cmd(t, "integrations", "disconnect", "github.com:work")
	require.NoError(t, err)

	// The older commands act on the connection "default".
	_, err = w.cmdIn(t, "ghp_default\n", "hosts", "token", "set", "github.com", "--token-file", "-")
	require.NoError(t, err)

	out, err = w.cmd(t, "integrations", "list")
	require.NoError(t, err)
	assert.Contains(t, out, "github.com\tgithub\tdefault\n")

	w.login(t, bearertoken.Coordinator)

	out, err = w.cmd(t, "integrations", "list")
	require.NoError(t, err, "a coordinator sees which accounts exist")
	assert.Contains(t, out, "default")

	_, err = w.cmdIn(t, "ghp_x", "integrations", "connect", "github.com", "--label", "mine", "--token-file", "-")
	require.Error(t, err, "only the owner connects an account")
}

// Scenario: the owner gives a git host its token from a file, sees which hosts
// have one, and takes it away. The token is never an argument and never
// printed; a coordinator cannot set one.
func TestAHostsTokenFromTheCLI(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.login(t, bearertoken.Owner)

	_, err := w.cmd(t, "hosts", "add", "gitlab.company.example")
	require.NoError(t, err)

	file := filepath.Join(t.TempDir(), "gitlab.token")
	require.NoError(t, os.WriteFile(file, []byte("glpat-never-printed\n"), 0o600))

	_, err = w.cmd(t, "hosts", "token", "set", "gitlab.company.example")
	require.ErrorContains(t, err, "--token-file")

	out, err := w.cmd(t, "hosts", "token", "set", "gitlab.company.example", "--token-file", file)
	require.NoError(t, err)
	assert.Equal(t, "host gitlab.company.example has a token\n", out)

	out, err = w.cmdIn(t, "ghp_from_stdin\n", "hosts", "token", "set", "github.com", "--token-file", "-")
	require.NoError(t, err)
	assert.NotContains(t, out, "ghp_from_stdin")

	out, err = w.cmd(t, "hosts", "list")
	require.NoError(t, err)
	assert.Equal(t, "github.com\tgithub\tknown\ttoken\ngitlab.com\tgitlab\tknown\tno token\n"+
		"gitlab.company.example\tgitlab\tdeclared\ttoken\n", out)

	out, err = w.cmd(t, "hosts", "list", "--json")
	require.NoError(t, err)
	assert.NotContains(t, out, "glpat", "the API never returns a token")

	out, err = w.cmd(t, "hosts", "token", "rm", "github.com")
	require.NoError(t, err)
	assert.Equal(t, "host github.com has no token\n", out)

	_, err = w.cmd(t, "hosts", "rm", "gitlab.company.example")
	require.NoError(t, err, "a host is forgotten with its token")

	w.login(t, bearertoken.Coordinator)

	_, err = w.cmdIn(t, "glpat-x", "hosts", "token", "set", "gitlab.com", "--token-file", "-")
	require.Error(t, err, "only the owner gives a host a token")
}

// Scenario: the owner sets up the company's GitLab server and its repositories
// from a terminal, then starts a Run by a repository's short name. A
// coordinator reads the hosts and repositories and cannot change them.
func TestHostsAndRepositoriesFromTheCLI(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.login(t, bearertoken.Owner)

	out, err := w.cmd(t, "runs", "create", "--repo", "https://gitlab.company.example/team/app", "ship it")
	require.Error(t, err, out)
	require.ErrorContains(t, err, "hypomnema hosts add gitlab.company.example --kind gitlab")

	out, err = w.cmd(t, "hosts", "add", "https://GitLab.Company.Example/users/sign_in")
	require.NoError(t, err)
	assert.Equal(t, "host gitlab.company.example is gitlab\n", out, "a URL copied from the browser names its host")

	out, err = w.cmd(t, "hosts", "list")
	require.NoError(t, err)
	assert.Equal(t, "github.com\tgithub\tknown\tno token\ngitlab.com\tgitlab\tknown\tno token\n"+
		"gitlab.company.example\tgitlab\tdeclared\tno token\n", out)

	out, err = w.cmd(t, "repos", "add", "https://gitlab.company.example/team/Platform-API.git", "--base", "develop")
	require.NoError(t, err)
	assert.Equal(t, "repository platform-api is https://gitlab.company.example/team/Platform-API\n", out)

	_, err = w.cmd(t, "repos", "add", "kl3inIT/hypomnema", "--name", "hyp")
	require.NoError(t, err)

	out, err = w.cmd(t, "repos", "list")
	require.NoError(t, err)
	assert.Equal(t,
		"hyp\tkl3inIT/hypomnema\t\t\nplatform-api\thttps://gitlab.company.example/team/Platform-API\tdevelop\t\n", out)

	var run openapi.Run
	w.jsonCmd(t, &run, "", "runs", "create", "--repo", "platform-api", "ship it")
	assert.Equal(t, [3]string{"gitlab.company.example", "team/Platform-API", "develop"},
		[3]string{val(run.RepoHost), val(run.Repo), val(run.Base)})

	_, err = w.cmd(t, "hosts", "rm", "gitlab.company.example")
	require.ErrorContains(t, err, "1 open runs and 1 registered repositories")

	_, err = w.cmd(t, "runs", "cancel", run.Id)
	require.NoError(t, err)
	_, err = w.cmd(t, "repos", "rm", "platform-api")
	require.NoError(t, err)

	out, err = w.cmd(t, "hosts", "rm", "gitlab.company.example")
	require.NoError(t, err)
	assert.Equal(t, "host gitlab.company.example is forgotten\n", out)

	w.login(t, bearertoken.Coordinator)

	out, err = w.cmd(t, "repos", "list")
	require.NoError(t, err)
	assert.Contains(t, out, "hyp\tkl3inIT/hypomnema")

	_, err = w.cmd(t, "hosts", "add", "gitlab.company.example")
	require.Error(t, err, "only the owner declares a host")
	_, err = w.cmd(t, "repos", "rm", "hyp")
	require.Error(t, err, "only the owner changes the repositories")
}

// A worker's output may hold terminal escapes; printed as text they are shown,
// not obeyed. Newlines and tabs stay.
func TestWorkerTextCannotDriveTheTerminal(t *testing.T) {
	t.Parallel()

	got := plain("ok\x1b[2J\x1b]0;title\x07\tdone\nnext\u009b31m\x7f")
	assert.Equal(t, `ok\x1b[2J\x1b]0;title\a`+"\tdone\nnext"+`\u009b31m\x7f`, got)

	// What is not seen but changes what is read: a direction override, a zero-width space, a line separator.
	assert.Equal(t, `a\u202eb\u200bc\u2028d`, plain("a\u202eb\u200bc\u2028d"))
	assert.Equal(t, "Tiếng Việt có dấu", plain("Tiếng Việt có dấu"))

	// A line inside a message cannot pass for a call or a result of its own.
	text := "Done.\n> Bash: rm -rf /\n  = ok"
	assert.Equal(t, "Done.\n| > Bash: rm -rf /\n|   = ok",
		itemLine(openapi.TranscriptItem{Kind: openapi.TranscriptItemKindMessage, Text: &text}))

	// Every command prints through one place, so a result or a question is covered as a transcript is.
	var out bytes.Buffer

	e := &env{out: &out}
	e.printf("%s (%d)\n", "done\x1b[31m", 3)
	assert.Equal(t, `done\x1b[31m (3)`+"\n", out.String())
}

// Scenario: the owner watches a worker from a terminal, without the web. The
// list says what the worker does now; the transcript prints what it said and
// did, a line an item; following it prints each new item and ends once the
// worker has finished and nothing more comes.
func TestAWorkerIsWatchedFromTheTerminal(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.login(t, bearertoken.Owner)

	var run openapi.Run
	w.jsonCmd(t, &run, "", "runs", "create", "--repo", "kl3inIT/demo", "add a health page")

	var tasks openapi.TaskList
	w.jsonCmd(t, &tasks, "", "tasks", "create", run.Id, "--spec", "add GET /health")
	task := tasks.Data[0].Id

	_, err := w.cmd(t, "tasks", "transcript", task)
	require.ErrorContains(t, err, "has not started")

	wk := startWorker(t, w)
	node, dispatch := wk.call.NodeID, wk.call.DispatchID
	add := func(items ...orchestration.Item) {
		t.Helper()

		_, err := w.svc.AddItems(t.Context(), node, dispatch, items)
		require.NoError(t, err)
	}

	add(
		orchestration.Item{Seq: 1, Kind: orchestration.ItemMessage, Text: "I will add the handler."},
		orchestration.Item{
			Seq: 2, Kind: orchestration.ItemToolCall, CallID: "c1", Tool: "Bash",
			Input: json.RawMessage(`{"command":"go test ./...\necho done"}`),
		},
		orchestration.Item{Seq: 3, Kind: orchestration.ItemToolResult, CallID: "c1", Text: "FAIL\nTestHealth", Failed: true},
	)

	out, err := w.cmd(t, "tasks", "list")
	require.NoError(t, err)
	assert.Contains(t, out, task+"\trunning\t"+run.Id+"\tBash: go test ./...")

	out, err = w.cmd(t, "tasks", "transcript", task)
	require.NoError(t, err)
	assert.Equal(t, "I will add the handler.\n> Bash: go test ./...\n  = FAIL [failed]\n", out)

	out, err = w.cmd(t, "tasks", "transcript", task, "--after", "2", "--json")
	require.NoError(t, err)

	var item openapi.TranscriptItem
	require.NoError(t, json.Unmarshal([]byte(out), &item), out)
	assert.Equal(t, int64(3), item.Seq)

	var followed syncBuffer

	done := make(chan error, 1)

	go func() {
		done <- Command(t.Context(), w.home, []string{"tasks", "transcript", task, "--after", "3", "--follow"},
			strings.NewReader(""), &followed)
	}()

	time.Sleep(200 * time.Millisecond)
	add(orchestration.Item{Seq: 4, Kind: orchestration.ItemMessage, Text: "All tests pass now."})
	require.Eventually(t, func() bool { return strings.Contains(followed.String(), "All tests pass now.") },
		5*time.Second, 20*time.Millisecond, "a new item is printed while the worker runs")

	wk.succeed(t, w)

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("following did not end when the worker finished")
	}

	out, err = w.cmd(t, "tasks", "list")
	require.NoError(t, err)
	assert.Contains(t, out,
		task+"\tsucceeded\t"+run.Id+"\tadded GET /health  https://github.com/kl3inIT/demo/pull/7")
}

// Scenario: the owner reads the skill from the binary and installs it for
// Claude Code and for pi and Codex. A second install changes nothing; a
// copy the owner edited is left alone unless --force, which keeps a backup.
func TestSkillsGetAndInstall(t *testing.T) { //nolint:paralleltest // replaces userHome
	home := t.TempDir()
	userHome = func() (string, error) { return home, nil }
	t.Setenv("CLAUDE_CONFIG_DIR", "")

	t.Cleanup(func() { userHome = os.UserHomeDir })

	w := &world{home: filepath.Join(home, ".hypomnema")}

	out, err := w.cmd(t, "skills", "get")
	require.NoError(t, err)
	assert.Contains(t, out, "# Coordinating Hypomnema workers")

	out, err = w.cmd(t, "skills", "get", "hypomnema-coordinator", "--references")
	require.NoError(t, err)
	assert.Equal(t, "investigate\nrecovery\nreview\n", out)

	out, err = w.cmd(t, "skills", "get", "hypomnema-coordinator", "--reference", "recovery")
	require.NoError(t, err)
	assert.Contains(t, out, "# Recovery")

	_, err = w.cmd(t, "skills", "get", "hypomnema-coordinator", "--reference", "nothing")
	require.ErrorIs(t, err, ErrUsage)

	out, err = w.cmd(t, "skills", "install", "--dry-run")
	require.NoError(t, err)
	assert.Contains(t, out, "would install")
	assert.NoFileExists(t, filepath.Join(home, ".claude", "skills", "hypomnema-coordinator", "SKILL.md"))

	out, err = w.cmd(t, "skills", "install")
	require.NoError(t, err)
	// One stub a skill, for each of the two agents' folders.
	assert.Equal(t, 2*len(skillNames()), strings.Count(out, "installed"), out)

	claude := filepath.Join(home, ".claude", "skills", "hypomnema-coordinator", "SKILL.md")
	agents := filepath.Join(home, ".agents", "skills", "hypomnema-coordinator", "SKILL.md")
	assert.FileExists(t, agents)

	out, err = w.cmd(t, "skills", "install")
	require.NoError(t, err)
	assert.Equal(t, 2*len(skillNames()), strings.Count(out, "is current"), out)

	require.NoError(t, os.WriteFile(claude, []byte("my own notes"), 0o600))

	out, err = w.cmd(t, "skills", "install", "--agent", "claude")
	require.NoError(t, err)
	assert.Contains(t, out, "was edited")

	got, err := os.ReadFile(claude) //nolint:gosec // a file under the test's own temporary home
	require.NoError(t, err)
	assert.Equal(t, "my own notes", string(got))

	_, err = w.cmd(t, "skills", "install", "--agent", "claude", "--force")
	require.NoError(t, err)

	backup, err := os.ReadFile(claude + ".bak") //nolint:gosec // a file under the test's own temporary home
	require.NoError(t, err)
	assert.Equal(t, "my own notes", string(backup))
}

// The installed stub and the guide route the same way: their frontmatter is
// one text (Orca copies it byte for byte).
func TestStubAndGuideShareTheirFrontmatter(t *testing.T) {
	t.Parallel()

	for _, name := range skillNames() {
		stub, err := skillFiles.ReadFile("skills/" + name + "/SKILL.md")
		require.NoError(t, err)

		guide, err := skillFiles.ReadFile("skills/" + name + "/guide.md")
		require.NoError(t, err)

		assert.Equal(t, frontmatter(t, string(stub)), frontmatter(t, string(guide)), name)
		assert.Contains(t, frontmatter(t, string(stub)), "name: "+name+"\n", "the name is the folder's")
	}
}

func frontmatter(t *testing.T, s string) string {
	t.Helper()

	parts := strings.SplitN(strings.ReplaceAll(s, "\r\n", "\n"), "---\n", 3)
	require.Len(t, parts, 3)

	return parts[1]
}

// A stub that changes adds its digest to releasedStubs, so `skills install`
// can later update copies of it.
func TestShippedStubsAreReleased(t *testing.T) {
	t.Parallel()

	for _, name := range skillNames() {
		stub, err := skillFiles.ReadFile("skills/" + name + "/SKILL.md")
		require.NoError(t, err)
		assert.Contains(t, releasedStubs[name], digest(stub), "add the stub's digest to releasedStubs[%q]", name)
	}
}

// Every hypomnema command the skills name exists, with only flags it
// accepts and the number of arguments it takes (Orca's
// bundled-guide-flags.test.ts).
func TestSkillCommandsParse(t *testing.T) {
	t.Parallel()

	commandLine := regexp.MustCompile("hypomnema [a-z][^`\n]*")
	placeholder := regexp.MustCompile(`<[^>]+>`)
	found := 0

	err := fsWalk(func(file, text string) {
		for _, line := range commandLine.FindAllString(text, -1) {
			if strings.Contains(line, "<command>") || !strings.Contains(line, " ") {
				continue // the generic `hypomnema <command> --help`
			}

			args := strings.Fields(placeholder.ReplaceAllString(line, "1"))[1:]

			c, rest := find(args)
			if !assert.NotNil(t, c, "%s: %q is not a command", file, line) {
				continue
			}

			fs := flagSet(c, io.Discard)
			if assert.NoError(t, parse(fs, rest), "%s: %q", file, line) {
				n := fs.NArg()
				assert.True(t, n >= c.min && (c.max < 0 || n <= c.max), "%s: %q takes %s", file, line, c.args)
			}

			found++
		}
	})
	require.NoError(t, err)
	assert.Greater(t, found, 15, "the check found the skills' commands")
}

func fsWalk(fn func(file, text string)) error {
	for _, name := range skillNames() {
		files := []string{"SKILL.md", "guide.md"}

		refs, err := references(name)
		if err != nil {
			return err
		}

		for _, r := range refs {
			files = append(files, "references/"+r+".md")
		}

		for _, f := range files {
			b, err := skillFiles.ReadFile("skills/" + name + "/" + f)
			if err != nil {
				return err
			}

			fn(name+"/"+f, string(b))
		}
	}

	return nil
}

func TestParseAnswer(t *testing.T) {
	t.Parallel()

	options := []openapi.Option{{Label: "red"}, {Label: "Green"}}
	option := func(i int) *int { return &i }
	text := func(s string) *string { return &s }

	for _, c := range []struct {
		typed string
		want  openapi.Answer
	}{
		{"2", openapi.Answer{Option: option(1)}},
		{"green", openapi.Answer{Option: option(1)}},
		{" RED ", openapi.Answer{Option: option(0)}},
		{"blue please", openapi.Answer{Text: text("blue please")}},
	} {
		assert.Equal(t, c.want, ParseAnswer(options, c.typed), c.typed)
	}

	assert.Equal(t, openapi.Answer{Text: text("8080")}, ParseAnswer(nil, "8080"), "a number without options is text")
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

// Scenario: the owner changes the reviewer profile from the laptop; applying
// the same file again changes nothing. A coordinator lists the profiles and
// models to choose from but cannot change them.
func TestProfilesFromTheCLI(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.login(t, bearertoken.Owner)

	file := filepath.Join(t.TempDir(), "reviewer.yaml")
	source := "name: reviewer\ndescription: d\nruntime: claude\nmodel:\n  name: opus\n"
	require.NoError(t, os.WriteFile(file, []byte(source), 0o600))

	out, err := w.cmd(t, "profiles", "apply", file)
	require.NoError(t, err)
	assert.Contains(t, out, "profile reviewer: new version v4")

	out, err = w.cmd(t, "profiles", "apply", file)
	require.NoError(t, err)
	assert.Contains(t, out, "unchanged, still v4")

	var versions openapi.ProfileList
	w.jsonCmd(t, &versions, "", "profiles", "versions", "reviewer")
	assert.Len(t, versions.Data, 4)

	coordinator := &world{svc: w.svc, url: w.url, home: filepath.Join(t.TempDir(), "coordinator")}
	coordinator.login(t, bearertoken.Coordinator)

	var profiles openapi.ProfileList
	coordinator.jsonCmd(t, &profiles, "", "profiles", "list")
	assert.Len(t, profiles.Data, 3)

	// The list says which profiles no worker can start under now, and why.
	out, err = coordinator.cmd(t, "profiles", "list")
	require.NoError(t, err)
	assert.Contains(t, out, "cannot run now: no_node")

	var models openapi.ModelList
	coordinator.jsonCmd(t, &models, "", "models", "list")
	assert.NotEmpty(t, models.Data)

	_, err = coordinator.cmd(t, "profiles", "apply", file)
	require.ErrorContains(t, err, "forbidden")
}

// replier plays pi for chat tests: it takes each owner message and answers.
type replier struct{ svc *orchestration.Service }

func (replier) Enabled() bool         { return true }
func (replier) Working(_ string) bool { return false }

func (replier) Watch(_ string) (<-chan coordinator.Frame, func()) { return nil, func() {} }

func (replier) Abort(_ context.Context, _ string) error { return nil }

func (replier) Key(_ string) (string, bool) { return "", false }

func (r replier) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(20 * time.Millisecond):
		}

		chats, _ := r.svc.ChatsWithUndelivered(ctx)
		for _, id := range chats {
			msgs, _ := r.svc.Undelivered(ctx, id)
			if len(msgs) == 0 {
				continue
			}

			_ = r.svc.MarkDelivered(ctx, id, msgs[len(msgs)-1].Seq)
			_, _ = r.svc.AddChatMessage(ctx, id, orchestration.ChatSystem, "pi ran bash: hypomnema runs list --json")
			_, _ = r.svc.AddChatMessage(ctx, id, orchestration.ChatCoordinator, "I read: "+msgs[0].Text)
		}
	}
}

// Scenario: the owner chats with pi from the terminal. The first message
// starts a chat; the reply and pi's tool line are printed; the next message
// goes to the same chat. Without a message, the chat is shown.
func TestChatFromTheTerminal(t *testing.T) {
	t.Parallel()

	var w *world

	rp := &replier{}
	w = newWorldWith(t, rp)
	rp.svc = w.svc

	ctx, cancel := context.WithCancel(context.WithoutCancel(t.Context()))
	t.Cleanup(cancel)

	go rp.run(ctx)

	w.login(t, bearertoken.Owner)

	out, err := w.cmd(t, "chat", "add", "a", "health", "page")
	require.NoError(t, err)
	assert.Contains(t, out, "pi: I read: add a health page")
	assert.Contains(t, out, "· pi ran bash: hypomnema runs list --json")

	chatID := regexp.MustCompile(`chat (cht_\w+)`).FindStringSubmatch(out)[1]

	out, err = w.cmdIn(t, "and a test\n", "chat", "--text-file", "-")
	require.NoError(t, err)
	assert.Contains(t, out, "chat "+chatID, "the most recent chat")
	assert.Contains(t, out, "pi: I read: and a test")

	out, err = w.cmd(t, "chat", "--last", "2")
	require.NoError(t, err)
	assert.Contains(t, out, "pi: I read: and a test")
	assert.NotContains(t, out, "you: add a health page", "only the last two")

	var chats openapi.ChatList
	w.jsonCmd(t, &chats, "", "chats")
	require.Len(t, chats.Data, 1)
	assert.Equal(t, "add a health page", chats.Data[0].Title, "a new chat is titled by its first message")

	out, err = w.cmd(t, "chat", "--new", "a second topic")
	require.NoError(t, err)
	assert.NotContains(t, out, "chat "+chatID)
}

// Without a coordinator on the server, a message is kept and the CLI says so.
func TestChatWithoutACoordinator(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.login(t, bearertoken.Owner)

	out, err := w.cmd(t, "chat", "hello")
	require.NoError(t, err)
	assert.Contains(t, out, "no coordinator runs on the server")
}

// pi's environment names its chat; the Runs it creates record it.
func TestRunsCreatedByPiRecordItsChat(t *testing.T) { //nolint:paralleltest // sets HYPOMNEMA_CHAT_ID
	w := newWorld(t)
	w.login(t, bearertoken.Coordinator)

	chat, err := w.svc.CreateChat(t.Context(), "", "")
	require.NoError(t, err)

	t.Setenv("HYPOMNEMA_CHAT_ID", chat.ID)

	var run openapi.Run
	w.jsonCmd(t, &run, "", "runs", "create", "--repo", "kl3inIT/demo", "health page")
	assert.Equal(t, chat.ID, val(run.ChatId))
}

// signIns stands in for the service that signs accounts in: google, whose
// sign-in is completed by any address that carries its state, and lark, an
// app that is connected once it has a client.
type signIns struct {
	mu          sync.Mutex
	clientID    string
	secret      string
	state       string
	asked       []string
	label       string
	connections []integration.Connection

	appID     string
	appSecret string
	apps      []integration.Connection
}

func (s *signIns) List(context.Context) ([]integration.Integration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return []integration.Integration{
		{Definition: integration.Google(), ClientID: s.clientID, Connections: slices.Clone(s.connections)},
		{Definition: integration.Lark(), ClientID: s.appID, Connections: slices.Clone(s.apps)},
	}, nil
}

func (s *signIns) SetClient(_ context.Context, id, clientID, clientSecret string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if id == integration.LarkID {
		s.appID, s.appSecret = clientID, clientSecret

		return nil
	}

	s.clientID, s.secret = clientID, clientSecret

	return nil
}

func (s *signIns) Connect(_ context.Context, id, label string) (integration.Connection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if id != integration.LarkID || s.appID == "" {
		return integration.Connection{}, integration.ErrInvalid
	}

	c := integration.Connection{Integration: id, Label: label, Account: "Hypomnema"}
	s.apps = append(s.apps, c)

	return c, nil
}

func (s *signIns) ConnectAccount(context.Context, string, string, integration.Account) (integration.Connection, error) {
	return integration.Connection{}, integration.ErrInvalid
}

func (s *signIns) Begin(_ context.Context, _, label string, services []string) (integration.SignIn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.state, s.label, s.asked = "state-1", label, services

	return integration.SignIn{
		ID: "signin-1", URL: "https://accounts.example/auth?state=state-1", ExpiresIn: 30 * time.Minute,
	}, nil
}

func (s *signIns) Complete(_ context.Context, _, id, pasted string) (integration.Connection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if id != "signin-1" || !strings.Contains(pasted, "state="+s.state) {
		return integration.Connection{}, integration.ErrSignInFailed
	}

	grants, err := integration.Google().ParseGrants(s.asked)
	if err != nil {
		return integration.Connection{}, err
	}

	c := integration.Connection{
		Integration: integration.GoogleID, Label: s.label, Account: "owner@example.com", Grants: grants,
	}
	s.connections = append(s.connections, c)

	return c, nil
}

func (s *signIns) SetChats(_ context.Context, _, label string, allowed bool) (integration.Connection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.connections {
		if s.connections[i].Label == label {
			s.connections[i].Chats = allowed

			return s.connections[i], nil
		}
	}

	return integration.Connection{}, integration.ErrNotFound
}

func (s *signIns) Disconnect(_ context.Context, id, label string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, c := range s.apps {
		if id == integration.LarkID && c.Label == label {
			s.apps = slices.Delete(s.apps, i, i+1)

			return true, nil
		}
	}

	for i, c := range s.connections {
		if c.Label == label {
			s.connections = slices.Delete(s.connections, i, i+1)

			return false, nil
		}
	}

	return false, integration.ErrNotFound
}

// Scenario: the owner gives Hypomnema his Lark app from a file with its id
// and secret, connects it as "work" with the same command he connects a
// Google account with, sees it in the list, and disconnects it. There is no
// link to open and nothing to paste; the secret is read from the file and
// never printed; a coordinator cannot connect the app.
func TestConnectingALarkAppFromTheCLI(t *testing.T) {
	t.Parallel()

	services := &signIns{}
	w := newWorldWith(t, nil, func(s *clientapi.Server) { s.WithIntegrations(services) })
	w.login(t, bearertoken.Owner)

	out, err := w.cmd(t, "integrations", "list")
	require.NoError(t, err)
	assert.Contains(t, out, "lark\tapp\tno client\n")

	out, err = w.cmdIn(t, `{"app_id":"cli_a1b2","app_secret":"never-printed"}`, "integrations", "client", "set", "lark",
		"--client-file", "-")
	require.NoError(t, err)
	assert.Equal(t, "lark is app cli_a1b2\n", out)
	assert.Equal(t, "never-printed", services.appSecret)

	out, err = w.cmd(t, "integrations", "connect", "lark", "--label", "work")
	require.NoError(t, err)
	assert.Equal(t, "lark:work is connected as the app Hypomnema\n", out)

	out, err = w.cmd(t, "integrations", "list")
	require.NoError(t, err)
	assert.Contains(t, out, "lark\tapp\twork (Hypomnema)\n")

	out, err = w.cmd(t, "integrations", "disconnect", "lark:work")
	require.NoError(t, err)
	assert.Equal(t, "lark:work is forgotten\n", out)

	w.login(t, bearertoken.Coordinator)

	_, err = w.cmd(t, "integrations", "connect", "lark", "--label", "work")
	require.Error(t, err, "only the owner connects an app")
	assert.Empty(t, services.apps)
}

// Scenario: the owner gives Hypomnema the Google client from the file Google
// gave him, signs his work account in by pasting the address the browser
// ended on, sees it beside the git hosts, and disconnects it. The client's
// secret is read from the file and never printed; a coordinator can see the
// account but cannot sign one in.
func TestSigningAGoogleAccountInFromTheCLI(t *testing.T) {
	t.Parallel()

	google := &signIns{}
	w := newWorldWith(t, nil, func(s *clientapi.Server) { s.WithIntegrations(google) })
	w.login(t, bearertoken.Owner)

	out, err := w.cmd(t, "integrations", "list")
	require.NoError(t, err)
	assert.Contains(t, out, "google\toauth\tno client\n")

	_, err = w.cmd(t, "integrations", "client", "set", "google")
	require.ErrorContains(t, err, "--client-file")

	_, err = w.cmdIn(t, "not json", "integrations", "client", "set", "google", "--client-file", "-")
	require.ErrorContains(t, err, "not JSON")

	const clientFile = `{"installed":{"client_id":"1234.apps.example","client_secret":"GOCSPX-never-printed",` +
		`"redirect_uris":["http://localhost"]}}`

	out, err = w.cmdIn(t, clientFile, "integrations", "client", "set", "google", "--client-file", "-", "--json")
	require.NoError(t, err)
	assert.Contains(t, out, "1234.apps.example")
	assert.NotContains(t, out, "GOCSPX")
	assert.Equal(t, "GOCSPX-never-printed", google.secret)

	_, err = w.cmdIn(t, "\n", "integrations", "connect", "google", "--label", "work", "--services", "gmail:read")
	require.ErrorContains(t, err, "no address was pasted")

	_, err = w.cmdIn(t, "http://localhost:1/?state=another&code=c\n", "integrations", "connect", "google", "--label",
		"work", "--services", "gmail:read")
	require.Error(t, err, "an address of another sign-in completes nothing")

	out, err = w.cmdIn(t, "http://localhost:1/?state=state-1&code=c\n", "integrations", "connect", "google",
		"--label", "work", "--services", "gmail:read,calendar")
	require.NoError(t, err)
	assert.Contains(t, out, "https://accounts.example/auth?state=state-1")
	assert.Contains(t, out, "google:work is connected as owner@example.com, with gmail:read,calendar\n")

	out, err = w.cmd(t, "integrations", "list")
	require.NoError(t, err)
	assert.Contains(t, out, "github.com\tgithub\tnot connected\n")
	assert.Contains(t, out, "google\toauth\twork (owner@example.com: gmail:read,calendar)\n")

	out, err = w.cmd(t, "integrations", "disconnect", "google:work")
	require.NoError(t, err)
	assert.Equal(t, "google:work is forgotten\nthe service could not be asked to end the access; remove it in the "+
		"account's own settings\n", out)

	_, err = w.cmd(t, "integrations", "disconnect", "google:work")
	require.Error(t, err, "it is gone")

	_, err = w.cmdIn(t, "http://localhost:1/?state=state-1&code=c\n", "integrations", "connect", "google",
		"--label", "personal", "--services", "drive")
	require.NoError(t, err)

	// No agent uses the account until the owner says so.
	out, err = w.cmd(t, "integrations", "chats", "google:personal", "on")
	require.NoError(t, err)
	assert.Equal(t, "the coordinator may use google:personal in chats\n", out)

	_, err = w.cmd(t, "integrations", "chats", "google:personal", "maybe")
	require.ErrorContains(t, err, "on or off")

	w.login(t, bearertoken.Coordinator)

	out, err = w.cmd(t, "integrations", "list")
	require.NoError(t, err)
	assert.Contains(t, out, "personal (owner@example.com: drive) chats")

	_, err = w.cmd(t, "integrations", "chats", "google:personal", "off")
	require.Error(t, err, "only the owner decides what the coordinator may use")

	_, err = w.cmdIn(t, "http://localhost:1/?state=state-1&code=c\n", "integrations", "connect", "google",
		"--label", "mine", "--services", "gmail")
	require.Error(t, err, "only the owner signs an account in")

	_, err = w.cmd(t, "integrations", "disconnect", "google:personal")
	require.Error(t, err, "only the owner disconnects")
	assert.Len(t, google.connections, 1)
}

// decider answers Approvals as the service proxy does, without a proxy.
// The world it answers in is made after it is wired, so it holds where the
// world will be.
type decider struct{ w **world }

func (d decider) Decide(ctx context.Context, id string, approve bool, scope orchestration.ApprovalScope,
) (orchestration.Approval, error) {
	return (*d.w).svc.DecideApproval(ctx, id, approve, scope)
}

// Scenario: the owner looks at what agents may do with his Google account,
// forbids sending, and answers a request that waits for him. The coordinator
// can do none of it: an agent that could approve would not be asking.
func TestTheOwnerSetsActionsAndAnswersApprovals(t *testing.T) {
	t.Parallel()

	var w *world

	w = newWorldWith(t, nil, func(s *clientapi.Server) { s.WithApprovals(decider{w: &w}) })
	ctx := t.Context()

	_, err := w.svc.SaveServiceConnection(ctx, orchestration.Connection{
		Integration: "google", Label: "work", Account: "owner@example.com", Secret: "conn-google-work",
	})
	require.NoError(t, err)

	chat, err := w.svc.CreateChat(ctx, "", "Reply to the invoice")
	require.NoError(t, err)

	w.login(t, bearertoken.Owner)

	out, err := w.cmd(t, "integrations", "actions", "google:work")
	require.NoError(t, err)
	assert.Contains(t, out, "gmail.send         ask    Gmail: Send mail\n")
	assert.Contains(t, out, "gmail.read         allow  Gmail: Read mail\n")
	assert.NotContains(t, out, "discovery", "what a tool needs to run at all is not the owner's to answer")

	out, err = w.cmd(t, "integrations", "actions", "set", "google:work", "gmail.send", "never")
	require.NoError(t, err)
	assert.Equal(t, "gmail.send         never  Gmail: Send mail  (default ask)\n", out)

	_, err = w.cmd(t, "integrations", "actions", "set", "google:work", "gmail.send", "sometimes")
	require.ErrorContains(t, err, "allow, ask or never")

	_, err = w.cmd(t, "integrations", "actions", "set", "google:work", "gmail.fly", "ask")
	require.ErrorContains(t, err, "no action gmail.fly")

	_, err = w.cmd(t, "integrations", "actions", "set", "google:other", "gmail.send", "ask")
	require.Error(t, err, "an account that is not connected")

	out, err = w.cmd(t, "approvals")
	require.NoError(t, err)
	assert.Equal(t, "nothing waits for your approval\n", out)

	asked, granted, err := w.svc.AskApproval(ctx, orchestration.NewApproval{
		Integration: "google", Label: "work", Action: "calendar.write", ActionName: "Create or change an event",
		ChatID: chat.ID, AskedBy: "pi", Title: "Create or change an event: Review", MatchHash: "digest-1",
		Payload: []orchestration.Field{{Label: "summary", Value: "Review"}, {Label: "notes", Value: "one\ntwo"}},
	})
	require.NoError(t, err)
	require.False(t, granted)

	out, err = w.cmd(t, "approvals")
	require.NoError(t, err)
	assert.Equal(t, asked.ID+"  pending  Create or change an event: Review\n"+
		"  google:work · Create or change an event · asked by pi in chat "+chat.ID+"\n"+
		"  summary: Review\n  notes: \n    one\n    two\n", out)

	out, err = w.cmd(t, "approvals", "approve", asked.ID, "--session")
	require.NoError(t, err)
	assert.Contains(t, out, asked.ID+"  approved for the session  ")

	_, err = w.cmd(t, "approvals", "reject", asked.ID)
	require.Error(t, err, "the first answer stands")

	w.login(t, bearertoken.Coordinator)

	for _, args := range [][]string{
		{"approvals"}, {"approvals", "approve", asked.ID}, {"integrations", "actions", "google:work"},
		{"integrations", "actions", "set", "google:work", "gmail.send", "allow"},
	} {
		_, err = w.cmd(t, args...)
		require.Error(t, err, "only the owner: %v", args)
	}
}
