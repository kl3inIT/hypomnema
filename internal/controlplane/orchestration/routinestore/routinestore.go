// Package routinestore keeps the Routines (issue #211), a domain store of the
// orchestration database reached through the root package's Routines(), as
// Omnara's storage facade reaches its stores (issue #404). It imports the
// generated store and never the root package; it only reads the tables of
// other domains (Bots, chats), through the shared queries. A write that spans
// another domain, such as a prompt run's chat, an approval that grants a
// Routine an action always, or removing a Bot with its chats, stays in the
// root and calls the functions here that end in Tx with its own queries.
//
// A Routine is work a Bot does on a schedule. What the work is, is not built
// in: a prompt the Bot works on with its model in a new chat, or a script
// that runs with the Bot's tools and no model. This package keeps the
// record: the Routine, its schedule, its runs and the grants the owner gave
// it. Running one is the routines package's.
//
// Each rule names the code it follows; the comparison is in
// docs/research/2026-11-12-work-on-a-schedule.md. OpenMausBot: the record,
// the schedule, an interval's grid, the count of skipped times, a Bot's
// change applied at once with an undo. Hermes: a script that is a file, the
// next time set before the run starts, a run that makes no Routines.
// OpenClaw: what a failure does to the next time, the alert and the pause,
// what is kept of the runs, the standing grant bound to the job's revision.
// MemoryOS: one live run kept by the database.
package routinestore

