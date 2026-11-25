package routines_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/routinestore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/routines"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

// The runner against the real store, with a script's command, pi and Lark
// stood in for. Time is the test's clock; what runs on a goroutine of the
// runner is waited for by looking at the store.

const (
	eventual = 5 * time.Second
	every    = 5 * time.Millisecond
)

var (
	t0    = time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)
	owner = routinestore.RoutineAuthor{}
)

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)
}

// answer is what a stand-in script does: its output and exit code, or why
// it could not be run.
type answer func(ctx context.Context) (string, int, error)

// script stands in for the launcher: it records what it was run with and
// does what the test queued, or prints three rows and exits 0.
type script struct {
	mu    sync.Mutex
	calls [][]string
	next  []answer
	// tokens are the tokens it was given, one a run.
	tokens []string
}

func (s *script) exec(ctx context.Context, argv []string, token string) (string, int, error) {
	s.mu.Lock()
	s.calls = append(s.calls, argv)
	s.tokens = append(s.tokens, token)

	var do answer
	if len(s.next) > 0 {
		do, s.next = s.next[0], s.next[1:]
	}
	s.mu.Unlock()

	if do == nil {
		return "3 rows written\n", 0, nil
	}

	return do(ctx)
}

func (s *script) then(do answer) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.next = append(s.next, do)
}

func exits(output string, code int) answer {
	return func(context.Context) (string, int, error) { return output, code, nil }
}

func (s *script) ran() [][]string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([][]string(nil), s.calls...)
}

// pi stands in for the supervisor of the Bots' pi. A chat's pi runs unless
// it exited.
type pi struct {
	mu      sync.Mutex
	off     bool
	working map[string]bool
	exited  map[string]bool
	aborted []string
}

func (p *pi) Enabled() bool { return !p.off }

func (p *pi) Working(chatID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.working[chatID]
}

func (p *pi) Idle(chatID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	return !p.working[chatID] && !p.exited[chatID]
}

func (p *pi) Abort(_ context.Context, chatID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.aborted = append(p.aborted, chatID)

	return nil
}

func (p *pi) stopped() []string {
	p.mu.Lock()
	defer p.mu.Unlock()

	return append([]string(nil), p.aborted...)
}

// lark keeps what the owner was told through the Bots' Lark Channels.
type lark struct {
	mu   sync.Mutex
	told []string
}

func (l *lark) TellOwner(_ context.Context, bot, chatID, title, text string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.told = append(l.told, fmt.Sprintf("%s|%s|%s|%s", bot, chatID, title, text))
}

func (l *lark) heard() []string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return append([]string(nil), l.told...)
}

type world struct {
	t      *testing.T
	svc    *orchestration.Service
	clock  *clock
	script *script
	pi     *pi
	lark   *lark
	runner *routines.Runner
	// tokens makes each script run's token; nil gives none.
	tokens routines.Tokens
	// waits are the times the runner waited between overdue Routines.
	waits []time.Duration
}

func newWorld(t *testing.T) *world {
	t.Helper()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	n := 0
	w := &world{
		t: t, clock: &clock{now: t0}, script: &script{}, lark: &lark{},
		pi: &pi{working: map[string]bool{}, exited: map[string]bool{}},
	}
	w.svc = orchestration.New(db, w.clock, func(prefix string) string {
		n++

		return fmt.Sprintf("%s_%04d", prefix, n)
	})
	w.runner = w.runnerWith("sudo", "-n", "-u", "coordinator", "/usr/local/bin/hypomnema-routine")

	return w
}

func (w *world) runnerWith(command ...string) *routines.Runner {
	return routines.New(w.svc, w.svc.Routines(), routines.Config{
		Command: command, Coordinator: w.pi, Owner: w.lark, Now: w.clock.Now, Exec: w.script.exec, Tokens: w.tokens,
		After: func(d time.Duration) <-chan time.Time {
			w.waits = append(w.waits, d)

			done := make(chan time.Time, 1)
			done <- w.clock.Now()

			return done
		},
	})
}

func (w *world) hourly(name string, chatID string) routinestore.Routine {
	w.t.Helper()

	r, err := w.svc.Routines().PutRoutine(w.t.Context(), routinestore.RoutineSpec{
		Bot: orchestration.DefaultBotID, Name: name, Kind: routinestore.RoutineScript, Script: "sync.sh",
		ScriptText: "#!/bin/sh\necho 3 rows written\n", EveryMinutes: 60, ChatID: chatID, Announce: true,
	}, owner)
	require.NoError(w.t, err)

	return r
}

func (w *world) daily(name, chatID string) routinestore.Routine {
	w.t.Helper()

	r, err := w.svc.Routines().PutRoutine(w.t.Context(), routinestore.RoutineSpec{
		Bot: orchestration.DefaultBotID, Name: name, Kind: routinestore.RoutinePrompt,
		Prompt: "Summarize what changed overnight.", EveryMinutes: 1440, ChatID: chatID, Announce: true,
	}, owner)
	require.NoError(w.t, err)

	return r
}

func (w *world) chat() string {
	w.t.Helper()

	c, err := w.svc.CreateChat(w.t.Context(), "", "Where the routine was made")
	require.NoError(w.t, err)

	return c.ID
}

func (w *world) routine(id string) routinestore.Routine {
	w.t.Helper()

	r, err := w.svc.Routines().GetRoutine(w.t.Context(), id)
	require.NoError(w.t, err)

	return r
}

// ended waits until the Routine has n runs and the newest is in a state,
// and returns it.
func (w *world) ended(routineID string, n int, state routinestore.RunState) routinestore.RoutineRun {
	w.t.Helper()

	var run routinestore.RoutineRun

	require.Eventually(w.t, func() bool {
		runs, err := w.svc.Routines().ListRoutineRuns(w.t.Context(), routineID, 0)
		if err != nil || len(runs) != n {
			return false
		}

		run = runs[0]

		return run.State == state
	}, eventual, every, "run %d of %s never was %s", n, routineID, state)

	return run
}

// said waits until a chat has n lines of the Bot and returns them.
func (w *world) said(chatID string, n int) []string {
	w.t.Helper()

	var out []string

	require.Eventually(w.t, func() bool {
		msgs, err := w.svc.ListChatMessages(w.t.Context(), chatID, 0, 0)
		if err != nil {
			return false
		}

		out = out[:0]

		for _, m := range msgs {
			if m.Role == orchestration.ChatCoordinator {
				out = append(out, m.Text)
			}
		}

		return len(out) == n
	}, eventual, every, "chat %s never had %d lines of the bot", chatID, n)

	return out
}

