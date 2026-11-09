// Package clientcli holds the hypomnema commands that call the client API
// (issue #41): login, run, answer and questions. They need no database, so
// they work from the owner's laptop as well as on the server. `login` keeps
// the URL and token in ~/.hypomnema/client.json, as `hypomnema-node init`
// keeps the node's.
package clientcli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
)

// ErrUsage marks a command line that is wrong.
var ErrUsage = errors.New("usage")

// Commands lists the commands this package runs.
var Commands = []string{"login", "run", "answer", "questions"}

const (
	basePath = "/api/v1"
	// waitSeconds is how long one events request waits for the next event.
	waitSeconds = 50
	// requestTimeout covers that wait.
	requestTimeout = 70 * time.Second
)

// Config is where the client API is and the token it is called with.
type Config struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

// DefaultHome is ~/.hypomnema.
func DefaultHome() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("finding the home directory: %w", err)
	}

	return filepath.Join(home, ".hypomnema"), nil
}

func configPath(home string) string { return filepath.Join(home, "client.json") }

// Command runs one client command with the configuration in home.
func Command(ctx context.Context, home string, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return ErrUsage
	}

	if args[0] == "login" {
		return login(ctx, home, args[1:], stdout)
	}

	cfg, err := LoadConfig(home)
	if err != nil {
		return err
	}

	c, err := newClient(cfg)
	if err != nil {
		return err
	}

	o := &owner{api: c, out: stdout}

	switch args[0] {
	case "run":
		return o.run(ctx, args[1:])
	case "answer":
		return o.answer(ctx, args[1:])
	case "questions":
		if len(args) != 1 {
			return ErrUsage
		}

		return o.questions(ctx)
	default:
		return ErrUsage
	}
}

func newClient(cfg Config) (*openapi.ClientWithResponses, error) {
	c, err := openapi.NewClientWithResponses(cfg.URL+basePath,
		openapi.WithHTTPClient(&http.Client{Timeout: requestTimeout}),
		openapi.WithRequestEditorFn(func(_ context.Context, r *http.Request) error {
			r.Header.Set("Authorization", "Bearer "+cfg.Token)

			return nil
		}))
	if err != nil {
		return nil, fmt.Errorf("making the API client: %w", err)
	}

	return c, nil
}

// login checks the token against the control plane and saves both. The
// token is read from a file, so it never appears on a command line.
func login(ctx context.Context, home string, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	rawURL := fs.String("url", "", "the control plane, http(s)://host[:port]")
	tokenFile := fs.String("token-file", "", "a file holding the token from `hypomnema tokens add`")

	if err := fs.Parse(args); err != nil || *rawURL == "" || *tokenFile == "" || fs.NArg() != 0 {
		return fmt.Errorf("%w: login --url URL --token-file FILE", ErrUsage)
	}

	u, err := url.Parse(*rawURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return fmt.Errorf("the control plane URL %q must be http(s)://host", *rawURL)
	}

	raw, err := os.ReadFile(*tokenFile)
	if err != nil {
		return fmt.Errorf("reading the token file: %w", err)
	}

	cfg := Config{URL: strings.TrimSuffix(*rawURL, "/"), Token: strings.TrimSpace(string(raw))}
	if !strings.HasPrefix(cfg.Token, "hyp_") {
		return errors.New("the token file does not hold a client token (hyp_…)")
	}

	c, err := newClient(cfg)
	if err != nil {
		return err
	}

	me, err := c.GetMeWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("reaching %s: %w", cfg.URL, err)
	}

	if me.JSON200 == nil {
		return apiError("checking the token", me.StatusCode(), me.JSONDefault)
	}

	if err := saveConfig(home, cfg); err != nil {
		return err
	}

	_, _ = fmt.Fprintf(stdout, "logged in to %s as %s (%s)\n", cfg.URL, me.JSON200.Name, me.JSON200.Kind)

	return nil
}

func saveConfig(home string, cfg Config) error {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return fmt.Errorf("making %s: %w", home, err)
	}

	if err := os.Chmod(home, 0o700); err != nil { //nolint:gosec // a directory needs its execute bit; 0700 is owner-only
		return fmt.Errorf("restricting %s: %w", home, err)
	}

	body, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding the configuration: %w", err)
	}

	// Written whole and renamed into place, so a crash never leaves half a file.
	tmp := configPath(home) + ".tmp"
	if err := os.WriteFile(tmp, append(body, '\n'), 0o600); err != nil {
		return fmt.Errorf("writing the configuration: %w", err)
	}

	if err := os.Rename(tmp, configPath(home)); err != nil {
		return fmt.Errorf("saving the configuration: %w", err)
	}

	return nil
}