import (
	"cmp"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	// The time zones are in the binary: a Routine's cron expression names one,
	// and the server may carry none.
	_ "time/tzdata"

	"github.com/robfig/cron/v3"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/storeutil"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/storeerr"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

// RoutineKind says what a Routine runs.
type RoutineKind string

// The kinds of a Routine.
const (
	RoutinePrompt RoutineKind = "prompt"
	RoutineScript RoutineKind = "script"
)

// The access of a Routine's runs (issue #956): full, or read, which only the owner grants. A read Routine runs a
// prompt; its check script, when it has one, reads with the same rights as its runs (owner, 2026-11-21 on #1023,
// after Hermes's pre-run script, cron/scheduler.py:2232-2250).
const (
	AccessFull = "full"
	AccessRead = "read"
)

// RoutineState says whether a Routine runs.
type RoutineState string

// The states of a Routine.
const (
	RoutineOn     RoutineState = "on"
	RoutinePaused RoutineState = "paused"
)

// RunState is where a Routine's run stands.
type RunState string

// The states of a run.
const (
	RunRunning   RunState = "running"
	RunWaiting   RunState = "waiting"
	RunSucceeded RunState = "succeeded"
	RunFailed    RunState = "failed"
	RunStopped   RunState = "canceled"
)

// What started a run.
const (
	StartedBySchedule = "schedule"
	StartedByHand     = "manual"
)

// Limits of a Routine.
const (
	// MinRoutineMinutes and MaxRoutineMinutes bound "every N minutes"
	// (OpenMausBot, server/routine-schedules.ts L134-135).
	MinRoutineMinutes = 5
	MaxRoutineMinutes = 1440
	// ScriptTimeout is how long a script runs unless its Routine says
	// otherwise, and PromptTimeout how long a prompt run works (OpenClaw,
	// src/cron/service/timeout-policy.ts L10-31: 10 minutes, and 60 for an
	// agent turn).
	ScriptTimeout = 10 * time.Minute
	PromptTimeout = 60 * time.Minute
	// MaxScriptText bounds a script, as a chat message is bounded.
	MaxScriptText = storeutil.MaxChatText
	// RoutineAlertAfter is the failures in a row after which the owner is
	// told, at most once every RoutineAlertEvery (OpenClaw,
	// service/failure-alerts.ts L45-46), and RoutinePauseAfter those after
	// which the Routine is paused (service/auto-disable.ts L14).
	RoutineAlertAfter = 2
	RoutineAlertEvery = time.Hour
	RoutinePauseAfter = 10
	// RoutineQuickRetries is how many failures in a row of a passing error
	// are tried again earlier than the schedule's next time (OpenClaw,
	// service/timer-trigger.ts L168-185).
	RoutineQuickRetries = 3
	// RoutineRunsKept and RoutineRunsKeptFor are what is kept of a Routine's
	// runs (OpenClaw, store/run-history.kernel.ts L30-32).
	RoutineRunsKept    = 2000
	RoutineRunsKeptFor = 7 * 24 * time.Hour
	// RoutineRunPageMax bounds one listing of runs.
	RoutineRunPageMax = 200
	// MaxRunOutput is how much of the end of a run's output is kept.
	MaxRunOutput = 16 << 10
)

// routineBackoff is the least time between a failed run's end and the next
// run, by the failures in a row (OpenClaw, service/jobs-scheduling.ts,
// DEFAULT_ERROR_BACKOFF_SCHEDULE_MS). A success resets it.
var routineBackoff = []time.Duration{
	30 * time.Second, 60 * time.Second, 5 * time.Minute, 15 * time.Minute, 60 * time.Minute,
}

// routineWait is the wait after the n-th failure in a row, among the first of
// the steps (OpenClaw's errorBackoffMs: the index is held within the list).
func routineWait(failures int64, steps int) time.Duration {
	return routineBackoff[max(0, min(int(failures), steps)-1)]
}

// scriptPattern is a script's file name: one name, never a path or a command
// line (Hermes, cron/jobs_invariants.py L15-18: a file under scripts/, not a
// shell command line).
var scriptPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// cronParser reads a 5-field cron expression: minute, hour, day of month,
// month, day of week. No seconds, no @every, no zone inside the expression.
var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// Routine is work a Bot does on a schedule.
type Routine struct {
	ID     string
	Bot    string
	Name   string
	Kind   RoutineKind
	Access string
	// Prompt is what a prompt Routine gives its Bot.
	Prompt string
	// Script is the name of the file a script Routine runs, never a path;
	// ScriptText what the file holds, written for each run. A prompt
	// Routine may have one too: it runs first and says whether the Bot is
	// woken (issue #656).
	Script     string
	ScriptText string
	// EveryMinutes, or Cron with Timezone, is the schedule.
	EveryMinutes int
	Cron         string
	Timezone     string
	// Timeout is a script's limit; PromptTimeout for a prompt Routine
	// without one, whose turn has PromptTimeout whatever its script's limit.
	Timeout time.Duration
	// Revision counts the changes of what runs and when.
	Revision     int
	State        RoutineState
	PausedReason string
	// Summary is what the work is, in the words of whoever made it.
	Summary string
	// ChatID is the chat the Routine was made in; a run's result goes there
	// when Announce is set.
	ChatID   string
	Announce bool
	// NextRunAt is nil for a Routine that does not run.
	NextRunAt *time.Time
	// Failures counts the runs that failed in a row.
	Failures int
	// Skipped counts the due times that passed while a run was live.
	Skipped       int
	LastSkippedAt *time.Time
	CreatedBy     string
	// CanUndo says the last change was a Bot's and can be undone.
	CanUndo   bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// RoutineRun is one run of a Routine.
type RoutineRun struct {
	ID        string
	RoutineID string
	StartedBy string
	State     RunState
	StartedAt time.Time
	EndedAt   *time.Time
	// ExitCode is a script's; nil for a prompt run and a run that did not
	// start.
	ExitCode *int
	// Output is the end of what a script printed, or a prompt run's answer.
	Output string
	Error  string
	// ChatID is a prompt run's chat.
	ChatID string
	// Access is the Routine's when the run started: a read run's check and chat read only for the whole run,
	// whatever the Routine is changed to meanwhile (issue #1023, review of #1037).
	Access string
}

// RoutineSpec is what a Routine is made or changed with.
type RoutineSpec struct {
	Bot            string      `json:"-"`
	Name           string      `json:"-"`
	Kind           RoutineKind `json:"kind"`
	Access         string      `json:"access,omitempty"`
	Prompt         string      `json:"prompt,omitempty"`
	Script         string      `json:"script,omitempty"`
	ScriptText     string      `json:"script_text,omitempty"`
	EveryMinutes   int         `json:"every_minutes,omitempty"`
	Cron           string      `json:"cron,omitempty"`
	Timezone       string      `json:"timezone,omitempty"`
	TimeoutSeconds int         `json:"timeout_seconds,omitempty"`
	Summary        string      `json:"summary,omitempty"`
	ChatID         string      `json:"chat_id,omitempty"`
	Announce       bool        `json:"announce"`
}

// RoutineAuthor is who makes or changes a Routine.
type RoutineAuthor struct {
	// Bot says a Bot does, with its coordinator's token, from the chat the
	// spec names; otherwise the owner.
	Bot bool
	// FromRun says the request came from a Routine's own run.
	FromRun bool
}

// RoutineFilter selects Routines; empty fields match everything.
type RoutineFilter struct {
	Bot    string
	ChatID string
}

// RoutineGrant is one "always for this Routine": an action of a Connection
// that passes in the Routine's runs without asking.
type RoutineGrant struct {
	RoutineID   string
	Integration string
	Label       string
	Action      string
	ActionName  string
	ApprovalID  string
	CreatedAt   time.Time
}

// StartedRun is a run that began, with its Routine as it was then.
type StartedRun struct {
	Routine Routine
	Run     RoutineRun
}

// RunResult is how a run ended.
type RunResult struct {
	// State is succeeded, failed or canceled.
	State    RunState
	ExitCode *int
	Output   string
	Error    string
	// Passing says a failure is of a kind that passes by itself: a rate
	// limit, an overloaded or failing server, the network, a timeout.
	Passing bool
	// Interrupted says the run was ended by a restart of the control plane:
	// it is recorded as failed and is no failure of the Routine.
	Interrupted bool
}

// Settled is what a run's end did to its Routine.
type Settled struct {
	Routine Routine
	Run     RoutineRun
	ReadRun bool
	// Alert says the owner is to be told now of the failures in a row.
	Alert bool
	// Paused says the Routine was paused for its failures.
	Paused bool
}

// ScriptLaunch is what the launcher of a script Routine's run is given.
type ScriptLaunch struct {
	Script  string
	Text    string
	Timeout time.Duration
	// Key is the run's own: a request that presents it is this run's.
	Key string
}

// routineUndo is what undoes a Bot's last change of a Routine.
type routineUndo struct {
	// Applied is the Routine's updated_at when the change was applied: an
	// undo of a Routine changed since is refused.
	Applied int64 `json:"applied"`
	// Before is what the Routine was; nil for a Routine the Bot made.
	Before *RoutineSpec `json:"before,omitempty"`
}

// Store keeps the Routines.
type Store struct {
	db    *sql.DB
	clock platform.Clock
	newID func(prefix string) string
	// signal wakes the orchestration's event readers after each commit.
	signal func()
}

// New returns the Store over the orchestration database. newID and signal
// are the root's: ids are made and commits signaled as for every other
// write.
func New(db *sql.DB, clock platform.Clock, newID func(prefix string) string, signal func()) *Store {
	return &Store{db: db, clock: clock, newID: newID, signal: signal}
}

func (s *Store) write(ctx context.Context, fn func(q *store.Queries, now int64) error) error {
	return storeutil.Write(ctx, s.db, s.clock, s.signal, fn)
}

func (s *Store) read() *store.Queries {
	return store.New(s.db)
}

func nullString(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}

	return 0
}

func routineFromRow(r store.Routine) Routine {
	return Routine{
		ID: r.ID, Bot: r.BotID, Name: r.Name, Kind: RoutineKind(r.Kind), Access: r.Access, Prompt: r.Prompt, Script: r.Script,
		ScriptText: r.ScriptText, EveryMinutes: int(r.EveryMinutes.Int64), Cron: r.Cron.String,
		Timezone: r.Timezone.String, Timeout: time.Duration(r.TimeoutSeconds) * time.Second,
		Revision: int(r.Revision), State: RoutineState(r.State), PausedReason: r.PausedReason, Summary: r.Summary,
		ChatID: r.ChatID.String, Announce: r.Announce == 1, NextRunAt: storeutil.OptionalMillis(r.NextRunAt),
		Failures: int(r.Failures), Skipped: int(r.Skipped), LastSkippedAt: storeutil.OptionalMillis(r.LastSkippedAt),
		CreatedBy: r.CreatedBy, CanUndo: undoOf(r) != nil, CreatedAt: storeutil.Millis(r.CreatedAt),
		UpdatedAt: storeutil.Millis(r.UpdatedAt),
	}
}

// undoOf reads what undoes a Routine's last change; nil when nothing does,
// or when the Routine was changed since.
func undoOf(r store.Routine) *routineUndo {
	if !r.Undo.Valid {
		return nil
	}

	var u routineUndo
	if err := json.Unmarshal([]byte(r.Undo.String), &u); err != nil || u.Applied != r.UpdatedAt {
		return nil
	}

	return &u
}

func specOfRow(r store.Routine) RoutineSpec {
	return RoutineSpec{
		Bot: r.BotID, Name: r.Name, Kind: RoutineKind(r.Kind), Access: r.Access, Prompt: r.Prompt, Script: r.Script,
		ScriptText: r.ScriptText, EveryMinutes: int(r.EveryMinutes.Int64), Cron: r.Cron.String,
		Timezone: r.Timezone.String, TimeoutSeconds: int(r.TimeoutSeconds), Summary: r.Summary,
		ChatID: r.ChatID.String, Announce: r.Announce == 1,
	}
}

func routineRunFromRow(r store.RoutineRun) RoutineRun {
	out := RoutineRun{
		ID: r.ID, RoutineID: r.RoutineID, StartedBy: r.StartedBy, State: RunState(r.State),
		StartedAt: storeutil.Millis(r.StartedAt), EndedAt: storeutil.OptionalMillis(r.EndedAt),
		Output: r.Output, Error: r.Error, ChatID: r.ChatID.String, Access: r.Access,
	}

	if r.ExitCode.Valid {
		code := int(r.ExitCode.Int64)
		out.ExitCode = &code
	}

	return out
}

// ScheduleWords says a Routine's schedule as the owner reads it.
func ScheduleWords(r Routine) string {
	if r.Cron != "" {
		return cronWords(r.Cron, r.Timezone)
	}

	switch {
	case r.EveryMinutes == 60:
		return "every hour"
	case r.EveryMinutes == MaxRoutineMinutes:
		return "every day"
	case r.EveryMinutes%60 == 0:
		return fmt.Sprintf("every %d hours", r.EveryMinutes/60)
	}

	return fmt.Sprintf("every %d minutes", r.EveryMinutes)
}

// RoutineSentence restates a Routine in one sentence: its schedule and its
// work, done by the Bot called bot (its name, which may change; issue #466).
func RoutineSentence(r Routine, bot string) string {
	schedule := ScheduleWords(r)
	schedule = strings.ToUpper(schedule[:1]) + schedule[1:]

	work := "works on a prompt"

	switch {
	case r.Kind == RoutineScript:
		work = "runs the script " + r.Script
	case r.Script != "":
		work = "runs the script " + r.Script + " and works on a prompt when it says so"
	}

	if r.Summary != "" {
		work += ": " + strings.TrimSuffix(r.Summary, ".")
	}

	return fmt.Sprintf("%s, %s %s.", schedule, bot, work)
}

// schedule is a checked schedule.
type schedule struct {
	every time.Duration
	// anchor is an interval's first due time: its later times are on the
	// grid that starts there.
	anchor time.Time
	cron   cron.Schedule
	zone   *time.Location
}

func scheduleOf(everyMinutes int, expr, zone string) (schedule, error) {
	switch {
	case everyMinutes != 0 && expr != "":
		return schedule{}, fmt.Errorf("%w: a schedule is every N minutes or a cron expression, not both", storeerr.ErrInvalid)
	case expr == "" && zone != "":
		return schedule{}, fmt.Errorf("%w: a time zone goes with a cron expression", storeerr.ErrInvalid)
	case expr == "":
		if everyMinutes < MinRoutineMinutes || everyMinutes > MaxRoutineMinutes {
			return schedule{}, fmt.Errorf("%w: a Routine runs every %d to %d minutes, or by a cron expression",
				storeerr.ErrInvalid, MinRoutineMinutes, MaxRoutineMinutes)
		}

		return schedule{every: time.Duration(everyMinutes) * time.Minute}, nil
	case zone == "":
		return schedule{}, fmt.Errorf("%w: a cron expression needs its time zone, such as Asia/Ho_Chi_Minh",
			storeerr.ErrInvalid)
	case len(strings.Fields(expr)) != 5 || strings.Contains(expr, "="):
		return schedule{}, fmt.Errorf("%w: a cron expression has five fields: minute, hour, day of month, month, "+
			"day of week", storeerr.ErrInvalid)
	}

	// "Local" would be the server's zone, whichever that is.
	loc, err := time.LoadLocation(zone)
	if err != nil || zone == "Local" {
		return schedule{}, fmt.Errorf("%w: %q is not an IANA time zone", storeerr.ErrInvalid, zone)
	}

	parsed, err := cronParser.Parse(expr)
	if err != nil {
		return schedule{}, fmt.Errorf("%w: the cron expression %q: %s", storeerr.ErrInvalid, expr, err.Error())
	}

	if parsed.Next(time.Unix(0, 0).In(loc)).IsZero() {
		return schedule{}, fmt.Errorf("%w: the cron expression %q never comes", storeerr.ErrInvalid, expr)
	}

	return schedule{cron: parsed, zone: loc}, nil
}

// next is the first time after t the schedule comes. An interval stays on
// the grid of its anchor whatever happened in between (OpenMausBot,
// server/routine-intervals.ts L40-46, nextAlignedInterval).
func (s schedule) next(t time.Time) time.Time {
	if s.cron != nil {
		return s.cron.Next(t.In(s.zone)).UTC()
	}

	if s.anchor.After(t) {
		return s.anchor
	}

	elapsed := int64(t.Sub(s.anchor) / s.every)

	return s.anchor.Add(time.Duration(elapsed+1) * s.every)
}

// notBefore is the schedule's first time at or after t.
func (s schedule) notBefore(t time.Time) time.Time {
	return s.next(t.Add(-time.Millisecond))
}

func scheduleOfRow(r store.Routine) (schedule, error) {
	s, err := scheduleOf(int(r.EveryMinutes.Int64), r.Cron.String, r.Timezone.String)
	if err != nil {
		return s, err
	}

	if r.AnchorAt.Valid {
		s.anchor = storeutil.Millis(r.AnchorAt.Int64)
	}

	return s, nil
}

// checkRoutine returns a spec with its texts trimmed and its limit filled
// in, and its schedule.
func checkRoutine(n RoutineSpec) (RoutineSpec, schedule, error) {
	n.Summary = strings.Join(strings.Fields(n.Summary), " ")
	n.Cron = strings.Join(strings.Fields(n.Cron), " ")

	sched, err := scheduleOf(n.EveryMinutes, n.Cron, n.Timezone)

	switch {
	case n.Access != "" && n.Access != AccessFull && n.Access != AccessRead:
		return n, sched, fmt.Errorf("%w: routine access is full or read", storeerr.ErrInvalid)
	case n.Access == AccessRead && n.Kind != RoutinePrompt:
		return n, sched, fmt.Errorf("%w: a read-only routine runs a prompt, with or without a check", storeerr.ErrInvalid)
	case !storeutil.NamePattern.MatchString(n.Name):
		return n, sched, fmt.Errorf("%w: a routine's name is lowercase letters, digits and dashes, starting with a "+
			"letter", storeerr.ErrInvalid)
	case err != nil:
		return n, sched, err
	case len(n.Summary) > 500:
		return n, sched, fmt.Errorf("%w: what the work is, is said in at most 500 characters", storeerr.ErrInvalid)
	case n.TimeoutSeconds < 0:
		return n, sched, fmt.Errorf("%w: a limit is a number of seconds", storeerr.ErrInvalid)
	}

	switch n.Kind {
	case RoutinePrompt:
		n.Prompt = strings.TrimSpace(n.Prompt)

		switch {
		case n.Prompt == "":
			return n, sched, fmt.Errorf("%w: a prompt routine needs its prompt", storeerr.ErrInvalid)
		case len(n.Prompt) > storeutil.MaxChatText:
			return n, sched, fmt.Errorf("%w: a prompt is at most %d bytes", storeerr.ErrInvalid, storeutil.MaxChatText)
		case n.Script == "" && n.ScriptText == "" && n.TimeoutSeconds != 0:
			return n, sched, fmt.Errorf("%w: a prompt run works for at most %s; only a script's limit is set",
				storeerr.ErrInvalid, PromptTimeout)
		case n.Script == "" && n.ScriptText == "":
			n.TimeoutSeconds = int(PromptTimeout / time.Second)

			return n, sched, nil
		}

		// A prompt Routine with a script: the script checks first whether the Bot is woken (issue #656, Hermes's
		// script before the agent, cron/scheduler.py L2264-2290). The limit is the script's.
		if err := checkScript(&n); err != nil {
			return n, sched, err
		}
	case RoutineScript:
		if err := checkScript(&n); err != nil {
			return n, sched, err
		}

		if n.Prompt != "" {
			return n, sched, fmt.Errorf("%w: a script routine runs its script alone; a prompt routine may have a "+
				"script that checks first", storeerr.ErrInvalid)
		}
	default:
		return n, sched, fmt.Errorf("%w: a routine runs a prompt or a script", storeerr.ErrInvalid)
	}

	return n, sched, nil
}

// checkScript checks a spec's script and fills in its limit.
func checkScript(n *RoutineSpec) error {
	if n.TimeoutSeconds == 0 {
		n.TimeoutSeconds = int(ScriptTimeout / time.Second)
	}

	switch {
	case !scriptPattern.MatchString(n.Script):
		return fmt.Errorf("%w: a script is named by its file under the bot's routines directory, a name of "+
			"letters, digits, dots, dashes and underscores; never a path or a command line", storeerr.ErrInvalid)
	case strings.TrimSpace(n.ScriptText) == "":
		return fmt.Errorf("%w: a script routine needs the script's text", storeerr.ErrInvalid)
	case len(n.ScriptText) > MaxScriptText:
		return fmt.Errorf("%w: a script is at most %d bytes", storeerr.ErrInvalid, MaxScriptText)
	}

	return nil
}

func nullInt(n int) sql.NullInt64 { return sql.NullInt64{Int64: int64(n), Valid: n != 0} }

func nullTime(t time.Time) sql.NullInt64 { return sql.NullInt64{Int64: t.UnixMilli(), Valid: true} }

// sameWork reports whether two specs run the same thing at the same times:
// what an "always for this Routine" was given for.
func sameWork(a, b RoutineSpec) bool {
	return a.Kind == b.Kind && a.Prompt == b.Prompt && a.Script == b.Script && a.ScriptText == b.ScriptText &&
		sameSchedule(a, b)
}

func sameSchedule(a, b RoutineSpec) bool {
	return a.EveryMinutes == b.EveryMinutes && a.Cron == b.Cron && a.Timezone == b.Timezone
}

// PutRoutine makes a Routine of a Bot, switched on at once, or changes what
// the one of that name runs, when, and where its result goes. The owner
// does, and a Bot from its chat, whose change can be undone (OpenMausBot,
// server/routine-requests.ts L962-966: a bot's change applies at once and is
// recorded with its undo). A Routine's run makes and changes none (Hermes,
// cron/scheduler.py L416-428). A changed schedule starts from now; a change
// of what runs or when ends what the Routine was allowed always; its state
// and its runs stay.
func (s *Store) PutRoutine(ctx context.Context, n RoutineSpec, by RoutineAuthor) (Routine, error) {
	n, sched, err := checkRoutine(n)
	if err != nil {
		return Routine{}, err
	}

	switch {
	case by.FromRun:
		return Routine{}, fmt.Errorf("%w: a routine's run cannot make or change routines", storeerr.ErrConflict)
	case by.Bot && n.Access != "":
		return Routine{}, fmt.Errorf("%w: only the owner sets routine access", storeerr.ErrInvalid)
	case by.Bot && n.ChatID == "":
		return Routine{}, fmt.Errorf("%w: a bot makes a routine from the chat it works in", storeerr.ErrInvalid)
	}

	var id string

	err = s.write(ctx, func(q *store.Queries, now int64) error {
		if err := routineRefs(ctx, q, n, by); err != nil {
			return err
		}

		row, err := q.GetRoutineByName(ctx, store.GetRoutineByNameParams{Bot: n.Bot, Name: n.Name})
		if errors.Is(err, sql.ErrNoRows) {
			id = s.newID("rtn")
			if n.Access == "" {
				n.Access = AccessFull
			}

			return insertRoutine(ctx, q, now, id, n, sched, by)
		}

		if err != nil {
			return fmt.Errorf("reading routine %s of bot %s: %w", n.Name, n.Bot, err)
		}

		id = row.ID
		if n.Access == "" {
			n.Access = row.Access
			if by.Bot && !sameWork(specOfRow(row), n) {
				n.Access = AccessFull
			}
			if n.Access == AccessRead && n.Kind != RoutinePrompt {
				return fmt.Errorf("%w: set access to full before making it a script alone", storeerr.ErrInvalid)
			}
		}

		return updateRoutine(ctx, q, now, row, n, sched, by.Bot)
	})
	if err != nil {
		return Routine{}, fmt.Errorf("saving routine %s of bot %s: %w", n.Name, n.Bot, err)
	}

	return s.GetRoutine(ctx, id)
}

func (s *Store) SetRoutineAccess(ctx context.Context, id, access string) (Routine, error) {
	err := s.write(ctx, func(q *store.Queries, now int64) error {
		row, err := q.GetRoutine(ctx, id)
		if err != nil {
			return storeutil.NotFound(err, "routine", id)
		}

		switch {
		case access != AccessRead && access != AccessFull:
			return fmt.Errorf("%w: routine access is full or read", storeerr.ErrInvalid)
		case access == AccessRead && row.Kind != string(RoutinePrompt):
			return fmt.Errorf("%w: a read-only routine runs a prompt, with or without a check", storeerr.ErrInvalid)
		case access == row.Access:
			return nil
		}

		return q.SetRoutineAccess(ctx, store.SetRoutineAccessParams{ID: id, Access: access, Now: now})
	})
	if err != nil {
		return Routine{}, err
	}

	return s.GetRoutine(ctx, id)
}

// routineRefs checks that a spec's Bot and chat exist, and for a Bot that
// the chat is one it talks in and not a Routine's run.
func routineRefs(ctx context.Context, q *store.Queries, n RoutineSpec, by RoutineAuthor) error {
	if _, err := q.GetBot(ctx, n.Bot); err != nil {
		return storeutil.NotFound(err, "bot", n.Bot)
	}

	if n.ChatID == "" {
		return nil
	}

	chat, err := q.GetChat(ctx, n.ChatID)
	if err != nil {
		return storeutil.NotFound(err, "chat", n.ChatID)
	}

	if !by.Bot {
		return nil
	}

	if chat.BotID != n.Bot {
		return fmt.Errorf("%w: chat %s talks with bot %s, not %s", storeerr.ErrInvalid, n.ChatID, chat.BotID, n.Bot)
	}

	if _, err := q.RoutineRunOfChat(ctx, nullString(n.ChatID)); err == nil {
		return fmt.Errorf("%w: a routine's run cannot make or change routines", storeerr.ErrConflict)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("reading the run of chat %s: %w", n.ChatID, err)
	}

	return nil
}

// undoJSON is what a Bot's change records so that it can be undone.
func undoJSON(now int64, before *RoutineSpec) (sql.NullString, error) {
	raw, err := json.Marshal(routineUndo{Applied: now, Before: before})
	if err != nil {
		return sql.NullString{}, fmt.Errorf("recording the undo: %w", err)
	}

	return sql.NullString{String: string(raw), Valid: true}, nil
}

func insertRoutine(ctx context.Context, q *store.Queries, now int64, id string, n RoutineSpec, sched schedule,
	by RoutineAuthor,
) error {
	var (
		anchor sql.NullInt64
		undo   sql.NullString
		author = "owner"
	)

	if sched.cron == nil {
		sched.anchor = storeutil.Millis(now).Add(sched.every)
		anchor = nullTime(sched.anchor)
	}

	if by.Bot {
		var err error

		if undo, err = undoJSON(now, nil); err != nil {
			return err
		}

		author = "bot"
	}

	return q.InsertRoutine(ctx, store.InsertRoutineParams{
		ID: id, Bot: n.Bot, Name: n.Name, Kind: string(n.Kind), Prompt: n.Prompt, Script: n.Script,
		ScriptText: n.ScriptText, EveryMinutes: nullInt(n.EveryMinutes), AnchorAt: anchor, Cron: nullString(n.Cron),
		Timezone: nullString(n.Timezone), TimeoutSeconds: int64(n.TimeoutSeconds), Summary: n.Summary,
		ChatID: nullString(n.ChatID), Announce: boolInt(n.Announce), NextRunAt: nullTime(sched.next(storeutil.Millis(now))),
		CreatedBy: author, Undo: undo, Now: now, Access: n.Access,
	})
}

// updateRoutine changes a Routine to a spec. byBot records what undoes it.
func updateRoutine(ctx context.Context, q *store.Queries, now int64, row store.Routine, n RoutineSpec, sched schedule,
	byBot bool,
) error {
	before := specOfRow(row)
	p := store.UpdateRoutineParams{
		ID: row.ID, Kind: string(n.Kind), Prompt: n.Prompt, Script: n.Script, ScriptText: n.ScriptText,
		EveryMinutes: nullInt(n.EveryMinutes), AnchorAt: row.AnchorAt, Cron: nullString(n.Cron),
		Timezone: nullString(n.Timezone), TimeoutSeconds: int64(n.TimeoutSeconds), Revision: row.Revision,
		Summary: n.Summary, ChatID: nullString(n.ChatID), Announce: boolInt(n.Announce), NextRunAt: row.NextRunAt,
		Now: now, Access: n.Access,
	}

	if !sameSchedule(before, n) {
		p.AnchorAt = sql.NullInt64{}

		if sched.cron == nil {
			sched.anchor = storeutil.Millis(now).Add(sched.every)
			p.AnchorAt = nullTime(sched.anchor)
		}

		if row.State == string(RoutineOn) {
			p.NextRunAt = nullTime(sched.next(storeutil.Millis(now)))
		}
	}

	if !sameWork(before, n) {
		p.Revision++

		if err := q.DeleteStaleRoutineGrants(ctx, store.DeleteStaleRoutineGrantsParams{
			RoutineID: row.ID, Revision: p.Revision,
		}); err != nil {
			return fmt.Errorf("ending the grants of routine %s: %w", row.ID, err)
		}
	}

	if byBot {
		var err error

		if p.Undo, err = undoJSON(now, &before); err != nil {
			return err
		}
	}

	return q.UpdateRoutine(ctx, p)
}

// UndoRoutine undoes a Bot's last change of a Routine: one it made is
// removed, one it changed is what it was before. A Routine changed since is
// not undone (OpenMausBot, server/routine-requests.ts L1099-1147). removed
// says the Routine is gone, with the run of it that was live, if one was.
func (s *Store) UndoRoutine(ctx context.Context, id string) (r Routine, removed bool, live *RoutineRun, err error) {
	err = s.write(ctx, func(q *store.Queries, now int64) error {
		row, err := q.GetRoutine(ctx, id)
		if err != nil {
			return storeutil.NotFound(err, "routine", id)
		}

		undo := undoOf(row)

		switch {
		case !row.Undo.Valid:
			return fmt.Errorf("%w: the last change of routine %s was not a bot's; there is nothing to undo",
				storeerr.ErrConflict, id)
		case undo == nil:
			return fmt.Errorf("%w: routine %s was changed since; undo it by hand", storeerr.ErrConflict, id)
		case undo.Before == nil:
			removed, r = true, routineFromRow(row)
			live, err = liveRun(ctx, q, id)

			return errors.Join(err, q.DeleteRoutine(ctx, id))
		}

		before := *undo.Before
		before.Bot, before.Name = row.BotID, row.Name
		if before.Access == "" {
			before.Access = row.Access
		}

		sched, err := scheduleOf(before.EveryMinutes, before.Cron, before.Timezone)
		if err != nil {
			return err
		}

		return updateRoutine(ctx, q, now, row, before, sched, false)
	})
	if err != nil {
		return Routine{}, false, nil, fmt.Errorf("undoing the change of routine %s: %w", id, err)
	}

	if removed {
		return r, true, live, nil
	}

	r, err = s.GetRoutine(ctx, id)

	return r, false, nil, err
}

// GetRoutine returns one Routine.
func (s *Store) GetRoutine(ctx context.Context, id string) (Routine, error) {
	row, err := s.read().GetRoutine(ctx, id)
	if err != nil {
		return Routine{}, storeutil.NotFound(err, "routine", id)
	}

	return routineFromRow(row), nil
}

// ListRoutines returns the Routines, the oldest first.
func (s *Store) ListRoutines(ctx context.Context, f RoutineFilter) ([]Routine, error) {
	rows, err := s.read().ListRoutines(ctx, store.ListRoutinesParams{Bot: f.Bot, ChatID: f.ChatID})
	if err != nil {
		return nil, fmt.Errorf("listing routines: %w", err)
	}

	out := make([]Routine, len(rows))
	for i, r := range rows {
		out[i] = routineFromRow(r)
	}

	return out, nil
}

// liveRun is the run of a Routine that still goes or waits; nil when none.
func liveRun(ctx context.Context, q *store.Queries, routineID string) (*RoutineRun, error) {
	row, err := q.LiveRunOfRoutine(ctx, routineID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // No live run is an answer, not a failure.
	}

	if err != nil {
		return nil, fmt.Errorf("reading the live run of routine %s: %w", routineID, err)
	}

	run := routineRunFromRow(row)

	return &run, nil
}

// DeleteRoutine removes a Routine with its runs and its grants, and returns
// the run of it that was live, for whoever runs it to stop (OpenClaw,
// service/ops-mutations.ts L427-456: removing a job cancels its active run).
func (s *Store) DeleteRoutine(ctx context.Context, id string) (*RoutineRun, error) {
	var live *RoutineRun

	err := s.write(ctx, func(q *store.Queries, _ int64) error {
		if _, err := q.GetRoutine(ctx, id); err != nil {
			return storeutil.NotFound(err, "routine", id)
		}

		var err error

		if live, err = liveRun(ctx, q, id); err != nil {
			return err
		}

		return q.DeleteRoutine(ctx, id)
	})
	if err != nil {
		return nil, fmt.Errorf("removing routine %s: %w", id, err)
	}

	return live, nil
}

// PauseRoutine stops a Routine's schedule; a run still going goes on.
func (s *Store) PauseRoutine(ctx context.Context, id string) (Routine, error) {
	return s.switchRoutine(ctx, id, RoutinePaused, "paused by the owner")
}

// ResumeRoutine switches a paused Routine on again, from now, with its
// failures forgotten.
func (s *Store) ResumeRoutine(ctx context.Context, id string) (Routine, error) {
	return s.switchRoutine(ctx, id, RoutineOn, "")
}

func (s *Store) switchRoutine(ctx context.Context, id string, to RoutineState, reason string) (Routine, error) {
	err := s.write(ctx, func(q *store.Queries, now int64) error {
		row, err := q.GetRoutine(ctx, id)
		if err != nil {
			return storeutil.NotFound(err, "routine", id)
		}

		if row.State == string(to) {
			return nil
		}

		p := store.SwitchRoutineParams{ID: id, State: string(to), PausedReason: reason, Now: now}

		if to == RoutineOn {
			sched, err := scheduleOfRow(row)
			if err != nil {
				return err
			}

			p.NextRunAt = nullTime(sched.next(storeutil.Millis(now)))
		} else {
			p.Failures, p.AlertedAt = row.Failures, row.AlertedAt
		}

		return q.SwitchRoutine(ctx, p)
	})
	if err != nil {
		return Routine{}, fmt.Errorf("switching routine %s: %w", id, err)
	}

	return s.GetRoutine(ctx, id)
}

// GetRoutineTx reads one Routine with the caller's queries.
func GetRoutineTx(ctx context.Context, q *store.Queries, id string) (Routine, error) {
	row, err := q.GetRoutine(ctx, id)
	if err != nil {
		return Routine{}, storeutil.NotFound(err, "routine", id)
	}

	return routineFromRow(row), nil
}

// LiveRunTx is the run of a Routine that still goes or waits, read with the
// caller's queries; nil when none.
func LiveRunTx(ctx context.Context, q *store.Queries, routineID string) (*RoutineRun, error) {
	return liveRun(ctx, q, routineID)
}

// StartRunTx records a run of a Routine that has none live, with the
// caller's queries. A prompt run's chat, which chatID names, was opened by
// the caller in the same transaction; chatID is empty for a script's run
// and for a prompt run whose script checks first (OpenRunChatTx).
func StartRunTx(ctx context.Context, q *store.Queries, now int64, r Routine, id, by, chatID string,
) (StartedRun, error) {
	if err := q.InsertRoutineRun(ctx, store.InsertRoutineRunParams{
		ID: id, RoutineID: r.ID, StartedBy: by, Now: now, ChatID: nullString(chatID), Access: cmp.Or(r.Access, AccessFull),
	}); err != nil {
		return StartedRun{}, fmt.Errorf("recording a run of routine %s: %w", r.ID, err)
	}

	run, err := q.GetRoutineRun(ctx, id)
	if err != nil {
		return StartedRun{}, fmt.Errorf("reading run %s: %w", id, err)
	}

	return StartedRun{Routine: r, Run: routineRunFromRow(run)}, nil
}

// OpenRunChatTx gives a live run the chat the caller opened for it in the
// same transaction: a prompt run whose script said to wake the Bot (issue
// #656). A run that ended, or has a chat, is a conflict.
func OpenRunChatTx(ctx context.Context, q *store.Queries, runID, chatID string) (StartedRun, error) {
	run, err := q.GetRoutineRun(ctx, runID)
	if err != nil {
		return StartedRun{}, storeutil.NotFound(err, "routine run", runID)
	}

	n, err := q.OpenRoutineRunChat(ctx, store.OpenRoutineRunChatParams{ID: runID, ChatID: nullString(chatID)})
	if err != nil {
		return StartedRun{}, fmt.Errorf("opening the chat of run %s: %w", runID, err)
	}

	if n != 1 {
		return StartedRun{}, fmt.Errorf("%w: run %s has ended or has its chat", storeerr.ErrConflict, runID)
	}

	routine, err := GetRoutineTx(ctx, q, run.RoutineID)
	if err != nil {
		return StartedRun{}, err
	}

	run.ChatID = nullString(chatID)

	return StartedRun{Routine: routine, Run: routineRunFromRow(run)}, nil
}

// ClaimDueTx returns the Routines whose time has come and whose run is to
// begin now, with the caller's queries, which then starts each with
// StartRunTx. A Routine's next time is set before its run starts, in the
// same transaction, so a crash does not run it twice (Hermes, cron/jobs.py
// L2701-2704). A Routine found overdue runs once: the times it missed are
// not replayed. A time that comes while a run of the Routine is live is
// skipped and counted on the Routine, with no run of its own (OpenClaw,
// service/timer-runnable.ts L83-88; the count: OpenMausBot,
// server/routines.ts L1059-1060). With a limit above zero at most that many
// are returned, and more says Routines are still due.
func ClaimDueTx(ctx context.Context, q *store.Queries, now int64, limit int) (due []Routine, more bool, err error) {
	rows, err := q.DueRoutines(ctx, sql.NullInt64{Int64: now, Valid: true})
	if err != nil {
		return nil, false, fmt.Errorf("listing the routines that are due: %w", err)
	}

	for _, row := range rows {
		sched, err := scheduleOfRow(row)
		if err != nil {
			return nil, false, fmt.Errorf("routine %s: %w", row.ID, err)
		}

		next := nullTime(sched.next(storeutil.Millis(now)))

		live, err := liveRun(ctx, q, row.ID)
		if err != nil {
			return nil, false, err
		}

		if live != nil {
			if err := q.SkipRoutineTime(ctx, store.SkipRoutineTimeParams{
				ID: row.ID, NextRunAt: next, SkippedAt: row.NextRunAt,
			}); err != nil {
				return nil, false, fmt.Errorf("skipping a time of routine %s: %w", row.ID, err)
			}

			continue
		}

		if limit > 0 && len(due) == limit {
			return due, true, nil
		}

		if err := q.SetRoutineNextRun(ctx, store.SetRoutineNextRunParams{ID: row.ID, NextRunAt: next}); err != nil {
			return nil, false, fmt.Errorf("advancing routine %s: %w", row.ID, err)
		}

		row.NextRunAt = next
		due = append(due, routineFromRow(row))
	}

	return due, false, nil
}

// ClaimRoutineScript gives the launcher of the live run of a Routine with a
// script, a script Routine or a prompt Routine's check, what it runs: the file's name, its text, its limit, and the run's key. It is
// given once a run, to a caller that names the Routine's Bot: whoever asks
// again is refused, so a request can present the key only from inside the
// run the launcher started.
func (s *Store) ClaimRoutineScript(ctx context.Context, routineID, bot string) (ScriptLaunch, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return ScriptLaunch{}, fmt.Errorf("making a run's key: %w", err)
	}

	out := ScriptLaunch{Key: "hrk_" + hex.EncodeToString(raw)}

	err := s.write(ctx, func(q *store.Queries, _ int64) error {
		row, err := q.GetRoutine(ctx, routineID)
		if err != nil {
			return storeutil.NotFound(err, "routine", routineID)
		}

		switch {
		case row.BotID != bot:
			return fmt.Errorf("%w: routine %s is not bot %s's", storeerr.ErrInvalid, routineID, bot)
		case row.Script == "":
			return fmt.Errorf("%w: routine %s runs a prompt, not a script", storeerr.ErrInvalid, routineID)
		}

		live, err := liveRun(ctx, q, routineID)
		if err != nil {
			return err
		}

		if live == nil {
			return fmt.Errorf("%w: no run of routine %s is starting", storeerr.ErrConflict, routineID)
		}

		n, err := q.ClaimRoutineRunKey(ctx, store.ClaimRoutineRunKeyParams{
			ID: live.ID, KeyHash: nullString(bearertoken.Hash(out.Key)),
		})
		if err != nil {
			return fmt.Errorf("keeping the key of run %s: %w", live.ID, err)
		}

		if n != 1 {
			return fmt.Errorf("%w: run %s of routine %s was started already", storeerr.ErrConflict, live.ID, routineID)
		}

		out.Script, out.Text = row.Script, row.ScriptText
		out.Timeout = time.Duration(row.TimeoutSeconds) * time.Second

		return nil
	})
	if err != nil {
		return ScriptLaunch{}, fmt.Errorf("starting the script of routine %s: %w", routineID, err)
	}

	return out, nil
}