// says is pi's turn in a chat: it takes what waits for it and answers.
func (w *world) says(chatID, text string) {
	w.t.Helper()

	require.NoError(w.t, w.svc.MarkDelivered(w.t.Context(), chatID, 1<<40))

	_, err := w.svc.AddChatMessage(w.t.Context(), chatID, orchestration.ChatCoordinator, text)
	require.NoError(w.t, err)
}

func (w *world) work(chatID string, working bool) {
	w.pi.mu.Lock()
	defer w.pi.mu.Unlock()

	w.pi.working[chatID] = working
}

func change(n orchestration.NewApproval) orchestration.NewApproval {
	n.Integration, n.Label, n.Action, n.ActionName = "lark", "work", "lark.base.write", "Change a Base"
	n.AskedBy, n.Title, n.MatchHash = "pi", "Change a Base: usage", "rows"

	return n
}

// held is a script's request that needs the owner, as the service proxy
// holds it: it asks, waits in place until the Approval is answered, and
// says how it ended.
func (w *world) held(ctx context.Context, routineID string, asked chan<- orchestration.Approval) (string, error) {
	launch, err := w.svc.Routines().ClaimRoutineScript(ctx, routineID, orchestration.DefaultBotID)
	if err != nil {
		return "", err
	}

	a, granted, err := w.svc.AskApproval(ctx, change(orchestration.NewApproval{RunKey: launch.Key}))
	if err != nil || granted {
		return "granted", err
	}

	asked <- a

	for {
		now, err := w.svc.GetApproval(ctx, a.ID)
		if err != nil {
			return "", err
		}

		if now.Status != orchestration.ApprovalPending {
			return string(now.Status), nil
		}

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(every):
		}
	}
}

// Scenario: an hourly script Routine made in a chat. At its time the
// launcher is run with the Bot's name and the Routine's id and nothing
// else; the run keeps its exit code and the end of its output, which is
// told in the chat the Routine was made in. A script that exits with an
// error fails the run and waits for the schedule; the second failure in a
// row tells the owner, in that chat and through the Bot's Lark Channel. A
// script stopped at its limit, and one that could not be run, may pass by
// themselves and are tried again early.
func TestAScriptRoutineRunsThroughTheLauncher(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	chat := w.chat()
	sync := w.hourly("sync", chat)

	w.runner.Tick(t.Context())
	assert.Empty(t, w.script.ran(), "not before its time")

	w.clock.add(time.Hour)
	w.runner.Tick(t.Context())

	run := w.ended(sync.ID, 1, routinestore.RunSucceeded)
	require.NotNil(t, run.ExitCode)
	assert.Zero(t, *run.ExitCode)
	assert.Equal(t, "3 rows written\n", run.Output)
	assert.Equal(t, [][]string{
		{"sudo", "-n", "-u", "coordinator", "/usr/local/bin/hypomnema-routine", orchestration.DefaultBotID, sync.ID},
	}, w.script.ran(), "the launcher gets a Bot's id and a Routine's id only")

	told := w.said(chat, 1)
	assert.Contains(t, told[0], `Routine "sync" ran`)
	assert.Contains(t, told[0], "3 rows written", "a script's output goes to the chat the routine was made in")

	w.script.then(exits("lark-cli: the Base refused\n", 1))
	w.clock.add(time.Hour)
	w.runner.Tick(t.Context())

	failed := w.ended(sync.ID, 2, routinestore.RunFailed)
	assert.Equal(t, "the script exited with code 1", failed.Error)
	assert.Equal(t, "lark-cli: the Base refused\n", failed.Output)
	assert.Equal(t, t0.Add(3*time.Hour), *w.routine(sync.ID).NextRunAt, "a script's own failure waits for the schedule")
	assert.Empty(t, w.lark.heard(), "one failure tells nobody")

	w.script.then(exits("", 124))
	w.clock.add(time.Hour)
	w.runner.Tick(t.Context())

	late := w.ended(sync.ID, 3, routinestore.RunFailed)
	assert.Equal(t, "the script was stopped at its limit of 10m0s", late.Error)
	assert.Equal(t, w.clock.Now().Add(time.Minute), *w.routine(sync.ID).NextRunAt,
		"a script that ran out of time may pass: it is tried again after the second step of the backoff")

	require.Eventually(t, func() bool { return len(w.lark.heard()) == 1 }, eventual, every)
	assert.Contains(t, w.lark.heard()[0], orchestration.DefaultBotID+"|"+chat+"|A routine keeps failing|")

	told = w.said(chat, 2)
	assert.Contains(t, told[1], `Routine "sync" of coordinator failed 2 times in a row`)
	assert.Contains(t, told[1], "stopped at its limit")

	w.script.then(func(context.Context) (string, int, error) { return "", 0, errors.New("sudo: a password is required") })
	w.clock.add(time.Minute)
	w.runner.Tick(t.Context())

	third := w.ended(sync.ID, 4, routinestore.RunFailed)
	assert.Contains(t, third.Error, "could not be run: sudo: a password is required")
	assert.Nil(t, third.ExitCode)
	assert.Equal(t, w.clock.Now().Add(5*time.Minute), *w.routine(sync.ID).NextRunAt)
	assert.Len(t, w.lark.heard(), 1, "the owner is told at most once an hour")
}

// A control plane that was given no command for scripts fails a script run
// with the name of the setting.
func TestAScriptRunFailsWhereNoCommandIsSet(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.runner = w.runnerWith()
	sync := w.hourly("sync", "")

	run, err := w.runner.RunNow(t.Context(), sync.ID)
	require.NoError(t, err)
	assert.Equal(t, routinestore.RunRunning, run.State)

	failed := w.ended(sync.ID, 1, routinestore.RunFailed)
	assert.Contains(t, failed.Error, "HYPOMNEMA_ROUTINE_COMMAND")
	assert.Empty(t, w.script.ran())
}

// runTokens makes a script run's token under the coordinator token named
// pi-coordinator, as the control plane does.
type runTokens struct{ svc *orchestration.Service }

func (r runTokens) AddRoutineToken(ctx context.Context, routineID string) (string, error) {
	return r.svc.AddRoutineToken(ctx, "pi-coordinator", routineID)
}

func (r runTokens) RevokeBotToken(secret string) { r.svc.RevokeBotToken(secret) }