// LoadConfig reads the configuration `login` wrote.
func LoadConfig(home string) (Config, error) {
	raw, err := os.ReadFile(configPath(home))
	if err != nil {
		return Config{}, fmt.Errorf("reading the configuration (run `hypomnema login` first): %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("decoding %s: %w", configPath(home), err)
	}

	if cfg.URL == "" || cfg.Token == "" {
		return Config{}, fmt.Errorf("%s needs url and token", configPath(home))
	}

	return cfg, nil
}

// apiError turns an error answer into an error.
func apiError(doing string, status int, e *openapi.Error) error {
	if e == nil {
		return fmt.Errorf("%s: HTTP %d", doing, status)
	}

	return fmt.Errorf("%s: %s (%s)", doing, e.Message, e.Code)
}

// newKey makes an Idempotency-Key, so a create the CLI repeats happens once.
func newKey() *string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	k := hex.EncodeToString(b)

	return &k
}

type owner struct {
	api *openapi.ClientWithResponses
	out io.Writer
}

func (o *owner) printf(format string, a ...any) {
	_, _ = fmt.Fprintf(o.out, format, a...)
}

// run creates a Run with one Task and follows it until the Task ends.
func (o *owner) run(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	repo := fs.String("repo", "", "repository the work happens in, owner/name")
	base := fs.String("base", "", "branch the worktree starts from (default main)")
	role := fs.String("role", "implement", "role of the Task")
	runtime := fs.String("runtime", "", "runtime, instead of the role's default")
	model := fs.String("model", "", "model, instead of the role's default")
	detach := fs.Bool("detach", false, "create the Run and return without following it")

	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %w", ErrUsage, err)
	}

	objective := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if objective == "" || *repo == "" {
		return fmt.Errorf("%w: run needs --repo owner/name and an objective", ErrUsage)
	}

	nr := openapi.NewRun{Objective: objective, Repo: repo}
	if *base != "" {
		nr.Base = base
	}

	created, err := o.api.CreateRunWithResponse(ctx, &openapi.CreateRunParams{IdempotencyKey: newKey()}, nr)
	if err != nil {
		return fmt.Errorf("creating the run: %w", err)
	}

	if created.JSON201 == nil {
		return apiError("creating the run", created.StatusCode(), created.JSONDefault)
	}

	runID := created.JSON201.Id

	task := openapi.NewTask{Ref: "main", Role: *role, Spec: objective}
	if *runtime != "" {
		task.Runtime = runtime
	}

	if *model != "" {
		task.Model = model
	}

	tasks, err := o.api.CreateTasksWithResponse(ctx, runID, &openapi.CreateTasksParams{IdempotencyKey: newKey()},
		openapi.NewTasks{Tasks: []openapi.NewTask{task}})
	if err != nil {
		return fmt.Errorf("creating the task: %w", err)
	}

	if tasks.JSON201 == nil {
		return apiError("creating the task", tasks.StatusCode(), tasks.JSONDefault)
	}

	taskID := tasks.JSON201.Data[0].Id
	o.printf("run %s, task %s\n", runID, taskID)

	if *detach {
		return nil
	}

	return o.follow(ctx, runID, taskID)
}

// follow prints the Run's events until its Task ends, then finishes the Run.
// Each request waits for the next event, so nothing polls.
func (o *owner) follow(ctx context.Context, runID, taskID string) error {
	var after int64

	wait := waitSeconds

	for {
		page, err := o.api.ListEventsWithResponse(ctx, &openapi.ListEventsParams{
			After: &after, RunId: &runID, Wait: &wait,
		})

		if ctx.Err() != nil {
			o.printf("stopped following; run %s goes on\n", runID)

			return nil //nolint:nilerr // Stopping (Ctrl-C) leaves the Run going; it is not a failure.
		}

		if err != nil {
			return fmt.Errorf("reading events: %w", err)
		}

		if page.JSON200 == nil {
			return apiError("reading events", page.StatusCode(), page.JSONDefault)
		}

		for _, e := range page.JSON200.Data {
			if err := o.printEvent(ctx, e); err != nil {
				return err
			}
		}

		after = page.JSON200.NextAfter

		if page.JSON200.HasMore {
			continue
		}

		done, err := o.taskEnded(ctx, runID, taskID)
		if done || err != nil {
			return err
		}
	}
}