// GetRoutineRun returns one run.
func (s *Store) GetRoutineRun(ctx context.Context, id string) (RoutineRun, error) {
	row, err := s.read().GetRoutineRun(ctx, id)
	if err != nil {
		return RoutineRun{}, storeutil.NotFound(err, "routine run", id)
	}

	return routineRunFromRow(row), nil
}

// ListRoutineRuns returns a Routine's runs, the newest first.
func (s *Store) ListRoutineRuns(ctx context.Context, routineID string, limit int) ([]RoutineRun, error) {
	runs, _, err := s.RoutineRunPage(ctx, routineID, limit, 0)

	return runs, err
}

// RoutineRunPage returns a page of a Routine's runs, the newest first, from
// offset, and how many runs the Routine has (OpenClaw,
// src/gateway/server-methods/cron-history.ts: offset, limit and hasMore).
func (s *Store) RoutineRunPage(ctx context.Context, routineID string, limit, offset int,
) (runs []RoutineRun, total int, err error) {
	if limit <= 0 || limit > RoutineRunPageMax {
		limit = RoutineRunPageMax
	}

	offset = max(offset, 0)

	q := s.read()
	if _, err := q.GetRoutine(ctx, routineID); err != nil {
		return nil, 0, storeutil.NotFound(err, "routine", routineID)
	}

	rows, err := q.ListRoutineRuns(ctx, store.ListRoutineRunsParams{
		RoutineID: routineID, Lim: int64(limit), Off: int64(offset),
	})
	if err != nil {
		return nil, 0, fmt.Errorf("listing the runs of routine %s: %w", routineID, err)
	}

	count, err := q.CountRoutineRuns(ctx, routineID)
	if err != nil {
		return nil, 0, fmt.Errorf("counting the runs of routine %s: %w", routineID, err)
	}

	runs = make([]RoutineRun, len(rows))
	for i, r := range rows {
		runs[i] = routineRunFromRow(r)
	}

	return runs, int(count), nil
}

