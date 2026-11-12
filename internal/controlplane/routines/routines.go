// Package routines runs the Routines of the Bots (issue #211): every minute
// it starts a run of each Routine whose time has come, and it follows the
// runs that still go until they end. The record is orchestration's; what a
// run does is here.
//
// A script Routine runs through a root-owned launcher that takes only a
// Bot's name and a Routine's id (deploy/coordinator/hypomnema-routine), as
// pi is started for a chat. A prompt Routine is a new chat with the prompt
// waiting in it: the coordinator's supervisor starts pi for it as for any
// chat, and this package reads the chat to know how the run ended.
//
// Each rule names the code it follows; the comparison is in
// docs/research/2026-11-12-work-on-a-schedule.md.
package routines

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/coordinator"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

const (
	// TickEvery is how often the Routines that are due are looked for
	// (Hermes and OpenMausBot: 60 s).
	TickEvery = 60 * time.Second
	// OverdueAtOnce is how many Routines found overdue when the control
	// plane starts begin at once, and OverdueApart how long apart the rest
	// begin (OpenClaw, src/cron/service/timer-constants.ts L12-16).
	OverdueAtOnce = 5
	OverdueApart  = 5 * time.Second
	// stopGrace is how long a script has to end once it was told to, and how
	// long after its limit the control plane stops waiting for it: the
	// launcher ends the script at the limit itself.
	stopGrace = 30 * time.Second
	// timedOut is the exit code of coreutils' timeout, which the launcher
	// runs the script under.
	timedOut = 124
)

// Store is what the runner keeps (orchestration.Service).
type Store interface {
	ClaimDueRoutines(ctx context.Context, limit int) ([]orchestration.StartedRun, bool, error)
	RunRoutineNow(ctx context.Context, id string) (orchestration.StartedRun, error)
	DeleteRoutine(ctx context.Context, id string) (*orchestration.RoutineRun, error)
	UndoRoutine(ctx context.Context, id string) (orchestration.Routine, bool, *orchestration.RoutineRun, error)
	LiveRoutineRuns(ctx context.Context) ([]orchestration.StartedRun, error)
	FinishRoutineRun(ctx context.Context, runID string, r orchestration.RunResult) (orchestration.Settled, error)
	RoutineRunAnswers(ctx context.Context, runID string) (orchestration.RunAnswers, error)
	PruneRoutineRuns(ctx context.Context) error
	Undelivered(ctx context.Context, chatID string) ([]orchestration.ChatMessage, error)
	ListChatMessages(ctx context.Context, chatID string, after int64, limit int) ([]orchestration.ChatMessage, error)
	TellChat(ctx context.Context, chatID, text, routineID, runID string) error
	Subscribe(ctx context.Context) <-chan struct{}
}

// Coordinator is the part of the supervisor of the Bots' pi a prompt run
// needs.
type Coordinator interface {
	Enabled() bool
	Working(chatID string) bool
	Abort(ctx context.Context, chatID string) error
}

// Owner tells the owner something through the Lark Channels of a Bot,
// except a Channel through which he already reads the chat named.
type Owner interface {
	TellOwner(ctx context.Context, bot, chatID, title, text string)
}

// Exec runs a command until it ends, or until ctx ends, which stops it,
// with the run's token, when there is one, as the first line of its input.
// It returns what the command printed, its exit code, and an error when it
// could not be run at all.
type Exec func(ctx context.Context, argv []string, token string) (output string, exitCode int, err error)

// Tokens makes and ends the token of one script run (issue #260's token per
// pi, for a Routine's script: #211's comment of 2026-11-12). The launcher
// reads it as the first line of its input and gives it to the script in its
// box, so what the script asks the control plane is limited to its Bot.
type Tokens interface {
	// AddRoutineToken returns a new token's secret.
	AddRoutineToken(ctx context.Context, routineID string) (string, error)
	// RevokeBotToken stops it from working.
	RevokeBotToken(secret string)
}

