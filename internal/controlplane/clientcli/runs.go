package clientcli

import (
	"context"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/shared/githost"
)

func createRun(ctx context.Context, e *env, nr openapi.NewRun, idempotencyKey *string) (openapi.Run, error) {
	resp, err := e.api.CreateRunWithResponse(ctx, &openapi.CreateRunParams{IdempotencyKey: idempotencyKey}, nr)
	if err != nil {
		return openapi.Run{}, fmt.Errorf("creating the run got no answer (%w); the Run may exist: "+
			"run the same command again with --key %s, which returns it instead of creating a second", err,
			val(idempotencyKey))
	}

	if resp.JSON201 == nil {
		return openapi.Run{}, apiError("creating the run", resp.StatusCode(), resp.JSONDefault)
	}

	return *resp.JSON201, nil
}

func runsCreate(ctx context.Context, e *env, fs *flag.FlagSet) error {
	objective, err := e.text(fs, "objective")
	if err != nil {
		return err
	}

	if str(fs, "objective-file") == "" {
		objective = strings.Join(fs.Args(), " ")
	}

	if strings.TrimSpace(objective) == "" {
		return fmt.Errorf("%w: hypomnema runs create --repo owner/name --objective-file <file>", ErrUsage)
	}

	run, err := createRun(ctx, e, openapi.NewRun{
		Objective: objective, Repo: optional(fs, "repo"), Base: optional(fs, "base"), ChatId: runChat(fs),
		Uses: uses(fs),
	}, key(fs))
	if err != nil {
		return err
	}

	return e.print(fs, run, func() { e.printf("run %s (%s)%s\n", run.Id, run.Status, runChatSuffix(run)) })
}

func runChat(fs *flag.FlagSet) *string {
	if named := optional(fs, "chat"); named != nil {
		return named
	}
	return chatOfThisAgent()
}

func runChatSuffix(run openapi.Run) string {
	if chat := val(run.ChatId); chat != "" {
		return " in chat " + chat
	}
	return ""
}

func runsAdopt(ctx context.Context, e *env, fs *flag.FlagSet) error {
	id := fs.Arg(0)
	resp, err := e.api.AdoptRunWithResponse(ctx, id, openapi.RunAdoption{ChatId: optional(fs, "chat")})
	if err != nil {
		return fmt.Errorf("adopting run %s: %w", id, err)
	}
	if resp.JSON200 == nil {
		return apiError("adopting run "+id, resp.StatusCode(), resp.JSONDefault)
	}
	return e.print(fs, resp.JSON200, func() { e.printf("run %s (%s)%s\n", id, resp.JSON200.Status, runChatSuffix(*resp.JSON200)) })
}

func runsList(ctx context.Context, e *env, fs *flag.FlagSet) error {
	limit := number(fs, "limit")
	params := &openapi.ListRunsParams{Limit: &limit, ChatId: optional(fs, "chat")}

	if s := str(fs, "status"); s != "" {
		status := openapi.RunStatus(s)
		params.Status = &status
	}

	resp, err := e.api.ListRunsWithResponse(ctx, params)
	if err != nil {
		return fmt.Errorf("listing runs: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("listing runs", resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() {
		for _, r := range resp.JSON200.Data {
			e.printf("%s\t%s\t%s\t%s\n", r.Id, r.Status, val(r.Repo), firstLine(r.Objective))
		}
	})
}

func getRun(ctx context.Context, e *env, id string) (openapi.RunDetail, error) {
	resp, err := e.api.GetRunWithResponse(ctx, id)
	if err != nil {
		return openapi.RunDetail{}, fmt.Errorf("reading run %s: %w", id, err)
	}

	if resp.JSON200 == nil {
		return openapi.RunDetail{}, apiError("reading run "+id, resp.StatusCode(), resp.JSONDefault)
	}

	return *resp.JSON200, nil
}

// RunOwnsReplacement checks the coordinator's Run before its launcher can close a superseded PR.
func RunOwnsReplacement(ctx context.Context, cfg Config, runID, prURL, branch, sha string) (bool, error) {
	api, err := newClient(cfg, requestTimeout)
	if err != nil {
		return false, err
	}
	run, err := getRun(ctx, &env{api: api}, runID)
	if err != nil {
		return false, err
	}
	return runOwnsReplacement(run, prURL, branch, sha), nil
}