// Scenario: a script runs with a token of its own (issue #211, after issue
// #260), given to the launcher first on its input. While the script runs,
// the token is its Bot's and its Routine's, in no chat; when the run ends,
// so does the token. Without a working coordinator token no token is made
// and the script does not run.
func TestAScriptRunHasATokenOfItsOwnThatEndsWithIt(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	_, _, err := w.svc.AddToken(ctx, "pi-coordinator", bearertoken.Coordinator)
	require.NoError(t, err)

	w.tokens = runTokens{svc: w.svc}
	w.runner = w.runnerWith("hypomnema-routine")
	sync := w.hourly("sync", w.chat())

	seen := make(chan orchestration.Token, 1)

	w.script.then(func(ctx context.Context) (string, int, error) {
		w.script.mu.Lock()
		token := w.script.tokens[len(w.script.tokens)-1]
		w.script.mu.Unlock()

		during, err := w.svc.AuthenticateToken(ctx, token)
		seen <- during

		return "", 0, err
	})

	_, err = w.runner.RunNow(ctx, sync.ID)
	require.NoError(t, err)
	w.ended(sync.ID, 1, routinestore.RunSucceeded)

	during := <-seen
	assert.Equal(t, bearertoken.Bot, during.Kind)
	assert.Equal(t, orchestration.DefaultBotID, during.Bot)
	assert.Equal(t, sync.ID, during.Routine)
	assert.Empty(t, during.Chat, "a script answers no chat")

	w.script.mu.Lock()
	given := w.script.tokens[0]
	w.script.mu.Unlock()

	_, err = w.svc.AuthenticateToken(ctx, given)
	require.ErrorIs(t, err, orchestration.ErrNotFound, "the token ends with its run")

	tokens, err := w.svc.ListTokens(ctx)
	require.NoError(t, err)
	require.NoError(t, w.svc.RevokeToken(ctx, tokens[0].ID))

	_, err = w.runner.RunNow(ctx, sync.ID)
	require.NoError(t, err)

	failed := w.ended(sync.ID, 2, routinestore.RunFailed)
	assert.Contains(t, failed.Error, "making the run's token failed")
	assert.Len(t, w.script.ran(), 1, "no script runs without its token")
}

// Scenario: a script's request needs the owner. The call waits where it is
// and the run waits for the owner; a run by hand meanwhile is told one is
// running. He approves: the same call goes on and the script ends, having
// been started once. In another run he says no: the script ends as it will,
// and the run is canceled, which is no failure. In a third nobody answers
// in time: the request is refused and the run fails.
func TestAScriptsRequestWaitsInPlaceForTheOwner(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	sync := w.hourly("sync", "")
	asked := make(chan orchestration.Approval, 1)

	asks := func(ctx context.Context) (string, int, error) {
		how, err := w.held(ctx, sync.ID, asked)

		switch {
		case err != nil:
			return "", 0, err
		case how == string(orchestration.ApprovalApproved):
			return "3 rows written\n", 0, nil
		}

		return "lark-cli: refused\n", 1, nil
	}

	w.script.then(asks)

	_, err := w.runner.RunNow(t.Context(), sync.ID)
	require.NoError(t, err)

	approval := <-asked
	waiting := w.ended(sync.ID, 1, routinestore.RunWaiting)

	_, err = w.runner.RunNow(t.Context(), sync.ID)
	require.ErrorIs(t, err, orchestration.ErrConflict)
	require.ErrorContains(t, err, waiting.ID+" of the routine is still waiting")

	_, err = w.svc.DecideApproval(t.Context(), approval.ID, true, orchestration.ScopeOnce)
	require.NoError(t, err)

	done := w.ended(sync.ID, 1, routinestore.RunSucceeded)
	assert.Equal(t, waiting.ID, done.ID)
	assert.Len(t, w.script.ran(), 1, "the script was started once: its call went on where it waited")
	assert.Zero(t, w.routine(sync.ID).Failures)

	w.script.then(asks)

	_, err = w.runner.RunNow(t.Context(), sync.ID)
	require.NoError(t, err)

	_, err = w.svc.DecideApproval(t.Context(), (<-asked).ID, false, "")
	require.NoError(t, err)

	stopped := w.ended(sync.ID, 2, routinestore.RunStopped)
	assert.Equal(t, `the owner rejected "Change a Base: usage"`, stopped.Error)
	assert.Zero(t, w.routine(sync.ID).Failures, "his no is no failure of the routine")

	w.script.then(asks)

	_, err = w.runner.RunNow(t.Context(), sync.ID)
	require.NoError(t, err)

	// The proxy's wait for the owner runs out.
	_, err = w.svc.ExpireApproval(t.Context(), (<-asked).ID)
	require.NoError(t, err)

	failed := w.ended(sync.ID, 3, routinestore.RunFailed)
	assert.Equal(t, `nobody answered the approval of "Change a Base: usage" in time`, failed.Error)
	assert.Equal(t, 1, w.routine(sync.ID).Failures)
	assert.Len(t, w.script.ran(), 3)
}

// Scenario: a daily prompt Routine. Its run is a new chat with the prompt
// waiting for the Bot; while the Bot works the run goes on. When the Bot has
// answered and stopped, the run succeeded with the answer, which goes to the
// chat the Routine was made in. A model call that failed for a passing
// reason fails the run and is tried again early. A Bot that still works
// after an hour is stopped.
func TestAPromptRoutineRunsInAChatOfItsOwn(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	home := w.chat()
	brief := w.daily("brief", home)

	w.clock.add(24 * time.Hour)
	w.runner.Tick(t.Context())

	run := w.ended(brief.ID, 1, routinestore.RunRunning)
	require.NotEmpty(t, run.ChatID)
	require.NotEqual(t, home, run.ChatID)

	w.runner.Follow(t.Context())
	w.ended(brief.ID, 1, routinestore.RunRunning)

	w.work(run.ChatID, true)
	w.says(run.ChatID, "Reading the repositories…")
	w.runner.Follow(t.Context())
	w.ended(brief.ID, 1, routinestore.RunRunning)

	w.says(run.ChatID, "Two pull requests were merged overnight.")
	w.work(run.ChatID, false)
	w.runner.Follow(t.Context())

	done := w.ended(brief.ID, 1, routinestore.RunSucceeded)
	assert.Equal(t, "Two pull requests were merged overnight.", done.Output)
	assert.Nil(t, done.ExitCode)

	announced := w.said(home, 1)
	assert.Contains(t, announced[0], `Routine "brief" ran`)
	assert.Contains(t, announced[0], "Two pull requests were merged overnight.")

	msgs, err := w.svc.ListChatMessages(t.Context(), home, 0, 0)
	require.NoError(t, err)
	assert.JSONEq(t, fmt.Sprintf(`[{"type": "routine_run", "id": %q, "routine_id": %q, "text": %q}]`,
		done.ID, brief.ID, announced[0]), string(msgs[len(msgs)-1].Parts), "the web draws the run's card from it")

	// The model's call fails for a reason that passes.
	second, err := w.runner.RunNow(t.Context(), brief.ID)
	require.NoError(t, err)
	require.NoError(t, w.svc.MarkDelivered(t.Context(), second.ChatID, 1<<40))

	_, err = w.svc.AddChatMessages(t.Context(), second.ChatID, orchestration.NewChatMessage{
		Role: orchestration.ChatSystem, Kind: orchestration.ChatFailed,
		Text: "pi's model call failed: 429 too many requests",
	})
	require.NoError(t, err)

	w.runner.Follow(t.Context())

	failed := w.ended(brief.ID, 2, routinestore.RunFailed)
	assert.Contains(t, failed.Error, "429 too many requests")
	assert.Equal(t, w.clock.Now().Add(30*time.Second), *w.routine(brief.ID).NextRunAt, "a rate limit is tried again early")
	w.said(home, 1)

	// The Bot never stops working.
	third, err := w.runner.RunNow(t.Context(), brief.ID)
	require.NoError(t, err)
	w.work(third.ChatID, true)

	w.clock.add(59 * time.Minute)
	w.runner.Follow(t.Context())
	w.ended(brief.ID, 3, routinestore.RunRunning)

	w.clock.add(2 * time.Minute)
	w.runner.Follow(t.Context())

	late := w.ended(brief.ID, 3, routinestore.RunFailed)
	assert.Equal(t, "stopped after 1h0m0s", late.Error)
	assert.Equal(t, []string{third.ChatID}, w.pi.stopped())
}