// Config is what a Runner is made of.
type Config struct {
	// Command runs a script Routine for the Bot's name and the Routine's id
	// appended to it, such as [sudo -n -u coordinator
	// /usr/local/bin/hypomnema-routine]. Empty: a script run fails, saying so.
	Command []string
	// Coordinator answers chats; nil when none runs.
	Coordinator Coordinator
	// Tokens makes each script run's token; nil gives the launcher none, and
	// the launcher then runs no script.
	Tokens Tokens
	// Owner tells the owner of repeated failures; nil tells nobody there.
	Owner  Owner
	Logger *slog.Logger
	// Now is the clock a prompt run's limit is read on; time.Now when nil.
	Now func() time.Time
	// Exec runs a script's command; the real one when nil. Tests set it.
	Exec Exec
	// After waits between the Routines found overdue at the start;
	// time.After when nil. Tests set it.
	After func(time.Duration) <-chan time.Time
}

// Runner starts and follows the runs of Routines.
type Runner struct {
	store Store
	cfg   Config

	mu sync.Mutex
	// scripts are the runs whose script is running now, by run id, each with
	// what stops it.
	scripts map[string]context.CancelFunc
	wg      sync.WaitGroup
}

// New returns a Runner; Run starts it.
func New(store Store, cfg Config) *Runner {
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}

	if cfg.Now == nil {
		cfg.Now = time.Now
	}

	if cfg.Exec == nil {
		cfg.Exec = run
	}

	if cfg.After == nil {
		cfg.After = time.After
	}

	return &Runner{store: store, cfg: cfg, scripts: map[string]context.CancelFunc{}}
}

// run is the real Exec. The launcher is not signaled: it runs as another
// user, whom the control plane's user cannot signal. It is given a pipe as
// its standard input, the token first on it, and ends its script when the
// pipe closes, as pi is stopped by closing its input.
func run(ctx context.Context, argv []string, token string) (string, int, error) {
	//nolint:gosec // The owner's configured command, with a Bot's name and a Routine's id the control plane made.
	// It is ended by closing its input, below, not by the context.
	cmd := exec.CommandContext(context.WithoutCancel(ctx), argv[0], argv[1:]...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", 0, fmt.Errorf("opening the launcher's input: %w", err)
	}

	out := &ending{keep: 4 * orchestration.MaxRunOutput}
	cmd.Stdout, cmd.Stderr = out, out
	// The pipes are given up when something the script left behind still holds them.
	cmd.WaitDelay = stopGrace

	if err := cmd.Start(); err != nil {
		return "", 0, err
	}

	// A secret given on stdin, never on a command line: sudo passes no environment (coordinator/proc.go).
	if token != "" {
		if _, err := io.WriteString(stdin, token+"\n"); err != nil {
			_ = stdin.Close()
			_ = cmd.Wait()

			return out.String(), 0, fmt.Errorf("giving the launcher the run's token: %w", err)
		}
	}

	done := make(chan error, 1)

	go func() { done <- cmd.Wait() }()

	select {
	case err = <-done:
	case <-ctx.Done():
		_ = stdin.Close()

		select {
		case err = <-done:
		case <-time.After(stopGrace):
			return out.String(), 0, errors.New("the script did not stop when it was told to")
		}
	}

	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return out.String(), exit.ExitCode(), nil
	}

	return out.String(), 0, err
}

// ending keeps the end of what is written to it: a script may print without
// bound, and only the end of its output is kept.
type ending struct {
	mu   sync.Mutex
	keep int
	buf  bytes.Buffer
}

func (e *ending) Write(p []byte) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.buf.Write(p)

	if over := e.buf.Len() - 2*e.keep; over > 0 {
		e.buf.Next(e.buf.Len() - e.keep)
	}

	return len(p), nil
}

func (e *ending) String() string {
	e.mu.Lock()
	defer e.mu.Unlock()

	return strings.ToValidUTF8(e.buf.String(), "")
}

