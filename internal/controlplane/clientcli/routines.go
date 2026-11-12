package clientcli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
)

// Routines from the terminal (issue #211): the owner makes, changes, pauses,
// runs and removes them, as `hermes cron` does for Hermes's jobs; a Bot
// makes one with the same command from its chat and runs it once; the
// launcher of a script Routine gets the script here before it runs it.

// routineEnv names the Routine whose script runs, set by its launcher: a
// Routine's run makes and changes no Routines.
const routineEnv = "HYPOMNEMA_ROUTINE"

// runKeyFile is the file, beside a script, that holds the key of the run
// the script runs in, for the Routine named.
func runKeyFile(dir, routineID string) string { return filepath.Join(dir, ".run-"+routineID) }

func routineFlags(fs *flag.FlagSet) {
	fs.String("bot", "", "the bot the routine belongs to (default: the bot this is, or coordinator)")
	fs.Int("every", 0, "run every this many minutes, 5 to 1440")
	fs.String("cron", "", "run by this 5-field cron expression; needs --tz")
	fs.String("tz", "", "the IANA time zone of the cron expression, such as Asia/Ho_Chi_Minh")
	fs.String("prompt-file", "", "a file with the prompt the bot works on at each run, or - for standard input")
	fs.String("script-file", "", "a file with the script to run at each run with no model, or - for standard input")
	fs.String("script", "", "the script's file name under the bot's routines directory (default: --script-file's name)")
	fs.Int("timeout", 0, "seconds a script may run (default 600)")
	fs.String("summary", "", "what the work is, in one line")
	fs.String("chat", "", "the chat the routine is made in: where its results and a failure are told")
	fs.Bool("quiet", false, "a run's result is not told in that chat")
	jsonFlag(fs)
}

// routineBot is the Bot a command is for: the one named, the one this
// process is, or the first.
func routineBot(fs *flag.FlagSet) string {
	if bot := str(fs, "bot"); bot != "" {
		return bot
	}

	if bot := os.Getenv(botEnv); bot != "" {
		return bot
	}

	return "coordinator"
}

// routineSettings reads what the flags say on top of what a Routine has: a
// flag left out keeps it, as `bots set` does. The chat is the one named, or
// the one pi works in.
func routineSettings(e *env, fs *flag.FlagSet, have *openapi.Routine) (openapi.RoutineSettings, error) {
	var set openapi.RoutineSettings

	if have != nil {
		set = openapi.RoutineSettings{
			Kind: have.Kind, Prompt: have.Prompt, Script: have.Script, ScriptText: have.ScriptText,
			EveryMinutes: have.EveryMinutes, Cron: have.Cron, Timezone: have.Timezone, Summary: have.Summary,
			ChatId: have.ChatId, Announce: &have.Announce,
		}

		if have.Kind == openapi.Script {
			set.TimeoutSeconds = &have.TimeoutSeconds
		}
	}

	if chat := os.Getenv(chatEnv); chat != "" {
		set.ChatId = &chat
	}

	if routine := os.Getenv(routineEnv); routine != "" {
		set.FromRoutine = &routine
	}

	var failed error

	fs.Visit(func(f *flag.Flag) {
		v := f.Value.String()

		switch f.Name {
		case "every":
			n := number(fs, "every")
			set.EveryMinutes, set.Cron, set.Timezone = &n, nil, nil
		case "cron":
			set.Cron, set.EveryMinutes = &v, nil
		case "tz":
			set.Timezone = &v
		case "prompt-file":
			text, err := e.read(v)
			if err != nil {
				failed = err
			}

			set.Kind, set.Prompt, set.Script, set.ScriptText, set.TimeoutSeconds = openapi.Prompt, &text, nil, nil, nil
		case "script-file":
			text, err := e.read(v)
			if err != nil {
				failed = err
			}

			set.Kind, set.ScriptText, set.Prompt = openapi.Script, &text, nil

			if set.Script == nil && v != "-" {
				name := filepath.Base(v)
				set.Script = &name
			}
		case "script":
			set.Script = &v
		case "timeout":
			n := number(fs, "timeout")
			set.TimeoutSeconds = &n
		case "summary":
			set.Summary = &v
		case "chat":
			set.ChatId = &v
		case "quiet":
			announce := !boolean(fs, "quiet")
			set.Announce = &announce
		}
	})

	if failed != nil {
		return set, failed
	}

	if set.Kind == "" {
		return set, fmt.Errorf("%w: say what the routine runs with --prompt-file or --script-file", ErrUsage)
	}

	return set, nil
}