func TestReadRoutineResultStaysInItsRunChat(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()
	home := w.chat()
	brief := w.daily("private-brief", home)
	_, err := w.svc.Routines().SetRoutineAccess(ctx, brief.ID, routinestore.AccessRead)
	require.NoError(t, err)
	run, err := w.runner.RunNow(ctx, brief.ID)
	require.NoError(t, err)

	_, err = w.svc.Routines().SetRoutineAccess(ctx, brief.ID, routinestore.AccessFull)
	require.NoError(t, err)
	w.work(run.ChatID, true)
	w.says(run.ChatID, "A private result.")
	w.work(run.ChatID, false)
	w.runner.Follow(ctx)

	done := w.ended(brief.ID, 1, routinestore.RunSucceeded)
	assert.Empty(t, done.Output)
	assert.Equal(t, []string{"A private result."}, w.said(run.ChatID, 1))
	w.said(home, 0)
}

// Scenario: a prompt run's Bot asks for something the owner must approve.
// Its tool's call waits in place, so the Bot is still working and the run
// waits for the owner. He approves twenty minutes later: the run goes on, and
// ends with what the Bot says next. A second run waits past its hour, which
// counts from its start, the wait included (OpenClaw's wall-clock timeout):
// it is stopped and fails.
func TestAPromptRunWaitsForTheOwner(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	brief := w.daily("brief", "")

	run, err := w.runner.RunNow(t.Context(), brief.ID)
	require.NoError(t, err)

	require.NoError(t, w.svc.MarkDelivered(t.Context(), run.ChatID, 1<<40))
	w.work(run.ChatID, true)

	asked, granted, err := w.svc.AskApproval(t.Context(), change(orchestration.NewApproval{ChatID: run.ChatID}))
	require.NoError(t, err)
	require.False(t, granted)
	assert.Equal(t, run.ID, asked.RoutineRunID)

	w.runner.Follow(t.Context())
	w.ended(brief.ID, 1, routinestore.RunWaiting)

	w.clock.add(20 * time.Minute)
	w.runner.Follow(t.Context())
	w.ended(brief.ID, 1, routinestore.RunWaiting)
	assert.Empty(t, w.pi.stopped(), "within its hour it waits")

	_, err = w.svc.DecideApproval(t.Context(), asked.ID, true, orchestration.ScopeOnce)
	require.NoError(t, err)

	w.runner.Follow(t.Context())
	w.ended(brief.ID, 1, routinestore.RunRunning)

	w.says(run.ChatID, "The Base has the new rows.")
	w.work(run.ChatID, false)
	w.runner.Follow(t.Context())

	done := w.ended(brief.ID, 1, routinestore.RunSucceeded)
	assert.Equal(t, "The Base has the new rows.", done.Output)

	second, err := w.runner.RunNow(t.Context(), brief.ID)
	require.NoError(t, err)
	require.NoError(t, w.svc.MarkDelivered(t.Context(), second.ChatID, 1<<40))
	w.work(second.ChatID, true)

	_, _, err = w.svc.AskApproval(t.Context(), change(orchestration.NewApproval{ChatID: second.ChatID}))
	require.NoError(t, err)
	w.runner.Follow(t.Context())
	w.ended(brief.ID, 2, routinestore.RunWaiting)

	w.clock.add(time.Hour + time.Minute)
	w.runner.Follow(t.Context())

	late := w.ended(brief.ID, 2, routinestore.RunFailed)
	assert.Equal(t, "stopped after 1h0m0s", late.Error)
	assert.Equal(t, []string{second.ChatID}, w.pi.stopped(), "the wait counts toward its hour")
}

// Where nothing answers chats, a prompt run fails at once and says why.
func TestAPromptRunFailsWhereNothingAnswersChats(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.pi.off = true
	brief := w.daily("brief", "")

	_, err := w.runner.RunNow(t.Context(), brief.ID)
	require.NoError(t, err)

	w.runner.Follow(t.Context())
	assert.Contains(t, w.ended(brief.ID, 1, routinestore.RunFailed).Error, "HYPOMNEMA_COORDINATOR_COMMAND")
}

// Scenario: the owner removes a Routine whose script still runs: the script
// is told to stop. He undoes a Routine a Bot just made while its prompt run
// goes: the Bot's turn is aborted. Pausing lets a run finish.
func TestRemovingARoutineStopsItsRun(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	sync := w.hourly("sync", "")
	stopped := make(chan struct{})

	w.script.then(func(ctx context.Context) (string, int, error) {
		<-ctx.Done()
		close(stopped)

		return "", 143, nil
	})

	_, err := w.runner.RunNow(t.Context(), sync.ID)
	require.NoError(t, err)

	_, err = w.svc.Routines().PauseRoutine(t.Context(), sync.ID)
	require.NoError(t, err)

	select {
	case <-stopped:
		require.Fail(t, "pausing stopped the run")
	case <-time.After(20 * every):
	}

	require.NoError(t, w.runner.Remove(t.Context(), sync.ID))

	select {
	case <-stopped:
	case <-time.After(eventual):
		require.Fail(t, "the script of a removed routine was not stopped")
	}

	_, err = w.svc.Routines().GetRoutine(t.Context(), sync.ID)
	require.ErrorIs(t, err, orchestration.ErrNotFound)

	chat := w.chat()

	made, err := w.svc.Routines().PutRoutine(t.Context(), routinestore.RoutineSpec{
		Bot: orchestration.DefaultBotID, Name: "brief", Kind: routinestore.RoutinePrompt, Prompt: "Summarize.",
		EveryMinutes: 1440, ChatID: chat, Announce: true,
	}, routinestore.RoutineAuthor{Bot: true})
	require.NoError(t, err)

	run, err := w.runner.RunNow(t.Context(), made.ID)
	require.NoError(t, err)

	_, removed, err := w.runner.Undo(t.Context(), made.ID)
	require.NoError(t, err)
	assert.True(t, removed)
	assert.Equal(t, []string{run.ChatID}, w.pi.stopped())
}