func runOwnsReplacement(run openapi.RunDetail, prURL, branch, sha string) bool {
	pr, ok := githost.ParseRequest(prURL, githost.KnownKind)
	if !ok || pr.Repo.Host != githost.GitHubHost || branch == "" || sha == "" || run.PublishedHeads == nil ||
		val(run.RepoHost) != pr.Repo.Host || !strings.EqualFold(val(run.Repo), pr.Repo.Path) {
		return false
	}
	for _, head := range *run.PublishedHeads {
		if branch == head.Branch && sha == head.Sha {
			return true
		}
	}
	return false
}

func runsShow(ctx context.Context, e *env, fs *flag.FlagSet) error {
	run, err := getRun(ctx, e, fs.Arg(0))
	if err != nil {
		return err
	}

	return e.print(fs, run, func() {
		e.printf("run %s (%s) %s\n%s\n", run.Id, run.Status, val(run.Repo), run.Objective)

		if run.Usage != nil {
			e.printf("used: %s\n", usageLine(*run.Usage))
		}

		for _, t := range run.Tasks {
			e.printf("  %s\t%s\t%s\n", t.Id, taskState(t.Status, t.BlockedReason), firstLine(t.Spec))

			if w := val(t.WaitingReason); w != "" {
				e.printf("    waiting: %s\n", waitingLine(w))
			}

			if t.Usage != nil && t.Usage.Dispatches > 0 {
				e.printf("    used: %s\n", usageLine(*t.Usage))
			}
		}
	})
}

