package clientcli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
)

// ownerRun is the owner's shortcut: a Run with one Task, followed until the
// Task ends.
func ownerRun(ctx context.Context, e *env, fs *flag.FlagSet) error {
	objective := strings.TrimSpace(strings.Join(fs.Args(), " "))
	repo := str(fs, "repo")

	if objective == "" || repo == "" {
		return fmt.Errorf("%w: hypomnema run --repo owner/name <objective>", ErrUsage)
	}

	run, err := createRun(ctx, e, openapi.NewRun{Objective: objective, Repo: &repo, Base: optional(fs, "base")}, key(fs))
	if err != nil {
		return err
	}

	task := openapi.NewTask{
		Ref: "main", Profile: optional(fs, "profile"), Spec: objective, Runtime: optional(fs, "runtime"),
		Model: optional(fs, "model"),
	}

	tasks, err := createTasks(ctx, e, run.Id, []openapi.NewTask{task}, key(fs))
	if err != nil {
		return err
	}

	taskID := tasks[0].Id
	e.printf("run %s, task %s\n", run.Id, taskID)

	if boolean(fs, "detach") {
		return nil
	}

	return follow(ctx, e, run.Id, taskID)
}

// follow prints the Run's events until its Task ends, then finishes the Run.
// Each request waits for the next event, so nothing polls.
func follow(ctx context.Context, e *env, runID, taskID string) error {
	var after int64

	wait := waitSeconds

	for {
		page, err := e.api.ListEventsWithResponse(ctx, &openapi.ListEventsParams{
			After: &after, RunId: &runID, Wait: &wait,
		})

		if ctx.Err() != nil {
			e.printf("stopped following; run %s goes on\n", runID)

			return nil //nolint:nilerr // Stopping (Ctrl-C) leaves the Run going; it is not a failure.
		}

		if err != nil {
			return fmt.Errorf("reading events: %w", err)
		}

		if page.JSON200 == nil {
			return apiError("reading events", page.StatusCode(), page.JSONDefault)
		}

		for _, ev := range page.JSON200.Data {
			if err := printEvent(ctx, e, ev); err != nil {
				return err
			}
		}

		after = page.JSON200.NextAfter

		if page.JSON200.HasMore {
			continue
		}

		done, err := taskEnded(ctx, e, runID, taskID)
		if done || err != nil {
			return err
		}
	}
}

// taskEnded reports whether the Task has ended; if it has, it finishes the
// Run and prints the result.
func taskEnded(ctx context.Context, e *env, runID, taskID string) (bool, error) {
	task, err := getTask(ctx, e, taskID)
	if err != nil {
		return false, err
	}

	if !ended(task.Status) {
		return false, nil
	}

	finished, err := e.api.FinishRunWithResponse(ctx, runID)
	if err != nil {
		return true, fmt.Errorf("finishing the run: %w", err)
	}

	if finished.JSON200 == nil && (finished.JSONDefault == nil || finished.JSONDefault.Code != openapi.Conflict) {
		return true, apiError("finishing the run", finished.StatusCode(), finished.JSONDefault)
	}

	e.printf("task %s: %s\n", task.Status, val(task.Result))

	return true, nil
}

func ended(s openapi.TaskStatus) bool {
	return s == openapi.TaskStatusSucceeded || s == openapi.TaskStatusFailed || s == openapi.TaskStatusCanceled
}

func printEvent(ctx context.Context, e *env, ev openapi.Event) error {
	payload, err := json.Marshal(ev.Payload)
	if err != nil {
		return fmt.Errorf("encoding event %d: %w", ev.Seq, err)
	}

	e.printf("%s %s %s\n", ev.CreatedAt.Local().Format("15:04:05"), ev.Kind, payload)

	if ev.Kind != "question.opened" || ev.QuestionId == nil {
		return nil
	}

	q, err := getQuestion(ctx, e, *ev.QuestionId)
	if err != nil {
		return err
	}

	if q.AddressedTo == openapi.AddresseeOwner {
		printQuestion(e, q)
	}

	return nil
}

func val[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}

	return *p
}