// LiveRoutineRuns returns the runs that still go or wait for the owner, each
// with its Routine, the oldest first.
func (s *Store) LiveRoutineRuns(ctx context.Context) ([]StartedRun, error) {
	q := s.read()

	rows, err := q.ListLiveRoutineRuns(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing the live runs of routines: %w", err)
	}

	out := make([]StartedRun, 0, len(rows))

	for _, r := range rows {
		routine, err := q.GetRoutine(ctx, r.RoutineID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}

		if err != nil {
			return nil, fmt.Errorf("reading routine %s: %w", r.RoutineID, err)
		}

		out = append(out, StartedRun{Routine: routineFromRow(routine), Run: routineRunFromRow(r)})
	}

	return out, nil
}

// KeptOutput is the end of a run's output that is kept, MaxRunOutput, cut on a
// character boundary.
func KeptOutput(s string) string {
	if len(s) <= MaxRunOutput {
		return s
	}

	cut := len(s) - MaxRunOutput
	for cut < len(s) && s[cut]&0xC0 == 0x80 {
		cut++
	}

	return "…" + s[cut:]
}

func nullCode(code *int) sql.NullInt64 {
	if code == nil {
		return sql.NullInt64{}
	}

	return sql.NullInt64{Int64: int64(*code), Valid: true}
}

