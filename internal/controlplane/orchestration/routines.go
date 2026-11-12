package orchestration

import (
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
)

// Routines (issue #211): work a Bot does on a schedule. What the work is, is
// not built in: a prompt the Bot works on with its model in a new chat, or a
// script that runs with the Bot's tools and no model. This file keeps the
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

// RoutineKind says what a Routine runs.
type RoutineKind string

// The kinds of a Routine.
const (
	RoutinePrompt RoutineKind = "prompt"
	RoutineScript RoutineKind = "script"
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
	MaxScriptText = MaxChatText
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
	ID   string
	Bot  string
	Name string
	Kind RoutineKind
	// Prompt is what a prompt Routine gives its Bot.
	Prompt string
	// Script is the name of the file a script Routine runs, never a path;
	// ScriptText what the file holds, written for each run.
	Script     string
	ScriptText string
	// EveryMinutes, or Cron with Timezone, is the schedule.
	EveryMinutes int
	Cron         string
	Timezone     string
	Timeout      time.Duration
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
}

// RoutineSpec is what a Routine is made or changed with.
type RoutineSpec struct {
	Bot            string      `json:"-"`
	Name           string      `json:"-"`
	Kind           RoutineKind `json:"kind"`
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

func routineFromRow(r store.Routine) Routine {
	return Routine{
		ID: r.ID, Bot: r.Bot, Name: r.Name, Kind: RoutineKind(r.Kind), Prompt: r.Prompt, Script: r.Script,
		ScriptText: r.ScriptText, EveryMinutes: int(r.EveryMinutes.Int64), Cron: r.Cron.String,
		Timezone: r.Timezone.String, Timeout: time.Duration(r.TimeoutSeconds) * time.Second,
		Revision: int(r.Revision), State: RoutineState(r.State), PausedReason: r.PausedReason, Summary: r.Summary,
		ChatID: r.ChatID.String, Announce: r.Announce == 1, NextRunAt: optionalTime(r.NextRunAt),
		Failures: int(r.Failures), Skipped: int(r.Skipped), LastSkippedAt: optionalTime(r.LastSkippedAt),
		CreatedBy: r.CreatedBy, CanUndo: undoOf(r) != nil, CreatedAt: millis(r.CreatedAt),
		UpdatedAt: millis(r.UpdatedAt),
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
		Bot: r.Bot, Name: r.Name, Kind: RoutineKind(r.Kind), Prompt: r.Prompt, Script: r.Script,
		ScriptText: r.ScriptText, EveryMinutes: int(r.EveryMinutes.Int64), Cron: r.Cron.String,
		Timezone: r.Timezone.String, TimeoutSeconds: int(r.TimeoutSeconds), Summary: r.Summary,
		ChatID: r.ChatID.String, Announce: r.Announce == 1,
	}
}

func routineRunFromRow(r store.RoutineRun) RoutineRun {
	out := RoutineRun{
		ID: r.ID, RoutineID: r.RoutineID, StartedBy: r.StartedBy, State: RunState(r.State),
		StartedAt: millis(r.StartedAt), EndedAt: optionalTime(r.EndedAt),
		Output: r.Output, Error: r.Error, ChatID: r.ChatID.String,
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
// work.
func RoutineSentence(r Routine) string {
	schedule := ScheduleWords(r)
	schedule = strings.ToUpper(schedule[:1]) + schedule[1:]

	work := "works on a prompt"
	if r.Kind == RoutineScript {
		work = "runs the script " + r.Script
	}

	if r.Summary != "" {
		work += ": " + strings.TrimSuffix(r.Summary, ".")
	}

	return fmt.Sprintf("%s, %s %s.", schedule, r.Bot, work)
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
		return schedule{}, fmt.Errorf("%w: a schedule is every N minutes or a cron expression, not both", ErrInvalid)
	case expr == "" && zone != "":
		return schedule{}, fmt.Errorf("%w: a time zone goes with a cron expression", ErrInvalid)
	case expr == "":
		if everyMinutes < MinRoutineMinutes || everyMinutes > MaxRoutineMinutes {
			return schedule{}, fmt.Errorf("%w: a Routine runs every %d to %d minutes, or by a cron expression",
				ErrInvalid, MinRoutineMinutes, MaxRoutineMinutes)
		}

		return schedule{every: time.Duration(everyMinutes) * time.Minute}, nil
	case zone == "":
		return schedule{}, fmt.Errorf("%w: a cron expression needs its time zone, such as Asia/Ho_Chi_Minh", ErrInvalid)
	case len(strings.Fields(expr)) != 5 || strings.Contains(expr, "="):
		return schedule{}, fmt.Errorf("%w: a cron expression has five fields: minute, hour, day of month, month, "+
			"day of week", ErrInvalid)
	}

	// "Local" would be the server's zone, whichever that is.
	loc, err := time.LoadLocation(zone)
	if err != nil || zone == "Local" {
		return schedule{}, fmt.Errorf("%w: %q is not an IANA time zone", ErrInvalid, zone)
	}

	parsed, err := cronParser.Parse(expr)
	if err != nil {
		return schedule{}, fmt.Errorf("%w: the cron expression %q: %s", ErrInvalid, expr, err.Error())
	}

	if parsed.Next(time.Unix(0, 0).In(loc)).IsZero() {
		return schedule{}, fmt.Errorf("%w: the cron expression %q never comes", ErrInvalid, expr)
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
		s.anchor = millis(r.AnchorAt.Int64)
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
	case !namePattern.MatchString(n.Name):
		return n, sched, fmt.Errorf("%w: a routine's name is lowercase letters, digits and dashes, starting with a "+
			"letter", ErrInvalid)
	case err != nil:
		return n, sched, err
	case len(n.Summary) > 500:
		return n, sched, fmt.Errorf("%w: what the work is, is said in at most 500 characters", ErrInvalid)
	case n.TimeoutSeconds < 0:
		return n, sched, fmt.Errorf("%w: a limit is a number of seconds", ErrInvalid)
	}

	switch n.Kind {
	case RoutinePrompt:
		n.Prompt = strings.TrimSpace(n.Prompt)

		switch {
		case n.Prompt == "":
			return n, sched, fmt.Errorf("%w: a prompt routine needs its prompt", ErrInvalid)
		case len(n.Prompt) > MaxChatText:
			return n, sched, fmt.Errorf("%w: a prompt is at most %d bytes", ErrInvalid, MaxChatText)
		case n.Script != "" || n.ScriptText != "":
			return n, sched, fmt.Errorf("%w: a routine runs a prompt or a script, not both", ErrInvalid)
		case n.TimeoutSeconds != 0:
			return n, sched, fmt.Errorf("%w: a prompt run works for at most %s; only a script's limit is set",
				ErrInvalid, PromptTimeout)
		}

		n.TimeoutSeconds = int(PromptTimeout / time.Second)
	case RoutineScript:
		if n.TimeoutSeconds == 0 {
			n.TimeoutSeconds = int(ScriptTimeout / time.Second)
		}

		switch {
		case !scriptPattern.MatchString(n.Script):
			return n, sched, fmt.Errorf("%w: a script is named by its file under the bot's routines directory, a "+
				"name of letters, digits, dots, dashes and underscores; never a path or a command line", ErrInvalid)
		case strings.TrimSpace(n.ScriptText) == "":
			return n, sched, fmt.Errorf("%w: a script routine needs the script's text", ErrInvalid)
		case len(n.ScriptText) > MaxScriptText:
			return n, sched, fmt.Errorf("%w: a script is at most %d bytes", ErrInvalid, MaxScriptText)
		case n.Prompt != "":
			return n, sched, fmt.Errorf("%w: a routine runs a prompt or a script, not both", ErrInvalid)
		}
	default:
		return n, sched, fmt.Errorf("%w: a routine runs a prompt or a script", ErrInvalid)
	}

	return n, sched, nil
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
func (s *Service) PutRoutine(ctx context.Context, n RoutineSpec, by RoutineAuthor) (Routine, error) {
	n, sched, err := checkRoutine(n)
	if err != nil {
		return Routine{}, err
	}

	switch {
	case by.FromRun:
		return Routine{}, fmt.Errorf("%w: a routine's run cannot make or change routines", ErrConflict)
	case by.Bot && n.ChatID == "":
		return Routine{}, fmt.Errorf("%w: a bot makes a routine from the chat it works in", ErrInvalid)
	}

	var id string

	err = s.write(ctx, func(q *store.Queries, now int64) error {
		if err := routineRefs(ctx, q, n, by); err != nil {
			return err
		}

		row, err := q.GetRoutineByName(ctx, store.GetRoutineByNameParams{Bot: n.Bot, Name: n.Name})
		if errors.Is(err, sql.ErrNoRows) {
			id = s.newID("rtn")

			return insertRoutine(ctx, q, now, id, n, sched, by)
		}

		if err != nil {
			return fmt.Errorf("reading routine %s of bot %s: %w", n.Name, n.Bot, err)
		}

		id = row.ID

		return updateRoutine(ctx, q, now, row, n, sched, by.Bot)
	})
	if err != nil {
		return Routine{}, fmt.Errorf("saving routine %s of bot %s: %w", n.Name, n.Bot, err)
	}

	return s.GetRoutine(ctx, id)
}

// routineRefs checks that a spec's Bot and chat exist, and for a Bot that
// the chat is one it talks in and not a Routine's run.
func routineRefs(ctx context.Context, q *store.Queries, n RoutineSpec, by RoutineAuthor) error {
	if _, err := q.GetBot(ctx, n.Bot); err != nil {
		return notFound(err, "bot", n.Bot)
	}

	if n.ChatID == "" {
		return nil
	}

	chat, err := q.GetChat(ctx, n.ChatID)
	if err != nil {
		return notFound(err, "chat", n.ChatID)
	}

	if !by.Bot {
		return nil
	}

	if chat.Bot != n.Bot {
		return fmt.Errorf("%w: chat %s talks with bot %s, not %s", ErrInvalid, n.ChatID, chat.Bot, n.Bot)
	}

	if _, err := q.RoutineRunOfChat(ctx, nullString(n.ChatID)); err == nil {
		return fmt.Errorf("%w: a routine's run cannot make or change routines", ErrConflict)
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
		sched.anchor = millis(now).Add(sched.every)
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
		ChatID: nullString(n.ChatID), Announce: boolInt(n.Announce), NextRunAt: nullTime(sched.next(millis(now))),
		CreatedBy: author, Undo: undo, Now: now,
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
		Now: now,
	}

	if !sameSchedule(before, n) {
		p.AnchorAt = sql.NullInt64{}

		if sched.cron == nil {
			sched.anchor = millis(now).Add(sched.every)
			p.AnchorAt = nullTime(sched.anchor)
		}

		if row.State == string(RoutineOn) {
			p.NextRunAt = nullTime(sched.next(millis(now)))
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
func (s *Service) UndoRoutine(ctx context.Context, id string) (r Routine, removed bool, live *RoutineRun, err error) {
	err = s.write(ctx, func(q *store.Queries, now int64) error {
		row, err := q.GetRoutine(ctx, id)
		if err != nil {
			return notFound(err, "routine", id)
		}

		undo := undoOf(row)

		switch {
		case !row.Undo.Valid:
			return fmt.Errorf("%w: the last change of routine %s was not a bot's; there is nothing to undo",
				ErrConflict, id)
		case undo == nil:
			return fmt.Errorf("%w: routine %s was changed since; undo it by hand", ErrConflict, id)
		case undo.Before == nil:
			removed, r = true, routineFromRow(row)
			live, err = liveRun(ctx, q, id)

			return errors.Join(err, q.DeleteRoutine(ctx, id))
		}

		before := *undo.Before
		before.Bot, before.Name = row.Bot, row.Name

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
func (s *Service) GetRoutine(ctx context.Context, id string) (Routine, error) {
	row, err := s.read().GetRoutine(ctx, id)
	if err != nil {
		return Routine{}, notFound(err, "routine", id)
	}

	return routineFromRow(row), nil
}

// ListRoutines returns the Routines, the oldest first.
func (s *Service) ListRoutines(ctx context.Context, f RoutineFilter) ([]Routine, error) {
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
func (s *Service) DeleteRoutine(ctx context.Context, id string) (*RoutineRun, error) {
	var live *RoutineRun

	err := s.write(ctx, func(q *store.Queries, _ int64) error {
		if _, err := q.GetRoutine(ctx, id); err != nil {
			return notFound(err, "routine", id)
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
func (s *Service) PauseRoutine(ctx context.Context, id string) (Routine, error) {
	return s.switchRoutine(ctx, id, RoutinePaused, "paused by the owner")
}

// ResumeRoutine switches a paused Routine on again, from now, with its
// failures forgotten.
func (s *Service) ResumeRoutine(ctx context.Context, id string) (Routine, error) {
	return s.switchRoutine(ctx, id, RoutineOn, "")
}

func (s *Service) switchRoutine(ctx context.Context, id string, to RoutineState, reason string) (Routine, error) {
	err := s.write(ctx, func(q *store.Queries, now int64) error {
		row, err := q.GetRoutine(ctx, id)
		if err != nil {
			return notFound(err, "routine", id)
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

			p.NextRunAt = nullTime(sched.next(millis(now)))
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

// promptNotice is what a prompt run's chat begins with. It goes to the Bot
// as a wake-up of the control plane, since nobody wrote it there.
func promptNotice(r store.Routine, by string) string {
	how := "by its schedule"
	if by == StartedByHand {
		how = "by hand, now"
	}

	return fmt.Sprintf("Routine %q (%s) runs %s. Nobody is in this chat: do the work below without asking, and end "+
		"with what you did. You cannot make or change routines from here.\n\n%s", r.Name, r.ID, how, r.Prompt)
}

// startRun begins a run of a Routine that has none live. A prompt run gets
// its own chat, with the prompt waiting for the Bot.
func (s *Service) startRun(ctx context.Context, q *store.Queries, now int64, row store.Routine, by string,
) (StartedRun, error) {
	id := s.newID("rrn")
	p := store.InsertRoutineRunParams{ID: id, RoutineID: row.ID, StartedBy: by, Now: now}

	if row.Kind == string(RoutinePrompt) {
		chatID := s.newID("cht")
		if err := q.InsertChat(ctx, store.InsertChatParams{
			ID: chatID, Title: "Routine " + row.Name, Bot: row.Bot, Now: now,
		}); err != nil {
			return StartedRun{}, fmt.Errorf("opening the chat of a run of routine %s: %w", row.ID, err)
		}

		if err := q.InsertRoutinePrompt(ctx, store.InsertRoutinePromptParams{
			ChatID: chatID, Text: promptNotice(row, by), Now: now,
		}); err != nil {
			return StartedRun{}, fmt.Errorf("giving the prompt of routine %s: %w", row.ID, err)
		}

		p.ChatID = nullString(chatID)
	}

	if err := q.InsertRoutineRun(ctx, p); err != nil {
		return StartedRun{}, fmt.Errorf("recording a run of routine %s: %w", row.ID, err)
	}

	run, err := q.GetRoutineRun(ctx, id)
	if err != nil {
		return StartedRun{}, fmt.Errorf("reading run %s: %w", id, err)
	}

	return StartedRun{Routine: routineFromRow(row), Run: routineRunFromRow(run)}, nil
}

// RunRoutineNow starts a run by hand. While a run of the Routine is live it
// answers that one is running (OpenClaw, service/ops-run-preparation.ts
// L207-209: "already-running").
func (s *Service) RunRoutineNow(ctx context.Context, id string) (StartedRun, error) {
	var out StartedRun

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		row, err := q.GetRoutine(ctx, id)
		if err != nil {
			return notFound(err, "routine", id)
		}

		live, err := liveRun(ctx, q, id)
		if err != nil {
			return err
		}

		if live != nil {
			return fmt.Errorf("%w: run %s of the routine is still %s", ErrConflict, live.ID, live.State)
		}

		out, err = s.startRun(ctx, q, now, row, StartedByHand)

		return err
	})
	if err != nil {
		return StartedRun{}, fmt.Errorf("running routine %s: %w", id, err)
	}

	return out, nil
}

// ClaimDueRoutines starts a run of the Routines whose time has come and
// returns the runs that began. A Routine's next time is set before its run
// starts, in the same transaction, so a crash does not run it twice (Hermes,
// cron/jobs.py L2701-2704). A Routine found overdue runs once: the times it
// missed are not replayed. A time that comes while a run of the Routine is
// live is skipped and counted on the Routine, with no run of its own
// (OpenClaw, service/timer-runnable.ts L83-88; the count: OpenMausBot,
// server/routines.ts L1059-1060). With a limit above zero at most that many
// runs begin, and more says Routines are still due.
func (s *Service) ClaimDueRoutines(ctx context.Context, limit int) (started []StartedRun, more bool, err error) {
	err = s.write(ctx, func(q *store.Queries, now int64) error {
		started, more = started[:0], false

		due, err := q.DueRoutines(ctx, sql.NullInt64{Int64: now, Valid: true})
		if err != nil {
			return fmt.Errorf("listing the routines that are due: %w", err)
		}

		for _, row := range due {
			sched, err := scheduleOfRow(row)
			if err != nil {
				return fmt.Errorf("routine %s: %w", row.ID, err)
			}

			next := nullTime(sched.next(millis(now)))

			live, err := liveRun(ctx, q, row.ID)
			if err != nil {
				return err
			}

			if live != nil {
				if err := q.SkipRoutineTime(ctx, store.SkipRoutineTimeParams{
					ID: row.ID, NextRunAt: next, SkippedAt: row.NextRunAt,
				}); err != nil {
					return fmt.Errorf("skipping a time of routine %s: %w", row.ID, err)
				}

				continue
			}

			if limit > 0 && len(started) == limit {
				more = true

				break
			}

			if err := q.SetRoutineNextRun(ctx, store.SetRoutineNextRunParams{ID: row.ID, NextRunAt: next}); err != nil {
				return fmt.Errorf("advancing routine %s: %w", row.ID, err)
			}

			row.NextRunAt = next

			run, err := s.startRun(ctx, q, now, row, StartedBySchedule)
			if err != nil {
				return err
			}

			started = append(started, run)
		}

		return nil
	})
	if err != nil {
		return nil, false, err
	}

	return started, more, nil
}

// ClaimRoutineScript gives the launcher of a script Routine's live run what
// it runs: the file's name, its text, its limit, and the run's key. It is
// given once a run, to a caller that names the Routine's Bot: whoever asks
// again is refused, so a request can present the key only from inside the
// run the launcher started.
func (s *Service) ClaimRoutineScript(ctx context.Context, routineID, bot string) (ScriptLaunch, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return ScriptLaunch{}, fmt.Errorf("making a run's key: %w", err)
	}

	out := ScriptLaunch{Key: "hrk_" + hex.EncodeToString(raw)}

	err := s.write(ctx, func(q *store.Queries, _ int64) error {
		row, err := q.GetRoutine(ctx, routineID)
		if err != nil {
			return notFound(err, "routine", routineID)
		}

		switch {
		case row.Bot != bot:
			return fmt.Errorf("%w: routine %s is not bot %s's", ErrInvalid, routineID, bot)
		case row.Kind != string(RoutineScript):
			return fmt.Errorf("%w: routine %s runs a prompt, not a script", ErrInvalid, routineID)
		}

		live, err := liveRun(ctx, q, routineID)
		if err != nil {
			return err
		}

		if live == nil {
			return fmt.Errorf("%w: no run of routine %s is starting", ErrConflict, routineID)
		}

		n, err := q.ClaimRoutineRunKey(ctx, store.ClaimRoutineRunKeyParams{
			ID: live.ID, KeyHash: nullString(bearertoken.Hash(out.Key)),
		})
		if err != nil {
			return fmt.Errorf("keeping the key of run %s: %w", live.ID, err)
		}

		if n != 1 {
			return fmt.Errorf("%w: run %s of routine %s was started already", ErrConflict, live.ID, routineID)
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
func (s *Service) GetRoutineRun(ctx context.Context, id string) (RoutineRun, error) {
	row, err := s.read().GetRoutineRun(ctx, id)
	if err != nil {
		return RoutineRun{}, notFound(err, "routine run", id)
	}

	return routineRunFromRow(row), nil
}

// ListRoutineRuns returns a Routine's runs, the newest first.
func (s *Service) ListRoutineRuns(ctx context.Context, routineID string, limit int) ([]RoutineRun, error) {
	runs, _, err := s.RoutineRunPage(ctx, routineID, limit, 0)

	return runs, err
}

// RoutineRunPage returns a page of a Routine's runs, the newest first, from
// offset, and how many runs the Routine has (OpenClaw,
// src/gateway/server-methods/cron-history.ts: offset, limit and hasMore).
func (s *Service) RoutineRunPage(ctx context.Context, routineID string, limit, offset int,
) (runs []RoutineRun, total int, err error) {
	if limit <= 0 || limit > RoutineRunPageMax {
		limit = RoutineRunPageMax
	}

	offset = max(offset, 0)

	q := s.read()
	if _, err := q.GetRoutine(ctx, routineID); err != nil {
		return nil, 0, notFound(err, "routine", routineID)
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
func (s *Service) LiveRoutineRuns(ctx context.Context) ([]StartedRun, error) {
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

// tail keeps the end of a run's output, cut on a character boundary.
func tail(s string) string {
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

// FinishRoutineRun ends a run and applies what its end means for the
// Routine (OpenClaw, service/timer-outcomes.ts). A failed run, by the
// schedule or by hand, adds to the failures in a row and moves the next run
// (afterFailure); a success resets the count. A run ended by a restart of
// the control plane is recorded as failed and is no failure of the Routine
// (L179-182); a prompt Routine is then run again after the backoff, a script
// is not (service/jobs-scheduling.ts L31-49). A canceled run changes
// nothing. After RoutineAlertAfter failures in a row the owner is to be
// told, at most once every RoutineAlertEvery; after RoutinePauseAfter the
// Routine is paused with the reason. What the run still asked the owner is
// refused. A run ends once: ending it again is a conflict.
func (s *Service) FinishRoutineRun(ctx context.Context, runID string, r RunResult) (Settled, error) {
	if r.State != RunSucceeded && r.State != RunFailed && r.State != RunStopped {
		return Settled{}, fmt.Errorf("%w: a run ends succeeded, failed or canceled", ErrInvalid)
	}

	var out Settled

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		out = Settled{}

		if _, err := q.GetRoutineRun(ctx, runID); err != nil {
			return notFound(err, "routine run", runID)
		}

		n, err := q.FinishRoutineRun(ctx, store.FinishRoutineRunParams{
			ID: runID, State: string(r.State), Now: sql.NullInt64{Int64: now, Valid: true},
			ExitCode: nullCode(r.ExitCode), Output: tail(r.Output), Error: r.Error,
		})
		if err != nil {
			return fmt.Errorf("ending run %s: %w", runID, err)
		}

		if n != 1 {
			return fmt.Errorf("%w: run %s has ended", ErrConflict, runID)
		}

		if err := q.ExpireApprovalsOfRoutineRun(ctx, store.ExpireApprovalsOfRoutineRunParams{
			RoutineRunID: nullString(runID), Now: sql.NullInt64{Int64: now, Valid: true},
		}); err != nil {
			return fmt.Errorf("refusing what run %s still asked: %w", runID, err)
		}

		run, err := q.GetRoutineRun(ctx, runID)
		if err != nil {
			return fmt.Errorf("reading run %s: %w", runID, err)
		}

		row, err := q.GetRoutine(ctx, run.RoutineID)
		if err != nil {
			return notFound(err, "routine", run.RoutineID)
		}

		out.Run = routineRunFromRow(run)

		if err := settleRoutine(ctx, q, now, row, r, &out); err != nil {
			return err
		}

		row, err = q.GetRoutine(ctx, row.ID)
		if err != nil {
			return fmt.Errorf("reading routine %s: %w", run.RoutineID, err)
		}

		out.Routine = routineFromRow(row)

		return nil
	})
	if err != nil {
		return Settled{}, err
	}

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
			natural := sched.next(millis(now))
			if row.NextRunAt.Valid && row.NextRunAt.Int64 > now {
				natural = millis(row.NextRunAt.Int64)
			}

			p.NextRunAt = nullTime(afterFailure(sched, natural, millis(now), p.Failures, r.Passing))
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
func (s *Service) PruneRoutineRuns(ctx context.Context) error {
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

// RunAnswers is what the owner said, or did not say, to what a run asked.
type RunAnswers struct {
	// Rejected is the request the owner said no to; nil when he said no to
	// none.
	Rejected *Approval
	// Expired is the request nobody answered in time; nil when each was
	// answered.
	Expired *Approval
}

// RoutineRunAnswers reads how the Approvals of a run ended.
func (s *Service) RoutineRunAnswers(ctx context.Context, runID string) (RunAnswers, error) {
	rows, err := s.read().ListDecidedApprovalsOfRoutineRun(ctx, nullString(runID))
	if err != nil {
		return RunAnswers{}, fmt.Errorf("reading the answered approvals of run %s: %w", runID, err)
	}

	var out RunAnswers

	for _, row := range rows {
		a, err := approvalFromRow(row)
		if err != nil {
			return RunAnswers{}, err
		}

		switch {
		case a.Expired && out.Expired == nil:
			out.Expired = &a
		case a.Status == ApprovalRejected && !a.Expired && out.Rejected == nil:
			out.Rejected = &a
		}
	}

	return out, nil
}

// RoutineGrants lists what the owner allowed a Routine always, as the
// Routine is now.
func (s *Service) RoutineGrants(ctx context.Context, routineID string) ([]RoutineGrant, error) {
	rows, err := s.read().ListRoutineGrants(ctx, routineID)
	if err != nil {
		return nil, fmt.Errorf("listing the grants of routine %s: %w", routineID, err)
	}

	out := make([]RoutineGrant, len(rows))
	for i, r := range rows {
		out[i] = RoutineGrant{
			RoutineID: r.RoutineID, Integration: r.Integration, Label: r.Label, Action: r.Action,
			ActionName: r.ActionName, ApprovalID: r.ApprovalID, CreatedAt: millis(r.CreatedAt),
		}
	}

	return out, nil
}

// RevokeRoutineGrant takes back an "always for this Routine": the action
// asks again from the Routine's next request.
func (s *Service) RevokeRoutineGrant(ctx context.Context, routineID, integration, label, action string) error {
	err := s.write(ctx, func(q *store.Queries, _ int64) error {
		n, err := q.DeleteRoutineGrant(ctx, store.DeleteRoutineGrantParams{
			RoutineID: routineID, Integration: integration, Label: label, Action: action,
		})
		if err != nil {
			return err
		}

		if n == 0 {
			return fmt.Errorf("%w: routine %s has no grant of %s for %s", ErrNotFound, routineID, action,
				Use(integration, label))
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("taking back a grant of routine %s: %w", routineID, err)
	}

	return nil
}

// TellChat adds a line the Bot said to a chat, for whoever reads it: what a
// Routine's run has to tell where the Routine was made. A run's result names
// the run in a part, which the web draws as the run's card (OpenMausBot,
// src/components/RoutineRunCard.tsx: a message carries its run); the text is
// for every other reader.
func (s *Service) TellChat(ctx context.Context, chatID, text, routineID, runID string) error {
	var parts json.RawMessage

	if runID != "" {
		var err error

		parts, err = json.Marshal([]map[string]string{
			{"type": "routine_run", "id": runID, "routine_id": routineID, "text": text},
		})
		if err != nil {
			return fmt.Errorf("writing the part of run %s: %w", runID, err)
		}
	}

	_, err := s.AddChatMessageParts(ctx, chatID, ChatCoordinator, text, parts)

	return err
}