// Scenario: the control plane restarts while a script runs, while a prompt
// run works and while another waits for the owner. Each is recorded as
// failed and is no failure of its Routine. The time of each counts as
// missed: each Routine is due at once, for its catch-up, and nothing runs
// before the start-up's catch-up begins it.
func TestARestartEndsTheRunsItInterrupted(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	sync := w.hourly("sync", "")
	brief := w.daily("brief", "")
	weekly := w.daily("weekly", "")

	// Started by the store alone: the control plane stopped before anything ran.
	_, err := w.svc.RunRoutineNow(t.Context(), sync.ID)
	require.NoError(t, err)

	_, err = w.svc.RunRoutineNow(t.Context(), brief.ID)
	require.NoError(t, err)

	waits, err := w.svc.RunRoutineNow(t.Context(), weekly.ID)
	require.NoError(t, err)

	_, _, err = w.svc.AskApproval(t.Context(), change(orchestration.NewApproval{ChatID: waits.Run.ChatID}))
	require.NoError(t, err)
	w.ended(weekly.ID, 1, routinestore.RunWaiting)

	w.clock.add(10 * time.Minute)
	w.runner.Interrupted(t.Context())

	for _, id := range []string{sync.ID, brief.ID, weekly.ID} {
		failed := w.ended(id, 1, routinestore.RunFailed)
		assert.Equal(t, "the control plane restarted while it ran", failed.Error)
		assert.Zero(t, w.routine(id).Failures, "an interrupted run is no failure of the routine")
		assert.Equal(t, w.clock.Now(), *w.routine(id).NextRunAt, "its missed time is caught up at once")
		assert.Equal(t, failed.ID, w.routine(id).ResumeRunID)
	}

	assert.Empty(t, w.script.ran(), "nothing runs before the catch-up")

	open, err := w.svc.ListApprovals(t.Context(), orchestration.ApprovalFilter{Status: orchestration.ApprovalPending})
	require.NoError(t, err)
	assert.Empty(t, open, "what the interrupted run asked is refused with it")
}

// prompt is what a prompt run's chat begins with: the prompt the Bot is
// given.
func (w *world) prompt(chatID string) string {
	w.t.Helper()

	msgs, err := w.svc.ListChatMessages(w.t.Context(), chatID, 0, 0)
	require.NoError(w.t, err)
	require.NotEmpty(w.t, msgs)

	return msgs[0].Text
}

// launched is a stand-in script that asks for its launch as the launcher
// does, and says whether it was told it catches up a cut run.
func (w *world) launched(routineID string, resumed chan<- bool) answer {
	return func(ctx context.Context) (string, int, error) {
		launch, err := w.svc.Routines().ClaimRoutineScript(ctx, routineID, orchestration.DefaultBotID)
		if err != nil {
			return "", 0, err
		}

		resumed <- launch.Resumed

		return "3 rows written\n", 0, nil
	}
}

// Scenario (issue #1170): the control plane starts and finds a prompt run
// and a script run left running. Each is failed, and each Routine gets one
// catch-up run in the start-up's catch-up: the prompt's chat tells the Bot
// that the run before was cut and to check what it did before doing it
// again; the script is told so by its launch. A Routine whose run was not
// cut is not told.
func TestARunARestartCutGetsOneCatchUpThatIsToldSo(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	sync := w.hourly("sync", "")
	brief := w.daily("brief", "")

	cutSync, err := w.svc.RunRoutineNow(t.Context(), sync.ID)
	require.NoError(t, err)

	cutBrief, err := w.svc.RunRoutineNow(t.Context(), brief.ID)
	require.NoError(t, err)

	resumed := make(chan bool, 2)
	w.script.then(w.launched(sync.ID, resumed))

	w.clock.add(10 * time.Minute)
	w.runner.Interrupted(t.Context())
	w.runner.CatchUp(t.Context())

	for _, id := range []string{sync.ID, brief.ID} {
		assert.Equal(t, "the control plane restarted while it ran", cutRun(t, w, id).Error)
	}

	catchUp := w.ended(brief.ID, 2, routinestore.RunRunning)
	assert.Equal(t, cutBrief.Run.ID, catchUp.ResumesRunID)
	assert.Equal(t, routinestore.StartedBySchedule, catchUp.StartedBy)
	assert.Contains(t, w.prompt(catchUp.ChatID), "Summarize what changed overnight.")
	assert.Contains(t, w.prompt(catchUp.ChatID), "The previous run of this routine ("+cutBrief.Run.ID+") was cut "+
		"off by a restart of the control plane before it ended. Check what it already did before you do any of it "+
		"again")
	assert.NotContains(t, w.prompt(cutBrief.Run.ChatID), "cut off by a restart", "the cut run was told nothing")
	assert.Empty(t, w.routine(brief.ID).ResumeRunID, "the catch-up is taken")
	assert.Equal(t, t0.Add(24*time.Hour), *w.routine(brief.ID).NextRunAt, "then its schedule")

	assert.True(t, <-resumed, "the script is told that it catches up a cut run")
	assert.Equal(t, cutSync.Run.ID, w.ended(sync.ID, 2, routinestore.RunSucceeded).ResumesRunID)
	assert.Len(t, w.script.ran(), 1, "one catch-up")

	// The next run is no catch-up and is told nothing.
	w.says(catchUp.ChatID, "Two pull requests were merged overnight.")
	w.runner.Follow(t.Context())
	w.ended(brief.ID, 2, routinestore.RunSucceeded)

	next, err := w.runner.RunNow(t.Context(), brief.ID)
	require.NoError(t, err)
	assert.Empty(t, next.ResumesRunID)
	assert.NotContains(t, w.prompt(next.ChatID), "cut off by a restart")

	w.script.then(w.launched(sync.ID, resumed))
	_, err = w.runner.RunNow(t.Context(), sync.ID)
	require.NoError(t, err)
	assert.False(t, <-resumed, "a run that catches up nothing is not told so")
}

// cutRun is a Routine's oldest run, the one the restart cut.
func cutRun(t *testing.T, w *world, routineID string) routinestore.RoutineRun {
	t.Helper()

	runs, err := w.svc.Routines().ListRoutineRuns(t.Context(), routineID, 0)
	require.NoError(t, err)

	return runs[len(runs)-1]
}

