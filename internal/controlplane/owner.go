package controlplane

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

// The owner's commands until the web chat (#27) exists. They open the same
// database as `hypomnema serve`; SQLite serializes their writes with the
// server's, and the server's scheduler sees them on its next pass.

// ErrUsage marks a command line that is wrong.
var ErrUsage = errors.New("usage")

// followEvery is how often `run` reads new events.
const followEvery = time.Second

// Command runs one owner command: nodes, accounts, run, answer or questions.
func Command(ctx context.Context, cfg Config, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return ErrUsage
	}

	db, err := sqlite.Open(ctx, cfg.Database)
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck // Closing at exit; nothing is left to report it to.

	if err := orchestration.CheckSchema(ctx, db); err != nil {
		return err
	}

	o := &owner{
		svc: orchestration.New(db, platform.SystemClock{}, platform.NewID, orchestration.WithRoles(cfg.Roles)),
		out: stdout, every: followEvery,
	}

	switch args[0] {
	case "nodes":
		return o.nodes(ctx, args[1:])
	case "accounts":
		return o.accounts(ctx, args[1:])
	case "run":
		return o.run(ctx, args[1:])
	case "answer":
		return o.answer(ctx, args[1:])
	case "questions":
		return o.questions(ctx)
	default:
		return ErrUsage
	}
}

type owner struct {
	svc   *orchestration.Service
	out   io.Writer
	every time.Duration
}

func (o *owner) printf(format string, a ...any) {
	_, _ = fmt.Fprintf(o.out, format, a...)
}

func (o *owner) nodes(ctx context.Context, args []string) error {
	switch {
	case len(args) >= 2 && args[0] == "add":
		fs := flag.NewFlagSet("nodes add", flag.ContinueOnError)
		limit := fs.Int("max", 0, "Dispatches the node runs at once (0: the default)")

		if err := fs.Parse(args[2:]); err != nil {
			return fmt.Errorf("%w: %w", ErrUsage, err)
		}

		n, token, err := o.svc.AddNode(ctx, args[1], *limit)
		if err != nil {
			return err
		}

		o.printf("node %s (%s), up to %d Dispatches\n", n.Name, n.ID, n.MaxDispatches)
		o.printf("token, shown only now; put it in the node's configuration and nowhere else:\n%s\n", token)

		return nil
	case len(args) == 1 && args[0] == "list":
		nodes, err := o.svc.ListNodes(ctx)
		if err != nil {
			return err
		}

		for _, n := range nodes {
			state := "offline"
			if n.Connected {
				state = "connected"
			}

			o.printf("%s\t%s\t%s\t%s\t%d/%d\t%s\n", n.ID, n.Name, n.Status, state, n.Active, n.MaxDispatches,
				runtimes(n.Runtimes))
		}

		return nil
	default:
		return ErrUsage
	}
}

func runtimes(rs []orchestration.RuntimeInfo) string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.Runtime+" "+r.Version)
	}

	return strings.Join(out, ", ")
}

func (o *owner) accounts(ctx context.Context, args []string) error {
	switch {
	case len(args) == 3 && args[0] == "add":
		a, err := o.svc.AddAccount(ctx, args[1], args[2])
		if err != nil {
			return err
		}

		o.printf("account %s (%s %s)\n", a.ID, a.Runtime, a.Label)

		return nil
	case len(args) == 1 && args[0] == "list":
		accounts, err := o.svc.ListAccounts(ctx)
		if err != nil {
			return err
		}

		for _, a := range accounts {
			cooling := ""
			if a.CoolingUntil != nil {
				cooling = "cooling until " + a.CoolingUntil.Format(time.RFC3339)
			}

			o.printf("%s\t%s\t%s\t%s\t%s\n", a.ID, a.Runtime, a.Label, a.Status, cooling)
		}

		return nil
	default:
		return ErrUsage
	}
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

	run, err := o.svc.CreateRun(ctx, orchestration.NewRun{
		Objective: objective, CreatedBy: "owner", Repo: *repo, Base: *base,
	})
	if err != nil {
		return err
	}

	tasks, err := o.svc.CreateTasks(ctx, run.ID, []orchestration.NewTask{
		{Ref: "main", Role: *role, Spec: objective, Runtime: *runtime, Model: *model},
	})
	if err != nil {
		return err
	}

	o.printf("run %s, task %s\n", run.ID, tasks[0].ID)

	if *detach {
		return nil
	}

	return o.follow(ctx, run.ID, tasks[0].ID)
}

// follow prints the Run's events until its Task ends, then finishes the Run.
func (o *owner) follow(ctx context.Context, runID, taskID string) error {
	var after int64

	tick := time.NewTicker(o.every)
	defer tick.Stop()

	for {
		page, err := o.svc.ListEvents(ctx, after, orchestration.EventFilter{RunID: runID}, orchestration.EventPageMax)
		if err != nil {
			return err
		}

		for _, e := range page.Events {
			if err := o.printEvent(ctx, e); err != nil {
				return err
			}
		}

		after = page.NextAfter

		task, err := o.svc.GetTask(ctx, taskID)
		if err != nil {
			return err
		}

		if task.Status.Terminal() && !page.HasMore {
			if err := o.svc.FinishRun(ctx, runID); err != nil && !errors.Is(err, orchestration.ErrConflict) {
				return err
			}

			o.printf("task %s: %s\n", task.Status, task.Result)

			return nil
		}

		select {
		case <-ctx.Done():
			o.printf("stopped following; run %s goes on\n", runID)

			return nil
		case <-tick.C:
		}
	}
}

func (o *owner) printEvent(ctx context.Context, e orchestration.Event) error {
	o.printf("%s %s %s\n", e.CreatedAt.Local().Format("15:04:05"), e.Kind, string(e.Payload))

	if e.Kind != orchestration.EventQuestionOpened {
		return nil
	}

	q, err := o.svc.GetQuestion(ctx, e.QuestionID)
	if err != nil {
		return err
	}

	if q.AddressedTo == orchestration.AddressOwner {
		o.printQuestion(q)
	}

	return nil
}

func (o *owner) printQuestion(q orchestration.Question) {
	o.printf("  question %s (%s): %s\n", q.ID, q.Mode, q.Text)

	for i, opt := range q.Options {
		o.printf("    %d. %s\n", i+1, opt.Label)
	}

	o.printf("  answer with: hypomnema answer %s <number or text>\n", q.ID)
}

// answer answers a question as the owner: a number picks that option, any
// other text is the answer's text.
func (o *owner) answer(ctx context.Context, args []string) error {
	if len(args) < 2 {
		return ErrUsage
	}

	text := strings.Join(args[1:], " ")
	a := orchestration.Answer{Text: text}

	if n, err := strconv.Atoi(text); err == nil {
		i := n - 1
		a = orchestration.Answer{Option: &i}
	}

	q, err := o.svc.AnswerQuestion(ctx, args[0], orchestration.AddressOwner, a)
	if err != nil {
		return err
	}

	o.printf("question %s is %s\n", q.ID, q.Status)

	return nil
}

// questions lists the open questions addressed to the owner.
func (o *owner) questions(ctx context.Context) error {
	qs, err := o.svc.ListQuestions(ctx, orchestration.QuestionFilter{
		Status: orchestration.QuestionOpen, AddressedTo: orchestration.AddressOwner,
	})
	if err != nil {
		return err
	}

	for _, q := range qs {
		o.printQuestion(q)
	}

	return nil
}
