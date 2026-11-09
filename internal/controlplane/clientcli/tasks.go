package clientcli

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
)

func createTasks(ctx context.Context, e *env, runID string, tasks []openapi.NewTask, idempotencyKey *string,
) ([]openapi.Task, error) {
	resp, err := e.api.CreateTasksWithResponse(ctx, runID, &openapi.CreateTasksParams{IdempotencyKey: idempotencyKey},
		openapi.NewTasks{Tasks: tasks})
	if err != nil {
		return nil, fmt.Errorf("creating tasks got no answer (%w); they may exist: "+
			"run the same command again with --key %s, which returns them instead of creating more", err,
			val(idempotencyKey))
	}

	if resp.JSON201 == nil {
		return nil, apiError("creating tasks", resp.StatusCode(), resp.JSONDefault)
	}

	return resp.JSON201.Data, nil
}

// tasksCreate adds Tasks from a JSON file ({"tasks": [...]}), or one Task
// from flags. Several Tasks with dependencies go in one call.
func tasksCreate(ctx context.Context, e *env, fs *flag.FlagSet) error {
	var tasks []openapi.NewTask

	if str(fs, "file") != "" {
		raw, err := e.read(str(fs, "file"))
		if err != nil {
			return err
		}

		var body openapi.NewTasks

		dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
		dec.DisallowUnknownFields()

		if err := dec.Decode(&body); err != nil {
			return fmt.Errorf("%w: the file is not {\"tasks\": [...]}: %w", ErrUsage, err)
		}

		tasks = body.Tasks
	} else {
		spec, err := e.text(fs, "spec")
		if err != nil {
			return err
		}

		if strings.TrimSpace(spec) == "" {
			return fmt.Errorf("%w: hypomnema tasks create <run_id> --file <tasks.json> or --spec-file <file>", ErrUsage)
		}

		t := openapi.NewTask{
			Ref: str(fs, "ref"), Role: str(fs, "role"), Spec: spec, Runtime: optional(fs, "runtime"),
			Model: optional(fs, "model"), Effort: optional(fs, "effort"),
		}

		if deps := str(fs, "depends-on"); deps != "" {
			list := strings.Split(deps, ",")
			for i := range list {
				list[i] = strings.TrimSpace(list[i])
			}

			t.DependsOn = &list
		}

		tasks = []openapi.NewTask{t}
	}

	created, err := createTasks(ctx, e, fs.Arg(0), tasks, key(fs))
	if err != nil {
		return err
	}

	return e.print(fs, openapi.TaskList{Data: created}, func() {
		for _, t := range created {
			e.printf("%s\t%s\t%s\n", t.Id, t.Status, firstLine(t.Spec))
		}
	})
}

func getTask(ctx context.Context, e *env, id string) (openapi.TaskDetail, error) {
	resp, err := e.api.GetTaskWithResponse(ctx, id)
	if err != nil {
		return openapi.TaskDetail{}, fmt.Errorf("reading task %s: %w", id, err)
	}

	if resp.JSON200 == nil {
		return openapi.TaskDetail{}, apiError("reading task "+id, resp.StatusCode(), resp.JSONDefault)
	}

	return *resp.JSON200, nil
}

func tasksShow(ctx context.Context, e *env, fs *flag.FlagSet) error {
	t, err := getTask(ctx, e, fs.Arg(0))
	if err != nil {
		return err
	}

	return e.print(fs, t, func() {
		e.printf("task %s (%s), run %s, %d failed attempts\n%s\n", t.Id, taskState(t.Status, t.BlockedReason), t.RunId,
			t.FailureCount, t.Spec)

		if d := t.LatestDispatch; d != nil {
			e.printf("latest dispatch %s on %s: %s\n", d.Id, d.NodeId, d.Status)

			if d.Result != nil {
				e.printf("%s\n", d.Result.Summary)

				for _, ev := range d.Result.Evidence {
					e.printf("  %s %s\n", ev.Kind, ev.Ref)
				}
			}
		}
	})
}

func tasksCancel(ctx context.Context, e *env, fs *flag.FlagSet) error {
	resp, err := e.api.CancelTaskWithResponse(ctx, fs.Arg(0))
	if err != nil {
		return fmt.Errorf("canceling the task: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("canceling the task", resp.StatusCode(), resp.JSONDefault)
	}

	t := resp.JSON200

	return e.print(fs, t, func() {
		if t.CancelRequestedAt != nil && t.Status != openapi.TaskStatusCanceled {
			e.printf("task %s: the worker is being stopped; it is canceled once the stop is confirmed\n", t.Id)

			return
		}

		e.printf("task %s is %s\n", t.Id, t.Status)
	})
}

func tasksReopen(ctx context.Context, e *env, fs *flag.FlagSet) error {
	resp, err := e.api.ReopenTaskWithResponse(ctx, fs.Arg(0))
	if err != nil {
		return fmt.Errorf("reopening the task: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("reopening the task", resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() { e.printf("task %s is %s\n", resp.JSON200.Id, resp.JSON200.Status) })
}
