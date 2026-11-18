package agentapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/routinestore"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// The routines tool (issues #211, #536) is a Bot's pi's door to its own
// Routines, one tool with an action as Hermes's cronjob_manage
// (tools/cronjob_tools.py, CRONJOB_SCHEMA) and OpenClaw's cron tool
// (src/agents/tools/cron-tool.ts) are: list, set and run. It does what the
// client API lets a pi's token do, through the same store and runner:
// set is PUT /bots/{id}/routines/{routine} (clientapi.PutRoutine) for its own
// Bot from its own chat, a change that can be undone; run is POST
// /routines/{id}/runs (clientapi.RunRoutine) for a Routine of its own Bot.
// Pausing, removing, undoing and taking a grant back stay the owner's, as
// OpenMausBot leaves deleting a trigger to the person's explicit request
// (server/drivers/agents-catalog.ts, delete_trigger). list shows its own
// Bot's, and one by name with its last runs, as OpenMausBot's trigger_info
// does with a trigger_id.
//
// A worker is never given it: Hermes keeps cronjob_manage from its
// sub-agents (tools/delegate_tool_toolsets.py, DELEGATE_BLOCKED_TOOLS: "no
// scheduling more work in the parent's name"). The owner's token, the
// coordinator token and a Routine's script have no MCP and keep
// `hypomnema routines`.

// Routines starts a Routine's run by hand: the control plane's runner.
type Routines interface {
	RunNow(ctx context.Context, id string) (routinestore.RoutineRun, error)
}

// WithRoutines gives a Bot's pi the routines tool's run. Without it run
// answers that this control plane runs no Routines.
func (s *Server) WithRoutines(r Routines) *Server {
	s.routines = r

	return s
}

// routineRunsShown is how many of a Routine's runs list shows with it.
const routineRunsShown = 5

// routineOutputShown bounds the end of a run's output that list shows.
const routineOutputShown = 2000

// makesRoutines says whether the agent is given the routines tool: a Bot's
// pi in its chat, and no other.
func makesRoutines(a agent) bool {
	return a.dispatchID == "" && a.chatID != "" && a.bot != ""
}

// routineTool is the routines tool as a Bot's pi is offered it.
func routineTool(a agent) (agenttool.BrowserTool, bool) {
	if !makesRoutines(a) {
		return agenttool.BrowserTool{}, false
	}

	raw, err := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{"type": "string", "enum": []string{
				agenttool.RoutinesList, agenttool.RoutinesSet, agenttool.RoutinesRun,
			}},
			"name": map[string]any{"type": "string", "description": "The Routine's name: lowercase letters, " +
				"digits and dashes, starting with a letter. Needed for set and run; for list, one Routine with " +
				"its last runs."},
			"every_minutes": map[string]any{"type": "integer", "minimum": routinestore.MinRoutineMinutes,
				"maximum": routinestore.MaxRoutineMinutes, "description": "set: run every this many minutes."},
			"cron": map[string]any{"type": "string",
				"description": "set: instead of every_minutes, a five-field cron expression, with timezone."},
			"timezone": map[string]any{"type": "string",
				"description": "set: the IANA time zone of cron, such as Asia/Ho_Chi_Minh."},
			"prompt": map[string]any{"type": "string", "description": "set: the work, for a Routine you do " +
				"with your model in a new chat each time; self-contained, since that chat does not see this one."},
			"script": map[string]any{"type": "string", "description": "set: a script file in the folder you " +
				"work in, such as routines/report.sh: alone, for work that needs no model; with prompt, a check " +
				"that runs first and wakes you only when it finds work."},
			"timeout_seconds": map[string]any{"type": "integer", "minimum": 1,
				"description": "set: seconds a script may run (default 600)."},
			"summary": map[string]any{"type": "string", "description": "set: what the work is, in one line."},
			"quiet": map[string]any{"type": "boolean",
				"description": "set: true when a run's result is not to be added to this chat."},
		},
		"required": []string{"action"},
	})
	if err != nil {
		return agenttool.BrowserTool{}, false
	}

	return agenttool.BrowserTool{
		Name: agenttool.ToolRoutines,
		Description: "Make, list and run your Routines: work done on a schedule, a script that runs with no " +
			"model or a prompt you work on with your model in a new chat each time. Use it when the owner asks " +
			"for something on a schedule; what is asked for once is done now, without a Routine. action list " +
			"shows yours with their ids, schedules, state and last run: look before you make one, and never make " +
			"one twice. action set makes the Routine named, on at once, or changes it; what you leave out stays. " +
			"Before you set one, say in one sentence when it runs and what it does, and have the owner's yes in " +
			"this chat. Give a schedule, every_minutes or cron with timezone, a summary, and what runs: script, a " +
			"file you wrote under routines/ in the folder you work in, with its #! line (it runs later in that " +
			"folder with your commands, no model and no tools, and prints what it did), or prompt, or both. A " +
			"script has commands, not tools: it reads a Superset chart with `hypomnema superset chart " +
			"<dashboard> <chart> [--filter \"Name=value\"]` and refreshes an artifact with `hypomnema artifact " +
			"show --id <id> --path <folder>`. A script is at most 100,000 bytes: keep longer code in a file of " +
			"your folder (routines/lib/report.py) run by a short script (exec python3 routines/lib/report.py), " +
			"never split across Routines, and tell the owner the Routine runs that file. With both, the script " +
			"runs first as a cheap check: when it prints nothing, or its last line is {\"wakeAgent\": false}, " +
			"the run ends there and costs no turn; otherwise you are woken with what it printed (stderr too) " +
			"before the prompt, so print what changed; a failing script fails the run without waking you. Keep " +
			"what it last saw, and what it is set to watch, in files of your folder (a config under files/), " +
			"which a Routine's run may change; never the script. Give what runs whole each time you change it: " +
			"prompt alone drops a check. What runs is the text as it was when you set it: after changing the " +
			"script, set the Routine again. action run starts a run now, in the background; its result is added " +
			"to this chat when it ends, and list with the name shows how it went. After you set one, run it once " +
			"and tell the owner what that run did; if it failed, say so and that he can undo the Routine on its " +
			"card. Pausing, removing and undoing a Routine are the owner's. A Routine's own run makes, changes " +
			"and starts no Routines.",
		InputSchema: raw,
	}, true
}