// Run starts the runs that are due every TickEvery and follows the live
// ones after every commit, until ctx ends; then it waits for the scripts
// that still run to be given up.
func (r *Runner) Run(ctx context.Context) error {
	r.Interrupted(ctx)

	wake := r.store.Subscribe(ctx)
	tick := time.NewTicker(TickEvery)

	defer tick.Stop()
	defer r.wg.Wait()

	r.CatchUp(ctx)

	for {
		r.Follow(ctx)

		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			r.Tick(ctx)
		case <-wake:
		}
	}
}

// Interrupted ends the runs that were live when the control plane last
// stopped: nothing runs them any more, and what they waited for is gone
// with the call that waited. Each is recorded as failed and is no failure
// of its Routine; a prompt is run again, a script is not (OpenClaw,
// service/startup-run-repair.ts L45-68, service/jobs-scheduling.ts L31-49).
func (r *Runner) Interrupted(ctx context.Context) {
	live, err := r.store.LiveRoutineRuns(ctx)
	if err != nil {
		r.failed(ctx, "reading the runs a restart interrupted", "", err)

		return
	}

	for _, l := range live {
		r.finish(ctx, l.Run.ID, orchestration.RunResult{
			State: orchestration.RunFailed, Interrupted: true, ExitCode: l.Run.ExitCode, Output: l.Run.Output,
			Error: "the control plane restarted while it ran",
		})
	}
}

// CatchUp starts the Routines found overdue when the control plane starts:
// each once, at most OverdueAtOnce at once and the rest OverdueApart apart.
func (r *Runner) CatchUp(ctx context.Context) {
	for limit := OverdueAtOnce; ; limit = 1 {
		more, err := r.claim(ctx, limit)
		if err != nil || !more {
			return
		}

		select {
		case <-ctx.Done():
			return
		case <-r.cfg.After(OverdueApart):
		}
	}
}

// Tick starts a run of every Routine whose time has come and removes the
// runs that are no longer kept.
func (r *Runner) Tick(ctx context.Context) {
	if _, err := r.claim(ctx, 0); err != nil {
		return
	}

	if err := r.store.PruneRoutineRuns(ctx); err != nil {
		r.failed(ctx, "removing old runs", "", err)
	}
}

func (r *Runner) claim(ctx context.Context, limit int) (more bool, err error) {
	started, more, err := r.store.ClaimDueRoutines(ctx, limit)
	if err != nil {
		r.failed(ctx, "starting the routines that are due", "", err)

		return false, err
	}

	for _, s := range started {
		r.begin(ctx, s)
	}

	return more, nil
}

// RunNow starts a run by hand and returns it. While a run of the Routine is
// live it is a conflict that names that run.
func (r *Runner) RunNow(ctx context.Context, id string) (orchestration.RoutineRun, error) {
	started, err := r.store.RunRoutineNow(ctx, id)
	if err != nil {
		return orchestration.RoutineRun{}, err
	}

	r.begin(ctx, started)

	return started.Run, nil
}

// Remove removes a Routine and stops the run of it that is live (OpenClaw,
// service/ops-mutations.ts L427-456).
func (r *Runner) Remove(ctx context.Context, id string) error {
	live, err := r.store.DeleteRoutine(ctx, id)
	if err != nil {
		return err
	}

	r.stop(ctx, live)

	return nil
}

// Undo undoes a Bot's last change of a Routine. A Routine the Bot made is
// removed, and its live run stopped.
func (r *Runner) Undo(ctx context.Context, id string) (orchestration.Routine, bool, error) {
	routine, removed, live, err := r.store.UndoRoutine(ctx, id)
	if err != nil {
		return orchestration.Routine{}, false, err
	}

	r.stop(ctx, live)

	return routine, removed, nil
}