// afterFailure is when a Routine that is on runs next after a failed run
// that ended at now, the failures-th in a row (OpenClaw,
// service/timer-outcomes.ts L483-537). A passing error is tried again after
// the first steps of the backoff, RoutineQuickRetries times in a row, when
// that is earlier than the schedule's next time (service/timer-trigger.ts
// L168-185). Otherwise the next run is the later of the schedule's next
// time and the run's end plus the backoff; a cron schedule takes its first
// time that is not before that.
func afterFailure(sched schedule, natural, now time.Time, failures int64, passing bool) time.Time {
	if passing && failures <= RoutineQuickRetries {
		if retry := now.Add(routineWait(failures, RoutineQuickRetries)); retry.Before(natural) {
			return retry
		}
	}

	floor := now.Add(routineWait(failures, len(routineBackoff)))

	switch {
	case !natural.Before(floor):
		return natural
	case sched.cron != nil:
		return sched.notBefore(floor)
	default:
		return floor
	}
}

// CheckRunResult refuses a run's end that is not one: a run ends
// succeeded, failed or canceled.
func CheckRunResult(r RunResult) error {
	if r.State != RunSucceeded && r.State != RunFailed && r.State != RunStopped {
		return fmt.Errorf("%w: a run ends succeeded, failed or canceled", storeerr.ErrInvalid)
	}

	return nil
}