// routineCall runs the routines tool and answers with the tool's result as
// MCP has it. What the agent can fix (its arguments, a schedule, a name not
// its own, a run still going) is its result, marked as an error.
func (s *Server) routineCall(w http.ResponseWriter, r *http.Request, a agent, call agenttool.BrowserCall) {
	var in agenttool.RoutineCall
	if json.Unmarshal(orEmpty(call.Arguments), &in) != nil {
		answerTool(w, "routines takes {action, name, every_minutes, cron, timezone, prompt, script, "+
			"timeout_seconds, summary, quiet}", true)

		return
	}

	var (
		text string
		err  error
	)

	switch in.Action {
	case agenttool.RoutinesList:
		text, err = s.listRoutines(r.Context(), a, in.Name)
	case agenttool.RoutinesSet:
		text, err = s.setRoutine(r.Context(), a, in)
	case agenttool.RoutinesRun:
		text, err = s.runRoutine(r.Context(), a, in.Name)
	default:
		text, err = "", fmt.Errorf("%w: action is list, set or run", orchestration.ErrInvalid)
	}

	switch {
	case err == nil:
		answerTool(w, text, false)
	case errors.Is(err, orchestration.ErrInvalid) || errors.Is(err, orchestration.ErrNotFound) ||
		errors.Is(err, orchestration.ErrConflict) || errors.Is(err, orchestration.ErrForbidden) ||
		errors.Is(err, orchestration.ErrUnavailable):
		answerTool(w, err.Error(), true)
	default:
		s.fail(w, r, err)
	}
}

// ownRoutine is the agent's Bot's Routine of that name; ErrNotFound when it
// has none.
func (s *Server) ownRoutine(ctx context.Context, a agent, name string) (routinestore.Routine, error) {
	if name == "" {
		return routinestore.Routine{}, fmt.Errorf("%w: name the routine", orchestration.ErrInvalid)
	}

	all, err := s.store.Routines().ListRoutines(ctx, routinestore.RoutineFilter{Bot: a.bot})
	if err != nil {
		return routinestore.Routine{}, err
	}

	for _, r := range all {
		if r.Name == name {
			return r, nil
		}
	}

	return routinestore.Routine{}, fmt.Errorf("%w: you have no routine %s; action list shows yours",
		orchestration.ErrNotFound, name)
}