func runsFinish(ctx context.Context, e *env, fs *flag.FlagSet) error {
	resp, err := e.api.FinishRunWithResponse(ctx, fs.Arg(0))
	if err != nil {
		return fmt.Errorf("finishing the run: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("finishing the run", resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() { e.printf("run %s is %s\n", resp.JSON200.Id, resp.JSON200.Status) })
}

func runsCancel(ctx context.Context, e *env, fs *flag.FlagSet) error {
	resp, err := e.api.CancelRunWithResponse(ctx, fs.Arg(0))
	if err != nil {
		return fmt.Errorf("canceling the run: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("canceling the run", resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() { e.printf("run %s is %s\n", resp.JSON200.Id, resp.JSON200.Status) })
}

// Waited is what `runs wait` prints: the events that need the coordinator,
// the cursor for the next wait, and the next steps.
type Waited struct {
	RunID string          `json:"run_id"`
	After int64           `json:"after"`
	Done  bool            `json:"run_done"`
	Wake  []openapi.Event `json:"events"`
	Next  []string        `json:"next"`
	// TimedOut: --timeout passed before anything needed the coordinator.
	TimedOut bool `json:"timed_out,omitempty"`
}

// runsWait waits until something in the Run needs its coordinator: a Task
// ended or was blocked, a question reached the coordinator, an escalation
// came, or the Run ended. It reads events with the API's long wait, so it
// sends one request per 50 s while nothing happens.
func runsWait(ctx context.Context, e *env, fs *flag.FlagSet) error {
	runID := fs.Arg(0)
	out := Waited{RunID: runID, After: int64(number(fs, "after"))}

	var deadline time.Time
	if d, _ := time.ParseDuration(str(fs, "timeout")); d > 0 {
		deadline = time.Now().Add(d)
	}

	hasMore := false

	for {
		// Wait only when the log holds nothing more; otherwise read the rest at once.
		wait := waitSeconds
		if len(out.Wake) > 0 || hasMore {
			wait = 0
		} else if !deadline.IsZero() {
			left := time.Until(deadline)
			if left <= 0 {
				out.TimedOut = true

				break
			}

			wait = min(wait, max(1, int(left.Seconds())))
		}

		page, err := e.api.ListEventsWithResponse(ctx, &openapi.ListEventsParams{
			After: &out.After, RunId: &runID, Wait: &wait,
		})
		if err != nil {
			return fmt.Errorf("waiting for events: %w", err)
		}

		if page.JSON200 == nil {
			return apiError("waiting for events", page.StatusCode(), page.JSONDefault)
		}

		for _, ev := range page.JSON200.Data {
			if needsCoordinator(ev) {
				out.Wake = append(out.Wake, ev)
			}
		}

		out.After, hasMore = page.JSON200.NextAfter, page.JSON200.HasMore

		if len(out.Wake) > 0 && !hasMore {
			break
		}
	}

	if err := nextSteps(ctx, e, &out); err != nil {
		return err
	}

	return e.print(fs, out, func() {
		for _, ev := range out.Wake {
			e.printf("%s %s task=%s question=%s %v\n", ev.CreatedAt.Local().Format("15:04:05"), ev.Kind,
				val(ev.TaskId), val(ev.QuestionId), ev.Payload)
		}

		for _, n := range out.Next {
			e.printf("next: %s\n", n)
		}
	})
}

// needsCoordinator reports whether an event is one the coordinator acts on.
func needsCoordinator(ev openapi.Event) bool {
	switch ev.Kind {
	case "task.status":
		to, _ := ev.Payload["to"].(string)

		return to == "succeeded" || to == "failed" || to == "blocked" || to == "canceled"
	case "question.opened":
		to, _ := ev.Payload["addressed_to"].(string)

		return to == "coordinator"
	case "escalation", "run.finished", "run.canceled":
		return true
	default:
		return false
	}
}

// nextSteps names the commands that follow from what woke the coordinator,
// as Orca's receipts carry `next`.
func nextSteps(ctx context.Context, e *env, out *Waited) error {
	seen := map[string]bool{}
	add := func(s string) {
		if !seen[s] {
			seen[s] = true

			out.Next = append(out.Next, s)
		}
	}

	for _, ev := range out.Wake {
		switch {
		case ev.Kind == "question.opened" && ev.QuestionId != nil:
			add("hypomnema questions show " + *ev.QuestionId + " --json")
		case ev.Kind == "task.status" || ev.Kind == "escalation":
			if ev.TaskId != nil {
				add("hypomnema tasks show " + *ev.TaskId + " --json")
			}
		}
	}

	run, err := getRun(ctx, e, out.RunID)
	if err != nil {
		return err
	}

	if run.Status != openapi.RunStatusOpen {
		out.Done = true

		return nil
	}

	if run.ReviewStall != nil {
		add("hypomnema skills get hypomnema-coordinator --reference review")

		return nil
	}

	settled := true
	for _, t := range run.Tasks {
		settled = settled && ended(t.Status)
	}

	if settled && len(run.Tasks) > 0 {
		add("hypomnema runs finish " + run.Id + " --json")

		return nil
	}

	add(fmt.Sprintf("hypomnema runs wait %s --after %d --json", run.Id, out.After))

	return nil
}

func taskState(s openapi.TaskStatus, reason *openapi.BlockedReason) string {
	if reason != nil {
		return string(s) + ":" + string(*reason)
	}

	return string(s)
}

// waitingLine says why a ready Task waits (issue #660), from its reason code (dispatch_types.go), as
// OpenMausBot's queued reason says it in one sentence (server/host/queued-reason.ts).
func waitingLine(code string) string {
	kind, arg, _ := strings.Cut(code, ":")

	switch kind {
	case "nodes_full":
		return "every node that can run it is full now"
	case "no_node_fits":
		return "it asks for more CPU or memory than any node that can run it has to give"
	case "nodes_low_memory":
		return "every node that can run it is low on memory"
	case "nodes_low_disk":
		return "no suitable node has enough measured free disk space"
	case "accounts_cooling":
		return "every account of its agent waits for its limit to reset"
	case "no_node":
		return "no connected node can run its agent"
	case "no_account":
		return "no account can run its agent"
	case "needs_gateway":
		return "its agent runs only through the model gateway, and its profile names none"
	case "no_node_reaches":
		return "no node reaches " + arg
	case "no_node_has_tool":
		return "no node has " + arg + " installed"
	}

	return code
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	if len(line) > 80 {
		return line[:77] + "..."
	}

	return line
}