// EndRunTx ends a run with the caller's queries. A run ends once: ending it
// again is a conflict. The caller then refuses what the run still asked the
// owner and applies the end to the Routine with SettleRunTx.
func EndRunTx(ctx context.Context, q *store.Queries, now int64, runID string, r RunResult) error {
	if _, err := q.GetRoutineRun(ctx, runID); err != nil {
		return storeutil.NotFound(err, "routine run", runID)
	}

	n, err := q.FinishRoutineRun(ctx, store.FinishRoutineRunParams{
		ID: runID, State: string(r.State), Now: sql.NullInt64{Int64: now, Valid: true},
		ExitCode: nullCode(r.ExitCode), Output: KeptOutput(r.Output), Error: r.Error,
	})
	if err != nil {
		return fmt.Errorf("ending run %s: %w", runID, err)
	}

	if n != 1 {
		return fmt.Errorf("%w: run %s has ended", storeerr.ErrConflict, runID)
	}

	return nil
}

// SettleRunTx applies what an ended run means for its Routine, with the
// caller's queries (OpenClaw, service/timer-outcomes.ts). A failed run, by
// the schedule or by hand, adds to the failures in a row and moves the next
// run (afterFailure); a success resets the count. A run ended by a restart
// of the control plane is recorded as failed and is no failure of the
// Routine (L179-182); a prompt Routine is then run again after the backoff,
// a script is not (service/jobs-scheduling.ts L31-49). A canceled run
// changes nothing. After RoutineAlertAfter failures in a row the owner is to
// be told, at most once every RoutineAlertEvery; after RoutinePauseAfter the
// Routine is paused with the reason.
func SettleRunTx(ctx context.Context, q *store.Queries, now int64, runID string, r RunResult) (Settled, error) {
	var out Settled

	run, err := q.GetRoutineRun(ctx, runID)
	if err != nil {
		return Settled{}, fmt.Errorf("reading run %s: %w", runID, err)
	}

	row, err := q.GetRoutine(ctx, run.RoutineID)
	if err != nil {
		return Settled{}, storeutil.NotFound(err, "routine", run.RoutineID)
	}

	out.Run = routineRunFromRow(run)

	if err := settleRoutine(ctx, q, now, row, r, &out); err != nil {
		return Settled{}, err
	}

	row, err = q.GetRoutine(ctx, row.ID)
	if err != nil {
		return Settled{}, fmt.Errorf("reading routine %s: %w", run.RoutineID, err)
	}

	out.Routine = routineFromRow(row)

	return out, nil
}