// stop ends a run whose Routine is gone: a script is told to end, pi's turn
// is aborted. The run's record went with the Routine.
func (r *Runner) stop(ctx context.Context, run *orchestration.RoutineRun) {
	if run == nil {
		return
	}

	r.mu.Lock()
	cancel := r.scripts[run.ID]
	r.mu.Unlock()

	if cancel != nil {
		cancel()
	}

	if run.ChatID != "" && r.cfg.Coordinator != nil {
		if err := r.cfg.Coordinator.Abort(ctx, run.ChatID); err != nil {
			r.failed(ctx, "stopping a removed routine's run", run.ID, err)
		}
	}

	r.cfg.Logger.InfoContext(ctx, "a removed routine's run was stopped", slog.String("event", "routines.stopped"),
		slog.String("routine", run.RoutineID), slog.String("run", run.ID))
}

// begin does what a run that just started needs: a script is run; a prompt
// waits in its chat for the supervisor, which the commit has woken.
func (r *Runner) begin(ctx context.Context, s orchestration.StartedRun) {
	r.cfg.Logger.InfoContext(ctx, "a routine's run started", slog.String("event", "routines.started"),
		slog.String("routine", s.Routine.ID), slog.String("bot", s.Routine.Bot), slog.String("run", s.Run.ID),
		slog.String("started_by", s.Run.StartedBy), slog.String("kind", string(s.Routine.Kind)))

	if s.Routine.Kind != orchestration.RoutineScript {
		return
	}

	// The run outlives the request that started it; it ends with the control plane, or when it is stopped.
	runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.Routine.Timeout+stopGrace)

	r.mu.Lock()
	r.scripts[s.Run.ID] = cancel
	r.mu.Unlock()

	r.wg.Go(func() {
		defer func() {
			cancel()

			r.mu.Lock()
			delete(r.scripts, s.Run.ID)
			r.mu.Unlock()
		}()

		r.runScript(runCtx, s)
	})
}

// runScript runs a script Routine's run once, from its first line to its
// end. A request of the script that needs the owner waits inside the script,
// in the call that made it (the service proxy holds it): the script is never
// started again for an answer.
func (r *Runner) runScript(ctx context.Context, s orchestration.StartedRun) {
	// What ends the run is recorded whatever stopped the script.
	record := context.WithoutCancel(ctx)

	if len(r.cfg.Command) == 0 {
		r.finish(record, s.Run.ID, orchestration.RunResult{
			State: orchestration.RunFailed,
			Error: "this control plane runs no scripts (HYPOMNEMA_ROUTINE_COMMAND is not set)",
		})

		return
	}

	// The run's own token, which ends with the run.
	var token string

	if r.cfg.Tokens != nil {
		var err error

		token, err = r.cfg.Tokens.AddRoutineToken(record, s.Routine.ID)
		if err != nil {
			r.finish(record, s.Run.ID, orchestration.RunResult{
				State: orchestration.RunFailed, Error: "making the run's token failed: " + err.Error(),
			})

			return
		}

		defer r.cfg.Tokens.RevokeBotToken(token)
	}

	argv := append(append([]string{}, r.cfg.Command...), s.Routine.Bot, s.Routine.ID)
	output, code, err := r.cfg.Exec(ctx, argv, token)

	result := orchestration.RunResult{State: orchestration.RunSucceeded, ExitCode: &code, Output: output}

	// How a script's failure is read is OpenClaw's (src/cron/script-failure.ts L7-15): a script that ran out of
	// time, and one that could not be run at all, may pass by themselves; any other exit is the script's own.
	switch {
	case err != nil:
		result = orchestration.RunResult{
			State: orchestration.RunFailed, Output: output, Passing: true,
			Error: "the script could not be run: " + err.Error(),
		}
	case code == timedOut:
		result.State, result.Passing = orchestration.RunFailed, true
		result.Error = fmt.Sprintf("the script was stopped at its limit of %s", s.Routine.Timeout)
	case code != 0:
		result.State = orchestration.RunFailed
		result.Error = fmt.Sprintf("the script exited with code %d", code)
	}

	r.finish(record, s.Run.ID, r.answered(record, s.Run.ID, result))
}