func when(t *time.Time) string {
	if t == nil {
		return "-"
	}

	return t.Local().Format("2006-01-02 15:04")
}

func printRoutineLine(e *env, r openapi.Routine) {
	last := "never ran"
	if r.LastRun != nil {
		last = fmt.Sprintf("last %s %s", r.LastRun.State, when(&r.LastRun.StartedAt))
	}

	e.printf("%s\t%s/%s\t%s\t%s\t%s\tnext %s\t%s\n", r.Id, r.Bot, r.Name, r.Kind, r.Schedule, r.State,
		when(r.NextRunAt), last)
}

func printRun(e *env, r openapi.RoutineRun) {
	line := fmt.Sprintf("%s\t%s\t%s\t%s", r.Id, r.State, r.StartedBy, when(&r.StartedAt))
	if r.ExitCode != nil {
		line += fmt.Sprintf("\texit %d", *r.ExitCode)
	}

	if chat := val(r.ChatId); chat != "" {
		line += "\tchat " + chat
	}

	e.printf("%s\n", line)

	if r.Error != "" {
		e.printf("  %s\n", r.Error)
	}
}

func printRoutine(e *env, r *openapi.Routine) {
	printRoutineLine(e, *r)
	e.printf("%s\n", r.Sentence)

	if reason := val(r.PausedReason); reason != "" {
		e.printf("%s\n", reason)
	}

	if r.Failures > 0 {
		e.printf("%d runs failed in a row\n", r.Failures)
	}

	if r.Skipped > 0 {
		e.printf("%d times were skipped while a run was still going; the last at %s\n", r.Skipped, when(r.LastSkippedAt))
	}

	if chat := val(r.ChatId); chat != "" {
		told := "its results are told there"
		if !r.Announce {
			told = "its results are not told there"
		}

		e.printf("made in chat %s; %s\n", chat, told)
	}

	if r.CanUndo {
		e.printf("a bot's change; undo it with `hypomnema routines undo %s`\n", r.Id)
	}

	if text := val(r.Prompt); text != "" {
		e.printf("\nprompt:\n%s\n", text)
	}

	if text := val(r.ScriptText); text != "" {
		e.printf("\nscript %s (stops after %d s):\n%s\n", val(r.Script), r.TimeoutSeconds, text)
	}

	if len(r.Grants) > 0 {
		e.printf("\nallowed always for this routine:\n")

		for _, g := range r.Grants {
			e.printf("- %s:%s %s (%s)\n", g.Integration, g.Label, g.Action, g.ActionName)
		}
	}
}

func getRoutine(ctx context.Context, e *env, id string) (*openapi.Routine, error) {
	resp, err := e.api.GetRoutineWithResponse(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("reading routine %s: %w", id, err)
	}

	if resp.JSON200 == nil {
		return nil, apiError("reading routine "+id, resp.StatusCode(), resp.JSONDefault)
	}

	return resp.JSON200, nil
}

func routinesList(ctx context.Context, e *env, fs *flag.FlagSet) error {
	resp, err := e.api.ListRoutinesWithResponse(ctx, &openapi.ListRoutinesParams{Bot: optional(fs, "bot")})
	if err != nil {
		return fmt.Errorf("listing routines: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("listing routines", resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() {
		if len(resp.JSON200.Data) == 0 {
			e.printf("no routine\n")
		}

		for _, r := range resp.JSON200.Data {
			printRoutineLine(e, r)
		}
	})
}