// Scenario (issue #1170): the control plane restarts in a loop. The first
// restart cuts a run and begins its catch-up; the second cuts the catch-up,
// which gets none of its own: the Routine waits for its schedule. A restart
// that comes before the catch-up began starts it once, not twice.
func TestTwoRestartsInARowStartOneCatchUpNotTwo(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	brief := w.daily("brief", "")

	_, err := w.svc.RunRoutineNow(t.Context(), brief.ID)
	require.NoError(t, err)

	w.clock.add(10 * time.Minute)
	w.runner.Interrupted(t.Context())
	w.runner.CatchUp(t.Context())
	w.ended(brief.ID, 2, routinestore.RunRunning)

	w.clock.add(time.Minute)

	again := w.runnerWith("sudo", "-n", "-u", "coordinator", "/usr/local/bin/hypomnema-routine")
	again.Interrupted(t.Context())
	again.CatchUp(t.Context())

	cut := w.ended(brief.ID, 2, routinestore.RunFailed)
	assert.Equal(t, "the control plane restarted while it ran", cut.Error)
	assert.NotEmpty(t, cut.ResumesRunID, "the cut run was the catch-up")
	assert.Empty(t, w.routine(brief.ID).ResumeRunID, "a cut catch-up gets none")
	assert.Equal(t, t0.Add(24*time.Hour), *w.routine(brief.ID).NextRunAt, "it waits for its schedule")

	again.CatchUp(t.Context())
	w.ended(brief.ID, 2, routinestore.RunFailed)

	// A restart before the catch-up began: the Routine still waits for it, and it begins once.
	sync := w.hourly("sync", "")
	_, err = w.svc.RunRoutineNow(t.Context(), sync.ID)
	require.NoError(t, err)

	w.runner.Interrupted(t.Context())
	again.Interrupted(t.Context())
	again.CatchUp(t.Context())
	w.runner.CatchUp(t.Context())

	w.ended(sync.ID, 2, routinestore.RunSucceeded)
	assert.Len(t, w.script.ran(), 1, "one catch-up, not two")
}

// Scenario: the control plane starts and finds seven Routines overdue. Five
// begin at once and the other two five seconds apart; each runs once.
func TestOverdueRoutinesBeginFiveAtOnceAndTheRestApart(t *testing.T) {
	t.Parallel()

	w := newWorld(t)

	ids := make([]string, 0, 7)
	for i := range 7 {
		ids = append(ids, w.hourly(fmt.Sprintf("sync-%d", i), "").ID)
	}

	w.clock.add(26 * time.Hour)
	w.runner.CatchUp(t.Context())

	assert.Equal(t, []time.Duration{5 * time.Second, 5 * time.Second}, w.waits)

	for _, id := range ids {
		w.ended(id, 1, routinestore.RunSucceeded)
	}

	assert.Len(t, w.script.ran(), 7, "each overdue routine runs once")

	w.runner.CatchUp(t.Context())
	assert.Len(t, w.script.ran(), 7)
}

// Scenario: a Routine fails ten times in a row. It is paused, and the owner
// is told so with the reason and how to switch it on again, in the chat the
// Routine was made in and through the Bot's Lark Channel.
func TestTheOwnerIsToldWhenARoutineIsPausedForItsFailures(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	chat := w.chat()
	sync := w.hourly("sync", chat)

	for i := range routinestore.RoutinePauseAfter {
		w.script.then(exits("the Base refused\n", 1))

		_, err := w.runner.RunNow(t.Context(), sync.ID)
		require.NoError(t, err)
		w.ended(sync.ID, i+1, routinestore.RunFailed)
	}

	require.Eventually(t, func() bool { return len(w.lark.heard()) == 2 }, eventual, every,
		"once for the failures, once for the pause")

	paused := w.lark.heard()[1]
	assert.Contains(t, paused, "|A routine was paused|")
	assert.Contains(t, paused, "paused after 10 failed runs in a row; the last: the script exited with code 1")
	assert.Contains(t, paused, "hypomnema routines resume "+sync.ID)

	told := w.said(chat, 2)
	assert.Contains(t, told[1], "was paused after 10 failed runs")
	assert.Equal(t, routinestore.RoutinePaused, w.routine(sync.ID).State)
}

// watched is an hourly prompt Routine whose script checks first whether the
// Bot has work (issue #656).
func (w *world) watched(name, chatID string) routinestore.Routine {
	w.t.Helper()

	r, err := w.svc.Routines().PutRoutine(w.t.Context(), routinestore.RoutineSpec{
		Bot: orchestration.DefaultBotID, Name: name, Kind: routinestore.RoutinePrompt,
		Prompt: "Adapt files/d9/bieu-do.json to the dashboard's change, then check the copy.",
		Script: "d9-check.sh", ScriptText: "#!/bin/sh\nhypomnema superset dashboard 9\n", EveryMinutes: 60,
		ChatID: chatID, Announce: true,
	}, owner)
	require.NoError(w.t, err)

	return r
}

// chats counts the chats there are.
func (w *world) chats() int {
	w.t.Helper()

	all, err := w.svc.ListChats(w.t.Context(), "", 0, 0)
	require.NoError(w.t, err)

	return len(all)
}

// Scenario: an hourly prompt Routine whose script checks the dashboard first
// (issue #656). The script runs through the launcher as a script Routine's
// does. When it prints nothing, and when its last line is {"wakeAgent":
// false}, the run succeeded with what it printed, no chat is made and
// nothing is told: no turn of the model was paid for.
func TestACheckThatFindsNothingWakesNoBot(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	home := w.chat()
	watch := w.watched("d9-watch", home)
	before := w.chats()

	w.script.then(exits("", 0))
	w.clock.add(time.Hour)
	w.runner.Tick(t.Context())

	quiet := w.ended(watch.ID, 1, routinestore.RunSucceeded)
	assert.Empty(t, quiet.ChatID, "no chat for a check that found nothing")
	require.NotNil(t, quiet.ExitCode)
	assert.Zero(t, *quiet.ExitCode)
	assert.Equal(t, [][]string{
		{"sudo", "-n", "-u", "coordinator", "/usr/local/bin/hypomnema-routine", orchestration.DefaultBotID, watch.ID},
	}, w.script.ran(), "the check runs through the launcher as a script does")

	said := "dashboard 9: 14 charts, unchanged since 08:00\n{\"wakeAgent\": false}\n\n"
	w.script.then(exits(said, 0))

	_, err := w.runner.RunNow(t.Context(), watch.ID)
	require.NoError(t, err)

	gated := w.ended(watch.ID, 2, routinestore.RunSucceeded)
	assert.Empty(t, gated.ChatID)
	assert.Equal(t, said, gated.Output, "what the check printed is kept on the run")

	w.runner.Follow(t.Context())
	assert.Equal(t, before, w.chats(), "no chat was made")
	w.said(home, 0)
	assert.Zero(t, w.routine(watch.ID).Failures)
}