// settleRoutine applies a run's end to its Routine.
func settleRoutine(ctx context.Context, q *store.Queries, now int64, row store.Routine, r RunResult, out *Settled,
) error {
	p := store.SettleRoutineParams{
		ID: row.ID, State: row.State, PausedReason: row.PausedReason, NextRunAt: row.NextRunAt,
		Failures: row.Failures, AlertedAt: row.AlertedAt,
	}
	on := row.State == string(RoutineOn)

	sched, err := scheduleOfRow(row)
	if err != nil {
		return err
	}

	switch {
	case r.State == RunStopped:
		return nil
	case r.State == RunSucceeded:
		p.Failures, p.AlertedAt = 0, sql.NullInt64{}
	case r.Interrupted:
		// The run is no failure. A prompt is run again after the wait the streak asks for; a script that was cut
		// off may have done part of its work and waits for its schedule.
		if retry := now + routineWait(row.Failures, len(routineBackoff)).Milliseconds(); on &&
			row.Kind == string(RoutinePrompt) && (!row.NextRunAt.Valid || retry < row.NextRunAt.Int64) {
			p.NextRunAt = sql.NullInt64{Int64: retry, Valid: true}
		}
	default:
		p.Failures++

		if on {
			natural := sched.next(storeutil.Millis(now))
			if row.NextRunAt.Valid && row.NextRunAt.Int64 > now {
				natural = storeutil.Millis(row.NextRunAt.Int64)
			}

			p.NextRunAt = nullTime(afterFailure(sched, natural, storeutil.Millis(now), p.Failures, r.Passing))
		}

		if p.Failures >= RoutineAlertAfter &&
			(!row.AlertedAt.Valid || now-row.AlertedAt.Int64 >= RoutineAlertEvery.Milliseconds()) {
			p.AlertedAt, out.Alert = sql.NullInt64{Int64: now, Valid: true}, true
		}

		if p.Failures >= RoutinePauseAfter && on {
			p.State, p.NextRunAt, out.Paused = string(RoutinePaused), sql.NullInt64{}, true
			p.PausedReason = fmt.Sprintf("paused after %d failed runs in a row; the last: %s", p.Failures,
				firstLine(r.Error))
		}
	}

	if err := q.SettleRoutine(ctx, p); err != nil {
		return fmt.Errorf("applying a run's end to routine %s: %w", row.ID, err)
	}

	return nil
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	if r := []rune(line); len(r) > 200 {
		line = string(r[:200]) + "…"
	}

	return line
}