// taskEnded reports whether the Task has ended; if it has, it finishes the
// Run and prints the result.
func (o *owner) taskEnded(ctx context.Context, runID, taskID string) (bool, error) {
	task, err := o.api.GetTaskWithResponse(ctx, taskID)
	if err != nil {
		return false, fmt.Errorf("reading the task: %w", err)
	}

	if task.JSON200 == nil {
		return false, apiError("reading the task", task.StatusCode(), task.JSONDefault)
	}

	switch task.JSON200.Status {
	case openapi.TaskStatusSucceeded, openapi.TaskStatusFailed, openapi.TaskStatusCanceled:
	default:
		return false, nil
	}

	finished, err := o.api.FinishRunWithResponse(ctx, runID)
	if err != nil {
		return true, fmt.Errorf("finishing the run: %w", err)
	}

	if finished.JSON200 == nil && (finished.JSONDefault == nil || finished.JSONDefault.Code != openapi.Conflict) {
		return true, apiError("finishing the run", finished.StatusCode(), finished.JSONDefault)
	}

	result := ""
	if task.JSON200.Result != nil {
		result = *task.JSON200.Result
	}

	o.printf("task %s: %s\n", task.JSON200.Status, result)

	return true, nil
}

func (o *owner) printEvent(ctx context.Context, e openapi.Event) error {
	payload, err := json.Marshal(e.Payload)
	if err != nil {
		return fmt.Errorf("encoding event %d: %w", e.Seq, err)
	}

	o.printf("%s %s %s\n", e.CreatedAt.Local().Format("15:04:05"), e.Kind, payload)

	if e.Kind != "question.opened" || e.QuestionId == nil {
		return nil
	}

	q, err := o.api.GetQuestionWithResponse(ctx, *e.QuestionId)
	if err != nil {
		return fmt.Errorf("reading question %s: %w", *e.QuestionId, err)
	}

	if q.JSON200 == nil {
		return apiError("reading question "+*e.QuestionId, q.StatusCode(), q.JSONDefault)
	}

	if q.JSON200.AddressedTo == openapi.AddresseeOwner {
		o.printQuestion(*q.JSON200)
	}

	return nil
}

func (o *owner) printQuestion(q openapi.Question) {
	o.printf("  question %s (%s): %s\n", q.Id, q.Mode, q.Text)

	for i, opt := range q.Options {
		o.printf("    %d. %s\n", i+1, opt.Label)
	}

	o.printf("  answer with: hypomnema answer %s <number, option or text>\n", q.Id)
}

// answer answers a question as the token's holder.
func (o *owner) answer(ctx context.Context, args []string) error {
	if len(args) < 2 {
		return ErrUsage
	}

	q, err := o.api.GetQuestionWithResponse(ctx, args[0])
	if err != nil {
		return fmt.Errorf("reading the question: %w", err)
	}

	if q.JSON200 == nil {
		return apiError("reading the question", q.StatusCode(), q.JSONDefault)
	}

	answered, err := o.api.AnswerQuestionWithResponse(ctx, q.JSON200.Id,
		ParseAnswer(q.JSON200.Options, strings.Join(args[1:], " ")))
	if err != nil {
		return fmt.Errorf("answering: %w", err)
	}

	if answered.JSON200 == nil {
		return apiError("answering", answered.StatusCode(), answered.JSONDefault)
	}

	o.printf("question %s is %s\n", answered.JSON200.Id, answered.JSON200.Status)

	return nil
}

// ParseAnswer reads what the owner typed: an option's number or its label
// (any case) picks it; any other text is the answer's text.
func ParseAnswer(options []openapi.Option, text string) openapi.Answer {
	pick := func(i int) openapi.Answer { return openapi.Answer{Option: &i} }

	if n, err := strconv.Atoi(text); err == nil {
		return pick(n - 1)
	}

	for i, opt := range options {
		if strings.EqualFold(strings.TrimSpace(text), opt.Label) {
			return pick(i)
		}
	}

	return openapi.Answer{Text: &text}
}

// questions lists the open questions addressed to the owner.
func (o *owner) questions(ctx context.Context) error {
	to, status := openapi.AddresseeOwner, openapi.QuestionStatusOpen

	resp, err := o.api.ListQuestionsWithResponse(ctx, &openapi.ListQuestionsParams{AddressedTo: &to, Status: &status})
	if err != nil {
		return fmt.Errorf("listing questions: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("listing questions", resp.StatusCode(), resp.JSONDefault)
	}

	for _, q := range resp.JSON200.Data {
		o.printQuestion(q)
	}

	return nil
}