// Scenario: the check finds a change and says so. A chat is made as for a
// prompt run, its prompt led by what the script printed under "## Script
// Output". From there the run is a prompt run: it goes on while the Bot
// works, its hour counts from the wake, and it ends with the Bot's answer,
// which is told where the Routine was made.
func TestACheckThatFindsWorkWakesTheBot(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	home := w.chat()
	watch := w.watched("d9-watch", home)

	held := make(chan struct{})

	w.script.then(func(context.Context) (string, int, error) {
		<-held

		return "chart 164 has a new column: region\n", 0, nil
	})

	_, err := w.runner.RunNow(t.Context(), watch.ID)
	require.NoError(t, err)

	w.clock.add(5 * time.Minute)
	w.runner.Follow(t.Context())
	checking := w.ended(watch.ID, 1, routinestore.RunRunning)
	assert.Empty(t, checking.ChatID, "while the check runs there is no chat to follow")

	close(held)

	var run routinestore.RoutineRun

	require.Eventually(t, func() bool {
		runs, err := w.svc.Routines().ListRoutineRuns(t.Context(), watch.ID, 0)
		if err != nil || len(runs) != 1 {
			return false
		}

		run = runs[0]

		return run.ChatID != ""
	}, eventual, every, "the check never woke the bot")
	assert.Equal(t, routinestore.RunRunning, run.State)

	msgs, err := w.svc.ListChatMessages(t.Context(), run.ChatID, 0, 0)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	prompt := msgs[0].Text
	assert.Contains(t, prompt, "## Script Output\n")
	assert.Contains(t, prompt, "```\nchart 164 has a new column: region\n```")
	assert.Less(t, strings.Index(prompt, "## Script Output"), strings.Index(prompt, "Adapt files/d9/bieu-do.json"),
		"what the script printed comes before the prompt")

	waiting, err := w.svc.Undelivered(t.Context(), run.ChatID)
	require.NoError(t, err)
	assert.Len(t, waiting, 1, "the prompt waits for the bot")

	w.work(run.ChatID, true)
	w.says(run.ChatID, "Reading the dashboard…")
	w.clock.add(58 * time.Minute)
	w.runner.Follow(t.Context())
	w.ended(watch.ID, 1, routinestore.RunRunning)
	assert.Empty(t, w.pi.stopped(), "the hour counts from the wake, not from the check's start")

	w.says(run.ChatID, "bieu-do.json has the region column; the copy matches.")
	w.work(run.ChatID, false)
	w.runner.Follow(t.Context())

	done := w.ended(watch.ID, 1, routinestore.RunSucceeded)
	assert.Equal(t, "bieu-do.json has the region column; the copy matches.", done.Output)
	assert.Contains(t, w.said(home, 1)[0], "the copy matches")
}

// Scenario: the check itself fails. The run fails as a script run fails, and
// the Bot is not woken to report it (unlike Hermes): a broken hourly check
// costs no turn. The second failure in a row tells the owner.
func TestAFailingCheckFailsWithoutAChat(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	home := w.chat()
	watch := w.watched("d9-watch", home)
	before := w.chats()

	for i := range 2 {
		w.script.then(exits("superset: 401 unauthorized\n", 2))

		_, err := w.runner.RunNow(t.Context(), watch.ID)
		require.NoError(t, err)

		failed := w.ended(watch.ID, i+1, routinestore.RunFailed)
		assert.Equal(t, "the script exited with code 2", failed.Error)
		assert.Empty(t, failed.ChatID)
	}

	assert.Equal(t, before, w.chats(), "no chat was made")
	assert.Equal(t, 2, w.routine(watch.ID).Failures)
	require.Eventually(t, func() bool { return len(w.lark.heard()) == 1 }, eventual, every)
	assert.Contains(t, w.lark.heard()[0], "|A routine keeps failing|")
}

// Scenario: a prompt run's Bot does its work with tools and ends its turn
// without a word. The run ends at once, succeeded with an empty answer, and
// nothing is told (OpenClaw, OpenMausBot); it no longer waits out its hour.
// A pi that exited during its turn has not ended it: that run still fails
// when its hour is up.
func TestABotTurnWithNoTextEndsTheRunAtOnce(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	home := w.chat()
	brief := w.daily("brief", home)

	run, err := w.runner.RunNow(t.Context(), brief.ID)
	require.NoError(t, err)
	require.NoError(t, w.svc.MarkDelivered(t.Context(), run.ChatID, 1<<40))

	_, err = w.svc.AddChatMessages(t.Context(), run.ChatID, orchestration.NewChatMessage{
		Role: orchestration.ChatTool, Text: "lark-cli base +record-batch-update: 3 records",
	})
	require.NoError(t, err)
	w.runner.Follow(t.Context())

	silent := w.ended(brief.ID, 1, routinestore.RunSucceeded)
	assert.Empty(t, silent.Output)
	w.said(home, 0)

	crashed, err := w.runner.RunNow(t.Context(), brief.ID)
	require.NoError(t, err)
	require.NoError(t, w.svc.MarkDelivered(t.Context(), crashed.ChatID, 1<<40))

	w.pi.mu.Lock()
	w.pi.exited[crashed.ChatID] = true
	w.pi.mu.Unlock()

	w.runner.Follow(t.Context())
	w.ended(brief.ID, 2, routinestore.RunRunning)

	w.clock.add(time.Hour + time.Minute)
	w.runner.Follow(t.Context())

	failed := w.ended(brief.ID, 2, routinestore.RunFailed)
	assert.Equal(t, "the bot gave no answer in 1h0m0s", failed.Error)
}