// botName is what a Routine's sentence calls its Bot.
func (s *Server) botName(ctx context.Context, a agent) string {
	if bot, err := s.store.GetBot(ctx, a.bot); err == nil && bot.Name != "" {
		return bot.Name
	}

	return a.bot
}

func at(t time.Time) string { return t.UTC().Format("2006-01-02 15:04 UTC") }

// routineLine is a Routine in one line: its name and id, its sentence, its
// state, its next time and its last run.
func routineLine(r routinestore.Routine, bot string, last *routinestore.RoutineRun) string {
	line := fmt.Sprintf("- %s (%s): %s %s", r.Name, r.ID, routinestore.RoutineSentence(r, bot), r.State)

	if r.PausedReason != "" {
		line += " (" + r.PausedReason + ")"
	}

	if r.NextRunAt != nil {
		line += "; next " + at(*r.NextRunAt)
	}

	if last == nil {
		return line + "; never ran."
	}

	return line + "; last run " + runWords(*last) + "."
}

func runWords(run routinestore.RoutineRun) string {
	words := fmt.Sprintf("%s %s %s", run.ID, run.State, at(run.StartedAt))
	if run.ExitCode != nil {
		words += fmt.Sprintf(", exit %d", *run.ExitCode)
	}

	if run.Error != "" {
		words += ": " + run.Error
	}

	return words
}

// listRoutines lists the agent's Bot's Routines, or the one named with its
// last runs and the end of its last output.
func (s *Server) listRoutines(ctx context.Context, a agent, name string) (string, error) {
	bot := s.botName(ctx, a)

	if name != "" {
		r, err := s.ownRoutine(ctx, a, name)
		if err != nil {
			return "", err
		}

		runs, err := s.store.Routines().ListRoutineRuns(ctx, r.ID, routineRunsShown)
		if err != nil {
			return "", err
		}

		var b strings.Builder

		var last *routinestore.RoutineRun
		if len(runs) > 0 {
			last = &runs[0]
		}

		b.WriteString(routineLine(r, bot, last))

		if r.Kind == routinestore.RoutinePrompt {
			b.WriteString("\n\nprompt:\n" + r.Prompt)
		}

		if r.Kind == routinestore.RoutinePrompt && r.Script != "" {
			b.WriteString("\n\nchecked first by the script " + r.Script + ":\n" + r.ScriptText)
		}

		if len(runs) > 0 {
			b.WriteString("\n\nruns, the newest first:")
		}

		for _, run := range runs {
			b.WriteString("\n- " + runWords(run))
		}

		if last != nil && last.Output != "" {
			out := last.Output
			if len(out) > routineOutputShown {
				out = "…" + out[len(out)-routineOutputShown:]
			}

			b.WriteString("\n\nthe end of the last run's output:\n" + out)
		}

		return b.String(), nil
	}

	all, err := s.store.Routines().ListRoutines(ctx, routinestore.RoutineFilter{Bot: a.bot})
	if err != nil {
		return "", err
	}

	if len(all) == 0 {
		return "You have no Routine.", nil
	}

	lines := make([]string, 0, len(all))

	for _, r := range all {
		runs, err := s.store.Routines().ListRoutineRuns(ctx, r.ID, 1)
		if err != nil {
			return "", err
		}

		var last *routinestore.RoutineRun
		if len(runs) > 0 {
			last = &runs[0]
		}

		lines = append(lines, routineLine(r, bot, last))
	}

	return strings.Join(lines, "\n"), nil
}

