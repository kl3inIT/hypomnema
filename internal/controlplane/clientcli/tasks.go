package clientcli

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

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
			Ref: str(fs, "ref"), Profile: optional(fs, "profile"), Spec: spec, Runtime: optional(fs, "runtime"),
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

// tasksList prints the board: GET /tasks.
func tasksList(ctx context.Context, e *env, fs *flag.FlagSet) error {
	params := &openapi.ListTasksParams{}

	if d, _ := time.ParseDuration(str(fs, "since")); d > 0 {
		since := time.Now().Add(-d)
		params.Since = &since
	}

	if n := number(fs, "limit"); n > 0 {
		params.Limit = &n
	}

	resp, err := e.api.ListTasksWithResponse(ctx, params)
	if err != nil {
		return fmt.Errorf("listing tasks: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("listing tasks", resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() {
		for _, t := range resp.JSON200.Data {
			e.printf("%s\t%s\t%s\t%s\n", t.Id, taskState(t.Status, t.BlockedReason), t.RunId, plain(taskLine(t)))
		}
	})
}

// taskLine is what a card says of a Task: what its worker does now, else its result, else its spec.
func taskLine(t openapi.TaskDetail) string {
	if d := t.LatestDispatch; d != nil {
		if t.Status == openapi.TaskStatusRunning && val(d.Activity) != "" {
			return firstLine(val(d.Activity))
		}

		if d.Result != nil {
			line := firstLine(d.Result.Summary)
			for _, ev := range d.Result.Evidence {
				line += "  " + ev.Ref
			}

			return line
		}
	}

	return firstLine(t.Spec)
}

// followWait is how long one request waits for the next item of a transcript,
// in seconds. It is short because the wait does not end when the worker does:
// the end is seen at the next round.
const followWait = 5

// tasksTranscript prints the transcript of a Task's latest Dispatch, a line an
// item, and with --follow keeps going until the worker has ended and nothing
// more comes. With --json it prints one item a line.
func tasksTranscript(ctx context.Context, e *env, fs *flag.FlagSet) error {
	t, err := getTask(ctx, e, fs.Arg(0))
	if err != nil {
		return err
	}

	if t.LatestDispatch == nil {
		return fmt.Errorf("task %s has not started: it has no transcript yet", t.Id)
	}

	id := t.LatestDispatch.Id
	after, _ := strconv.ParseInt(str(fs, "after"), 10, 64)
	follow := boolean(fs, "follow")

	for {
		params := &openapi.ListDispatchItemsParams{After: &after}

		ended := !follow
		if follow {
			// Read the status before the items: an empty page after a settled Dispatch is then the end.
			now, err := getTask(ctx, e, t.Id)
			if err != nil {
				return err
			}

			ended = now.LatestDispatch == nil || now.LatestDispatch.Id != id || now.LatestDispatch.SettledAt != nil
			if !ended {
				wait := followWait
				params.Wait = &wait
			}
		}

		resp, err := e.api.ListDispatchItemsWithResponse(ctx, id, params)
		if err != nil {
			return fmt.Errorf("reading the transcript: %w", err)
		}

		if resp.JSON200 == nil {
			return apiError("reading the transcript", resp.StatusCode(), resp.JSONDefault)
		}

		for _, it := range resp.JSON200.Data {
			after = it.Seq

			if err := e.print(fs, it, func() { e.printf("%s\n", plain(itemLine(it))) }); err != nil {
				return err
			}
		}

		if resp.JSON200.HasMore {
			continue
		}

		if ended && len(resp.JSON200.Data) == 0 || !follow {
			return nil
		}
	}
}

// plain makes text from a worker safe to print on a terminal: a control
// character other than a newline or a tab is shown as its escape (`\x1b`), so
// a worker's output cannot move the cursor, recolour or rewrite what the
// owner reads. JSON output needs none of this: it escapes them itself.
func plain(s string) string {
	var b strings.Builder

	for _, r := range s {
		if r != '\n' && r != '\t' && unicode.IsControl(r) {
			b.WriteString(strings.Trim(strconv.QuoteRuneToASCII(r), "'"))

			continue
		}

		b.WriteRune(r)
	}

	return b.String()
}

// itemHints are the inputs that say what a tool call is about, in the order tried.
var itemHints = []string{"command", "file_path", "path", "pattern", "url", "query", "description", "summary", "text"}

// itemLine is one item of a transcript in one line: what the worker said, the
// tool it called and on what, or the start of what the tool answered.
func itemLine(it openapi.TranscriptItem) string {
	switch it.Kind {
	case openapi.TranscriptItemKindToolCall:
		line := "> " + val(it.Tool)

		for _, key := range itemHints {
			if hint, ok := val(it.Input)[key].(string); ok && strings.TrimSpace(hint) != "" {
				return line + ": " + firstLine(hint)
			}
		}

		return line
	case openapi.TranscriptItemKindToolResult:
		line := "  = " + firstLine(val(it.Text))
		if val(it.Failed) {
			line += " [failed]"
		}

		return line
	case openapi.TranscriptItemKindReasoning:
		return "(thinking) " + firstLine(val(it.Text))
	case openapi.TranscriptItemKindNotice:
		return "! " + val(it.Text)
	case openapi.TranscriptItemKindMessage:
		return val(it.Text)
	default:
		return val(it.Text)
	}
}