// PruneRoutineRuns removes the runs that are no longer kept: those that
// ended more than RoutineRunsKeptFor ago, and those beyond the newest
// RoutineRunsKept of a Routine.
func (s *Store) PruneRoutineRuns(ctx context.Context) error {
	err := s.write(ctx, func(q *store.Queries, now int64) error {
		before := sql.NullInt64{Int64: now - RoutineRunsKeptFor.Milliseconds(), Valid: true}
		if err := q.DeleteRoutineRunsBefore(ctx, before); err != nil {
			return err
		}

		routines, err := q.ListRoutines(ctx, store.ListRoutinesParams{})
		if err != nil {
			return err
		}

		for _, r := range routines {
			if err := q.DeleteRoutineRunsBeyond(ctx, store.DeleteRoutineRunsBeyondParams{
				RoutineID: r.ID, Keep: RoutineRunsKept,
			}); err != nil {
				return err
			}
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("removing old runs of routines: %w", err)
	}

	return nil
}

// RoutineGrants lists what the owner allowed a Routine always, as the
// Routine is now.
func (s *Store) RoutineGrants(ctx context.Context, routineID string) ([]RoutineGrant, error) {
	rows, err := s.read().ListRoutineGrants(ctx, routineID)
	if err != nil {
		return nil, fmt.Errorf("listing the grants of routine %s: %w", routineID, err)
	}

	out := make([]RoutineGrant, len(rows))
	for i, r := range rows {
		out[i] = RoutineGrant{
			RoutineID: r.RoutineID, Integration: r.Integration, Label: r.Label, Action: r.Action,
			ActionName: r.ActionName, ApprovalID: r.ApprovalID, CreatedAt: storeutil.Millis(r.CreatedAt),
		}
	}

	return out, nil
}

// RevokeRoutineGrant takes back an "always for this Routine": the action
// asks again from the Routine's next request.
func (s *Store) RevokeRoutineGrant(ctx context.Context, routineID, integration, label, action string) error {
	err := s.write(ctx, func(q *store.Queries, _ int64) error {
		n, err := q.DeleteRoutineGrant(ctx, store.DeleteRoutineGrantParams{
			RoutineID: routineID, Integration: integration, Label: label, Action: action,
		})
		if err != nil {
			return err
		}

		if n == 0 {
			return fmt.Errorf("%w: routine %s has no grant of %s for %s:%s", storeerr.ErrNotFound, routineID,
				action, integration, label)
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("taking back a grant of routine %s: %w", routineID, err)
	}

	return nil
}

// RunOfAskerTx finds, with the caller's queries, the live run of a Routine a
// request belongs to: the run whose chat asks, or the run whose key the
// script presents. A key is given once, to the launcher that starts the
// run's script, so naming a Routine is not enough to be its run. Both are
// empty for a request no live run made.
func RunOfAskerTx(ctx context.Context, q *store.Queries, runKey, chatID string) (routineID, runID string, err error) {
	var run store.RoutineRun

	switch {
	case runKey != "":
		run, err = q.LiveRoutineRunOfKey(ctx, nullString(bearertoken.Hash(runKey)))
	case chatID != "":
		run, err = q.LiveRoutineRunOfChat(ctx, nullString(chatID))
	default:
		return "", "", nil
	}

	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}

	if err != nil {
		return "", "", fmt.Errorf("reading the routine's run that asks: %w", err)
	}

	return run.RoutineID, run.ID, nil
}

// GrantedTx says, with the caller's queries, whether the owner allowed a
// Routine an action of a Connection always, as the Routine is now.
func GrantedTx(ctx context.Context, q *store.Queries, routineID, integration, label, action string) (bool, error) {
	grants, err := q.CountRoutineGrants(ctx, store.CountRoutineGrantsParams{
		RoutineID: routineID, Integration: integration, Label: label, Action: action,
	})
	if err != nil {
		return false, fmt.Errorf("reading the grants of routine %s: %w", routineID, err)
	}

	return grants > 0, nil
}

// GrantTx keeps, with the caller's queries, an "always for this Routine" the
// owner gave in answering an approval. The grant is for the Routine as it is
// now: a change of what it runs, or when, ends it.
func GrantTx(ctx context.Context, q *store.Queries, now int64, g RoutineGrant) error {
	routine, err := q.GetRoutine(ctx, g.RoutineID)
	if err != nil {
		return storeutil.NotFound(err, "routine", g.RoutineID)
	}

	if err := q.UpsertRoutineGrant(ctx, store.UpsertRoutineGrantParams{
		RoutineID: g.RoutineID, Integration: g.Integration, Label: g.Label, Action: g.Action,
		ActionName: g.ActionName, Revision: routine.Revision, ApprovalID: g.ApprovalID, Now: now,
	}); err != nil {
		return fmt.Errorf("keeping the grant of routine %s: %w", g.RoutineID, err)
	}

	return nil
}

// WaitRunTx records, with the caller's queries, that a run waits for the
// owner while its call does.
func WaitRunTx(ctx context.Context, q *store.Queries, runID string) error {
	if _, err := q.WaitRoutineRun(ctx, runID); err != nil {
		return fmt.Errorf("recording that run %s waits: %w", runID, err)
	}

	return nil
}

// ResumeRunTx lets a run that waited go on, with the caller's queries.
func ResumeRunTx(ctx context.Context, q *store.Queries, runID string) error {
	if _, err := q.ResumeRoutineRun(ctx, runID); err != nil {
		return fmt.Errorf("resuming run %s: %w", runID, err)
	}

	return nil
}

// CountRoutinesOfBotTx counts a Bot's Routines with the caller's queries: a
// Bot with Routines is not removed.
func CountRoutinesOfBotTx(ctx context.Context, q *store.Queries, bot string) (int64, error) {
	return q.CountRoutinesOfBot(ctx, bot)
}

// DetachFromChatTx makes the Routines and the runs made in a chat lose the
// chat, with the caller's queries, when the chat is deleted on its own or
// with its Bot (issue #484): they keep their rows.
func DetachFromChatTx(ctx context.Context, q *store.Queries, chatID string) error {
	if err := q.DetachRoutinesFromChat(ctx, chatID); err != nil {
		return err
	}

	return q.DetachRoutineRunsFromChat(ctx, chatID)
}

// CountRoutinesOfChatTx counts the Routines that tell their runs in a chat,
// with the caller's queries: such a chat is not deleted (issue #484).
func CountRoutinesOfChatTx(ctx context.Context, q *store.Queries, chatID string) (int64, error) {
	return q.CountRoutinesOfChat(ctx, chatID)
}

// LiveRunOfChatTx is the run a chat was opened for while it still goes or
// waits, read with the caller's queries; nil when none.
func LiveRunOfChatTx(ctx context.Context, q *store.Queries, chatID string) (*RoutineRun, error) {
	row, err := q.LiveRoutineRunOfChat(ctx, sql.NullString{String: chatID, Valid: true})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // No live run is an answer, not a failure.
	}

	if err != nil {
		return nil, fmt.Errorf("reading the live run of chat %s: %w", chatID, err)
	}

	run := routineRunFromRow(row)

	return &run, nil
}