func routinesShow(ctx context.Context, e *env, fs *flag.FlagSet) error {
	r, err := getRoutine(ctx, e, fs.Arg(0))
	if err != nil {
		return err
	}

	limit := number(fs, "runs")

	runs, err := e.api.ListRoutineRunsWithResponse(ctx, r.Id, &openapi.ListRoutineRunsParams{Limit: &limit})
	if err != nil {
		return fmt.Errorf("listing the runs of routine %s: %w", r.Id, err)
	}

	if runs.JSON200 == nil {
		return apiError("listing the runs of routine "+r.Id, runs.StatusCode(), runs.JSONDefault)
	}

	shown := struct {
		*openapi.Routine

		Runs []openapi.RoutineRun `json:"runs"`
	}{Routine: r, Runs: runs.JSON200.Data}

	return e.print(fs, shown, func() {
		printRoutine(e, r)

		if len(shown.Runs) > 0 {
			e.printf("\nruns, the newest first:\n")
		}

		for _, run := range shown.Runs {
			printRun(e, run)
		}

		if len(shown.Runs) > 0 && shown.Runs[0].Output != "" {
			e.printf("\nthe end of the last run's output:\n%s\n", shown.Runs[0].Output)
		}
	})
}

// routineNamed finds a Bot's Routine by its name; nil when there is none.
func routineNamed(ctx context.Context, e *env, bot, name string) (*openapi.Routine, error) {
	resp, err := e.api.ListRoutinesWithResponse(ctx, &openapi.ListRoutinesParams{Bot: &bot})
	if err != nil {
		return nil, fmt.Errorf("listing the routines of bot %s: %w", bot, err)
	}

	if resp.JSON200 == nil {
		return nil, apiError("listing the routines of bot "+bot, resp.StatusCode(), resp.JSONDefault)
	}

	for _, r := range resp.JSON200.Data {
		if r.Name == name {
			return &r, nil
		}
	}

	return nil, nil //nolint:nilnil // No Routine of that name is an answer, not a failure.
}