// answered changes how a run ended by what the owner said to what it asked:
// his no ends the run canceled, which is no failure; a request nobody
// answered in time fails it.
func (r *Runner) answered(ctx context.Context, runID string, result orchestration.RunResult) orchestration.RunResult {
	answers, err := r.store.RoutineRunAnswers(ctx, runID)
	if err != nil {
		r.failed(ctx, "reading what the owner answered a run", runID, err)

		return result
	}

	switch {
	case answers.Rejected != nil:
		result.State, result.Passing = orchestration.RunStopped, false
		result.Error = fmt.Sprintf("the owner rejected %q", answers.Rejected.Title)
	case answers.Expired != nil:
		result.State, result.Passing = orchestration.RunFailed, false
		result.Error = fmt.Sprintf("nobody answered the approval of %q in time", answers.Expired.Title)
	}

	return result
}

// Follow looks at every live prompt run and ends those whose Bot has
// answered and stopped working. A script's run ends when its script does.
func (r *Runner) Follow(ctx context.Context) {
	live, err := r.store.LiveRoutineRuns(ctx)
	if err != nil {
		r.failed(ctx, "reading the live runs", "", err)

		return
	}

	for _, l := range live {
		if l.Routine.Kind != orchestration.RoutinePrompt {
			continue
		}

		if err := r.followPrompt(ctx, l); err != nil && ctx.Err() == nil {
			r.failed(ctx, "following a run", l.Run.ID, err)
		}
	}
}

func (r *Runner) followPrompt(ctx context.Context, l orchestration.StartedRun) error {
	chat := l.Run.ChatID

	if r.cfg.Coordinator == nil || !r.cfg.Coordinator.Enabled() {
		r.finish(ctx, l.Run.ID, orchestration.RunResult{
			State: orchestration.RunFailed, Error: "nothing answers chats on this control plane (" +
				coordinator.ErrDisabled.Error() + ")",
		})

		return nil
	}

	waiting, err := r.store.Undelivered(ctx, chat)
	if err != nil {
		return err
	}

	// The limit is wall time from the run's start, the time it waits for the owner included (OpenClaw,
	// src/cron/service/timeout-policy.ts L18-31, resolveCronJobTimeoutMs).
	overdue := r.cfg.Now().Sub(l.Run.StartedAt) > l.Routine.Timeout
	late := orchestration.RunResult{State: orchestration.RunFailed, Passing: true}

	if len(waiting) > 0 || r.cfg.Coordinator.Working(chat) {
		if !overdue {
			return nil
		}

		err = r.cfg.Coordinator.Abort(ctx, chat)
		late.Error = fmt.Sprintf("stopped after %s", l.Routine.Timeout)
		r.finish(ctx, l.Run.ID, late)

		return err
	}

	answer, failure, err := r.lastWord(ctx, chat)
	if err != nil {
		return err
	}

	switch {
	case failure != "":
		r.finish(ctx, l.Run.ID, r.answered(ctx, l.Run.ID, orchestration.RunResult{
			State: orchestration.RunFailed, Output: answer, Error: failure, Passing: passes(failure),
		}))
	case answer != "":
		r.finish(ctx, l.Run.ID, r.answered(ctx, l.Run.ID, orchestration.RunResult{
			State: orchestration.RunSucceeded, Output: answer,
		}))
	case overdue:
		// The Bot was given the prompt and said nothing: pi did not start, or ended without a word.
		late.Error = fmt.Sprintf("the bot gave no answer in %s", l.Routine.Timeout)
		r.finish(ctx, l.Run.ID, late)
	}

	return nil
}

