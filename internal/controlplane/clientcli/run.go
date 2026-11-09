package clientcli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
)

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