// routinesSet makes a Routine or changes what the flags name. It is on at
// once, for the owner and for a Bot alike.
func routinesSet(ctx context.Context, e *env, fs *flag.FlagSet) error {
	bot, name := routineBot(fs), fs.Arg(0)

	have, err := routineNamed(ctx, e, bot, name)
	if err != nil {
		return err
	}

	set, err := routineSettings(e, fs, have)
	if err != nil {
		return err
	}

	resp, err := e.api.PutRoutineWithResponse(ctx, bot, name, set)
	if err != nil {
		return fmt.Errorf("saving routine %s of bot %s: %w", name, bot, err)
	}

	if resp.JSON200 == nil {
		return apiError("saving routine "+name+" of bot "+bot, resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() { printRoutine(e, resp.JSON200) })
}

func routinesUndo(ctx context.Context, e *env, fs *flag.FlagSet) error {
	id := fs.Arg(0)

	resp, err := e.api.UndoRoutineWithResponse(ctx, id)
	if err != nil {
		return fmt.Errorf("undoing the change of routine %s: %w", id, err)
	}

	if resp.JSON200 == nil {
		return apiError("undoing the change of routine "+id, resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() {
		if resp.JSON200.Removed || resp.JSON200.Routine == nil {
			e.printf("routine %s, which a bot made, is removed\n", id)

			return
		}

		e.printf("routine %s is what it was before the bot's change:\n", id)
		printRoutine(e, resp.JSON200.Routine)
	})
}

func routinesPause(ctx context.Context, e *env, fs *flag.FlagSet) error {
	id := fs.Arg(0)

	resp, err := e.api.PauseRoutineWithResponse(ctx, id)
	if err != nil {
		return fmt.Errorf("pausing routine %s: %w", id, err)
	}

	if resp.JSON200 == nil {
		return apiError("pausing routine "+id, resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() { printRoutineLine(e, *resp.JSON200) })
}

func routinesResume(ctx context.Context, e *env, fs *flag.FlagSet) error {
	id := fs.Arg(0)

	resp, err := e.api.ResumeRoutineWithResponse(ctx, id)
	if err != nil {
		return fmt.Errorf("resuming routine %s: %w", id, err)
	}

	if resp.JSON200 == nil {
		return apiError("resuming routine "+id, resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() { printRoutineLine(e, *resp.JSON200) })
}

func routinesRun(ctx context.Context, e *env, fs *flag.FlagSet) error {
	id := fs.Arg(0)

	if os.Getenv(routineEnv) != "" {
		return errors.New("a routine's run starts no routines")
	}

	resp, err := e.api.RunRoutineWithResponse(ctx, id)
	if err != nil {
		return fmt.Errorf("running routine %s: %w", id, err)
	}

	if resp.JSON200 == nil {
		return apiError("running routine "+id, resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() { printRun(e, *resp.JSON200) })
}

func routinesRemove(ctx context.Context, e *env, fs *flag.FlagSet) error {
	id := fs.Arg(0)

	resp, err := e.api.DeleteRoutineWithResponse(ctx, id)
	if err != nil {
		return fmt.Errorf("removing routine %s: %w", id, err)
	}

	if resp.StatusCode() != 204 {
		return apiError("removing routine "+id, resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, map[string]bool{"removed": true}, func() { e.printf("routine %s is removed\n", id) })
}

// routinesRevoke takes back an "always for this Routine".
func routinesRevoke(ctx context.Context, e *env, fs *flag.FlagSet) error {
	id, action := fs.Arg(0), fs.Arg(2)

	integration, label, err := account(fs.Arg(1))
	if err != nil {
		return err
	}

	resp, err := e.api.RevokeRoutineGrantWithResponse(ctx, id, integration, label, action)
	if err != nil {
		return fmt.Errorf("taking back %s of routine %s: %w", action, id, err)
	}

	if resp.StatusCode() != 204 {
		return apiError("taking back "+action+" of routine "+id, resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, map[string]bool{"revoked": true}, func() {
		e.printf("routine %s asks again for %s of %s\n", id, action, fs.Arg(1))
	})
}

// routinesScript is for the launcher of a script Routine
// (deploy/coordinator/hypomnema-routine). It asks the control plane for what
// the Routine's starting run runs, which is given once a run, writes the
// script into the Bot's routines directory and the run's key beside it, and
// prints the file's name and the seconds it may run, one a line. The control
// plane's text is what runs, so the file is what the owner read. The key is
// never printed: the tools a script calls read it from its file.
func routinesScript(ctx context.Context, e *env, fs *flag.FlagSet) error {
	id, bot, dir := fs.Arg(0), str(fs, "bot"), str(fs, "into")
	if bot == "" || dir == "" {
		return fmt.Errorf("%w: name the routine's bot with --bot and the directory with --into", ErrUsage)
	}

	resp, err := e.api.LaunchRoutineScriptWithResponse(ctx, id, openapi.RoutineLaunchRequest{Bot: bot})
	if err != nil {
		return fmt.Errorf("starting the script of routine %s: %w", id, err)
	}

	if resp.JSON200 == nil {
		return apiError("starting the script of routine "+id, resp.StatusCode(), resp.JSONDefault)
	}

	launch := resp.JSON200

	name := launch.Script
	if name == "" || name != filepath.Base(name) || strings.HasPrefix(name, ".") {
		return fmt.Errorf("routine %s names no file", id)
	}

	if err := writeFile(dir, name, launch.ScriptText, 0o700); err != nil {
		return err
	}

	if err := writeFile(dir, filepath.Base(runKeyFile(dir, id)), launch.Key+"\n", 0o600); err != nil {
		return err
	}

	e.printf("%s\n%d\n", name, launch.TimeoutSeconds)

	return nil
}

// writeFile puts a file beside its place and moves it there, so a run never
// reads half of one.
func writeFile(dir, name, text string, mode os.FileMode) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("making %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".new-*")
	if err != nil {
		return fmt.Errorf("writing %s: %w", name, err)
	}

	_, err = tmp.WriteString(text)
	err = errors.Join(err, tmp.Chmod(mode), tmp.Close())

	if err == nil {
		err = os.Rename(tmp.Name(), filepath.Join(dir, name))
	}

	if err != nil {
		_ = os.Remove(tmp.Name())

		return fmt.Errorf("writing %s: %w", name, err)
	}

	return nil
}