// routineSpec is what a Routine is made or changed with: what it has, with
// what the call names on top, as `hypomnema routines set` reads its flags
// (clientcli/routines.go, routineSettings). A Bot's is always made from its
// chat, as the client API binds a pi's token to its chat (chatOf).
func routineSpec(a agent, have *routinestore.Routine, in agenttool.RoutineCall) (routinestore.RoutineSpec, error) {
	spec := routinestore.RoutineSpec{Bot: a.bot, Name: in.Name, Announce: true}

	if have != nil {
		spec = routinestore.RoutineSpec{
			Bot: a.bot, Name: in.Name, Kind: have.Kind, Prompt: have.Prompt, Script: have.Script,
			ScriptText: have.ScriptText, EveryMinutes: have.EveryMinutes, Cron: have.Cron, Timezone: have.Timezone,
			Summary: have.Summary, Announce: have.Announce,
		}

		if have.Script != "" {
			spec.TimeoutSeconds = int(have.Timeout / time.Second)
		}
	}

	spec.ChatID = a.chatID

	if in.EveryMinutes != nil {
		spec.EveryMinutes, spec.Cron, spec.Timezone = *in.EveryMinutes, "", ""
	}

	if in.Cron != nil {
		spec.Cron, spec.EveryMinutes = *in.Cron, 0
	}

	if in.Timezone != nil {
		spec.Timezone = *in.Timezone
	}

	if in.Script != nil && in.ScriptText == nil {
		// A tools server of an earlier release sends the path without reading it.
		return spec, fmt.Errorf("%w: this tools server cannot send a script yet; wait for its update",
			orchestration.ErrUnavailable)
	}

	// What runs is given whole: a prompt, a script, or both, a prompt whose script checks first (issue #656).
	switch {
	case in.Prompt != nil && in.Script != nil:
		spec.Kind, spec.Prompt, spec.Script, spec.ScriptText =
			routinestore.RoutinePrompt, *in.Prompt, path.Base(strings.ReplaceAll(*in.Script, `\`, "/")), *in.ScriptText
	case in.Prompt != nil:
		spec.Kind, spec.Prompt, spec.Script, spec.ScriptText, spec.TimeoutSeconds =
			routinestore.RoutinePrompt, *in.Prompt, "", "", 0
	case in.Script != nil:
		spec.Kind, spec.Script, spec.ScriptText, spec.Prompt =
			routinestore.RoutineScript, path.Base(strings.ReplaceAll(*in.Script, `\`, "/")), *in.ScriptText, ""
	}

	if in.TimeoutSeconds != nil {
		spec.TimeoutSeconds = *in.TimeoutSeconds
	}

	if in.Summary != nil {
		spec.Summary = *in.Summary
	}

	if in.Quiet != nil {
		spec.Announce = !*in.Quiet
	}

	if spec.Kind == "" {
		return spec, fmt.Errorf("%w: say what the routine runs: a prompt, or a script file", orchestration.ErrInvalid)
	}

	return spec, nil
}

// setRoutine makes or changes a Routine of the agent's own Bot, from its
// chat: the client API's PutRoutine for a pi's token.
func (s *Server) setRoutine(ctx context.Context, a agent, in agenttool.RoutineCall) (string, error) {
	have, err := s.ownRoutine(ctx, a, in.Name)

	var had *routinestore.Routine

	switch {
	case err == nil:
		had = &have
	case errors.Is(err, orchestration.ErrNotFound):
	default:
		return "", err
	}

	spec, err := routineSpec(a, had, in)
	if err != nil {
		return "", err
	}

	r, err := s.store.Routines().PutRoutine(ctx, spec, routinestore.RoutineAuthor{Bot: true})
	if err != nil {
		return "", err
	}

	s.logger.InfoContext(ctx, "a bot set a routine", slog.String("event", "agentapi.routine_set"),
		slog.String("agent", a.name), slog.String("routine", r.ID), slog.Bool("made", had == nil))

	text := fmt.Sprintf("Routine %s (%s) is set: %s", r.Name, r.ID, routinestore.RoutineSentence(r, s.botName(ctx, a)))
	if r.State == routinestore.RoutineOn {
		text += " It is on."
	} else {
		text += " It is " + string(r.State) + "; the owner switches it on again."
	}

	return text + " Run it once with action run, then tell the owner what that run did.", nil
}

// runRoutine starts a run of a Routine of the agent's own Bot now: the
// client API's RunRoutine for a pi's token.
func (s *Server) runRoutine(ctx context.Context, a agent, name string) (string, error) {
	r, err := s.ownRoutine(ctx, a, name)
	if err != nil {
		return "", err
	}

	if s.routines == nil {
		return "", fmt.Errorf("%w: this control plane runs no routines", orchestration.ErrUnavailable)
	}

	run, err := s.routines.RunNow(ctx, r.ID)
	if err != nil {
		return "", err
	}

	s.logger.InfoContext(ctx, "a bot ran a routine", slog.String("event", "agentapi.routine_run"),
		slog.String("agent", a.name), slog.String("routine", r.ID), slog.String("run", run.ID))

	text := fmt.Sprintf("Run %s of routine %s started; it goes on in the background.", run.ID, r.Name)
	if r.Announce && r.ChatID != "" {
		text += " Its result is added to the chat the Routine was made in when it ends."
	}

	return text + fmt.Sprintf(" To see how it went, call routines with action list and name %s.", r.Name), nil
}