// lastWord reads how a chat's last turn ended: the Bot's last answer, and
// the failure of its model's call when that came after it.
func (r *Runner) lastWord(ctx context.Context, chatID string) (answer, failure string, err error) {
	after := int64(0)

	for {
		msgs, err := r.store.ListChatMessages(ctx, chatID, after, orchestration.ChatPageMax)
		if err != nil {
			return "", "", err
		}

		for _, m := range msgs {
			switch {
			case m.Role == orchestration.ChatCoordinator && strings.TrimSpace(m.Text) != "":
				answer, failure = m.Text, ""
			case m.Kind == orchestration.ChatFailed:
				failure = m.Text
			}

			after = m.Seq
		}

		if len(msgs) < orchestration.ChatPageMax {
			return answer, failure, nil
		}
	}
}

// finish ends a run and tells whoever its end concerns.
func (r *Runner) finish(ctx context.Context, runID string, result orchestration.RunResult) {
	settled, err := r.store.FinishRoutineRun(ctx, runID, result)
	if err != nil {
		// A run of a Routine that was removed meanwhile ends into nothing.
		if !errors.Is(err, orchestration.ErrNotFound) && !errors.Is(err, orchestration.ErrConflict) {
			r.failed(ctx, "ending a run", runID, err)
		}

		return
	}

	routine, run := settled.Routine, settled.Run

	r.cfg.Logger.InfoContext(ctx, "a routine's run ended", slog.String("event", "routines.ended"),
		slog.String("routine", routine.ID), slog.String("bot", routine.Bot), slog.String("run", run.ID),
		slog.String("state", string(run.State)), slog.String("error", run.Error),
		slog.Int("failures", routine.Failures), slog.Bool("paused", settled.Paused))

	// A run's answer, or what a script printed, goes to the chat the Routine was made in unless the owner chose
	// none (OpenClaw, src/cron/delivery-defaults.ts L155-189: agent, command and script jobs announce by default).
	if out := strings.TrimSpace(run.Output); run.State == orchestration.RunSucceeded && routine.Announce && out != "" {
		r.tell(ctx, routine.ChatID, fmt.Sprintf("Routine %q ran (%s):\n\n%s", routine.Name, run.ID, out),
			routine.ID, run.ID)
	}

	switch {
	case settled.Paused:
		// Always told, with the reason and how to switch it on again (OpenClaw, service/auto-disable.ts L20-49).
		r.alert(ctx, routine, "A routine was paused", fmt.Sprintf("Routine %q of %s was %s\n\nFix what fails, then "+
			"switch it on again with `hypomnema routines resume %s`.", routine.Name, routine.Bot, routine.PausedReason,
			routine.ID))
	case settled.Alert:
		r.alert(ctx, routine, "A routine keeps failing", fmt.Sprintf("Routine %q of %s failed %d times in a row. "+
			"The last: %s\n\nRead it with `hypomnema routines show %s`.", routine.Name, routine.Bot, routine.Failures,
			run.Error, routine.ID))
	}
}

// alert tells the owner in the chat the Routine was made in and through the
// Bot's Lark Channels.
func (r *Runner) alert(ctx context.Context, routine orchestration.Routine, title, text string) {
	r.tell(ctx, routine.ChatID, text, "", "")

	if r.cfg.Owner != nil {
		r.cfg.Owner.TellOwner(ctx, routine.Bot, routine.ChatID, title, text)
	}
}

func (r *Runner) tell(ctx context.Context, chatID, text, routineID, runID string) {
	if chatID == "" {
		return
	}

	if err := r.store.TellChat(ctx, chatID, text, routineID, runID); err != nil {
		r.failed(ctx, "telling a chat of a run", chatID, err)
	}
}

func (r *Runner) failed(ctx context.Context, doing, id string, err error) {
	if ctx.Err() != nil {
		return
	}

	r.cfg.Logger.ErrorContext(ctx, doing+" failed", slog.String("event", "routines.failed"), slog.String("id", id),
		slog.Any("error", err))
}