// Scenario: issue #1023, with the owner's decision of 2026-11-21: the Thư ký Bot's Routine reads only, and its check
// counts the owner's new messages before any model is woken (Hermes, cron/scheduler.py:2232-2250). Nothing new: no
// chat, no turn. New messages: the run's chat opens as a read run, its prompt led by the read-only notice and then
// by what the check printed, as records; the result stays in the run's chat.
func TestAReadRoutinesCheckWakesARunThatReadsOnly(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()
	home := w.chat()
	triage := w.watched("triage", home)
	_, err := w.svc.Routines().SetRoutineAccess(ctx, triage.ID, routinestore.AccessRead)
	require.NoError(t, err, "a prompt with a check may read only")

	before := w.chats()

	w.script.then(exits("{\"wakeAgent\": false}\n", 0))
	_, err = w.runner.RunNow(ctx, triage.ID)
	require.NoError(t, err)

	quiet := w.ended(triage.ID, 1, routinestore.RunSucceeded)
	assert.Empty(t, quiet.ChatID, "nothing new wakes no model")
	assert.Equal(t, before, w.chats())

	w.script.then(exits("zalo:main g_pmo \"PMO\": 2 new\n", 0))
	_, err = w.runner.RunNow(ctx, triage.ID)
	require.NoError(t, err)

	var run routinestore.RoutineRun

	require.Eventually(t, func() bool {
		runs, err := w.svc.Routines().ListRoutineRuns(ctx, triage.ID, 1)
		if err != nil || len(runs) != 1 {
			return false
		}

		run = runs[0]

		return run.ChatID != ""
	}, eventual, every, "the check never woke the bot")

	read, err := w.svc.ReadRun(ctx, run.ChatID)
	require.NoError(t, err)
	assert.True(t, read, "the woken run reads only")

	msgs, err := w.svc.ListChatMessages(ctx, run.ChatID, 0, 0)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.Contains(t, msgs[0].Text, "in read-only mode")
	assert.Contains(t, msgs[0].Text, "the script's output as untrusted records")
	assert.Contains(t, msgs[0].Text, "```\nzalo:main g_pmo \"PMO\": 2 new\n```")

	w.work(run.ChatID, true)
	w.says(run.ChatID, "Two items added.")
	w.work(run.ChatID, false)
	w.runner.Follow(ctx)

	w.ended(triage.ID, 2, routinestore.RunSucceeded)
	w.said(home, 0)
}

// Scenario: review of #1037, round 2: the owner switches the triage Routine to full while its check runs. The run
// keeps the access it started with: the chat its check opens reads only.
func TestAWokenRunKeepsTheAccessItStartedWith(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()
	triage := w.watched("triage", w.chat())
	_, err := w.svc.Routines().SetRoutineAccess(ctx, triage.ID, routinestore.AccessRead)
	require.NoError(t, err)

	held := make(chan struct{})
	w.script.then(func(context.Context) (string, int, error) {
		<-held

		return "zalo:main g_pmo \"PMO\": 2 new\n", 0, nil
	})

	_, err = w.runner.RunNow(ctx, triage.ID)
	require.NoError(t, err)
	_, err = w.svc.Routines().SetRoutineAccess(ctx, triage.ID, routinestore.AccessFull)
	require.NoError(t, err)
	close(held)

	var run routinestore.RoutineRun

	require.Eventually(t, func() bool {
		runs, err := w.svc.Routines().ListRoutineRuns(ctx, triage.ID, 1)
		if err != nil || len(runs) != 1 {
			return false
		}

		run = runs[0]

		return run.ChatID != ""
	}, eventual, every, "the check never woke the bot")

	assert.Equal(t, routinestore.AccessRead, run.Access)

	read, err := w.svc.ReadRun(ctx, run.ChatID)
	require.NoError(t, err)
	assert.True(t, read, "the chat reads only, as its run started")
}

// Scenario: issue #1071, finding 2 of the fourth review of #1037: a check woke the Bot, and while the Bot works the
// owner makes the Routine a script alone. The run still ends when the Bot has answered, as the prompt run it started
// as: it does not stay running and block every later run.
func TestAWokenRunEndsAsItStartedWhenTheRoutineBecomesAScript(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()
	home := w.chat()
	triage := w.watched("triage", home)

	w.script.then(exits("zalo:main g_pmo \"PMO\": 2 new\n", 0))
	_, err := w.runner.RunNow(ctx, triage.ID)
	require.NoError(t, err)

	var run routinestore.RoutineRun

	require.Eventually(t, func() bool {
		runs, err := w.svc.Routines().ListRoutineRuns(ctx, triage.ID, 1)
		if err != nil || len(runs) != 1 {
			return false
		}

		run = runs[0]

		return run.ChatID != ""
	}, eventual, every, "the check never woke the bot")

	_, err = w.svc.Routines().PutRoutine(ctx, routinestore.RoutineSpec{
		Bot: orchestration.DefaultBotID, Name: "triage", Kind: routinestore.RoutineScript, Script: "sync.sh",
		ScriptText: "#!/bin/sh\necho 3 rows written\n", EveryMinutes: 60, ChatID: home, Announce: true,
	}, owner)
	require.NoError(t, err)

	w.work(run.ChatID, true)
	w.says(run.ChatID, "Two items added.")
	w.work(run.ChatID, false)
	w.runner.Follow(ctx)

	ended := w.ended(triage.ID, 1, routinestore.RunSucceeded)
	assert.Equal(t, "Two items added.", ended.Output)
}

// Scenario: issue #1071, finding 2: the check of a Routine that reads only finds nothing new, and while it runs the
// owner makes the Routine a script alone that tells its chat what it printed. What the read check printed stays on
// its run: it is told nowhere, as the quiet check it started as.
func TestAQuietCheckStaysQuietWhenTheRoutineChangesMeanwhile(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()
	ended := endedTokens{revoked: make(chan struct{})}
	w.tokens = ended
	w.runner = w.runnerWith("hypomnema-routine")
	home := w.chat()
	triage := w.watched("triage", home)
	_, err := w.svc.Routines().SetRoutineAccess(ctx, triage.ID, routinestore.AccessRead)
	require.NoError(t, err)

	held := make(chan struct{})
	w.script.then(func(context.Context) (string, int, error) {
		<-held

		return "lark:me oc_1 \"Lan\": báo giá tháng 11\n{\"wakeAgent\": false}\n", 0, nil
	})

	_, err = w.runner.RunNow(ctx, triage.ID)
	require.NoError(t, err)

	_, err = w.svc.Routines().PutRoutine(ctx, routinestore.RoutineSpec{
		Bot: orchestration.DefaultBotID, Name: "triage", Kind: routinestore.RoutineScript, Access: routinestore.AccessFull,
		Script: "sync.sh", ScriptText: "#!/bin/sh\necho 3 rows written\n", EveryMinutes: 60, ChatID: home,
		Announce: true,
	}, owner)
	require.NoError(t, err)
	close(held)

	quiet := w.ended(triage.ID, 1, routinestore.RunSucceeded)
	assert.Empty(t, quiet.ChatID, "nothing new wakes no model")

	select {
	case <-ended.revoked:
	case <-time.After(eventual):
		require.FailNow(t, "the run's script never ended")
	}

	msgs, err := w.svc.ListChatMessages(ctx, home, 0, 0)
	require.NoError(t, err)
	assert.Empty(t, msgs, "what the read check printed is told nowhere")
}

// endedTokens gives a script run a token and says when the run is over: its token is revoked last, after the run was
// ended and told.
type endedTokens struct{ revoked chan struct{} }

func (endedTokens) AddRoutineToken(context.Context, string) (string, error) { return "hbt_run", nil }

func (e endedTokens) RevokeBotToken(string) { close(e.revoked) }
