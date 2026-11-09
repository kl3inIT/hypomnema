package clientcli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
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
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

// world is a control plane serving the client API, and a home for the CLI.
type world struct {
	svc  *orchestration.Service
	url  string
	home string
}

func newWorld(t *testing.T) *world {
	t.Helper()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: filepath.Join(t.TempDir(), "hypomnema.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, orchestration.Migrate(t.Context(), db))

	svc := orchestration.New(db, platform.SystemClock{}, platform.NewID,
		orchestration.WithRoles(map[string]orchestration.Role{"implement": {Runtime: "claude"}}))
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })

	mux := http.NewServeMux()
	require.NoError(t, clientapi.New(svc, slog.New(slog.DiscardHandler), stop).Register(mux))

	hs := httptest.NewServer(mux)
	t.Cleanup(hs.Close)

	return &world{svc: svc, url: hs.URL, home: filepath.Join(t.TempDir(), "home")}
}

// login makes a token of kind and logs the CLI in with it.
func (w *world) login(t *testing.T, kind bearertoken.Kind) string {
	t.Helper()

	_, secret, err := w.svc.AddToken(t.Context(), "cli-"+string(kind), kind)
	require.NoError(t, err)

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
	require.ErrorContains(t, err, "validation_failed")

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
		{"ref": "api", "role": "implement", "spec": "add GET /health"},
		{"ref": "docs", "role": "implement", "spec": "document it", "depends_on": ["api"]}
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
	assert.Equal(t, "recovery\n", out)

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
	assert.Equal(t, 2, strings.Count(out, "installed"), out)

	claude := filepath.Join(home, ".claude", "skills", "hypomnema-coordinator", "SKILL.md")
	agents := filepath.Join(home, ".agents", "skills", "hypomnema-coordinator", "SKILL.md")
	assert.FileExists(t, agents)

	out, err = w.cmd(t, "skills", "install")
	require.NoError(t, err)
	assert.Equal(t, 2, strings.Count(out, "is current"), out)

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
