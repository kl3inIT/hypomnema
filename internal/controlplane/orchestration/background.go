package orchestration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
)

// A Bot's background commands (issue #1058). pi's bash takes `background: true` (the box's pi-background
// extension): the command runs on, the tool answers at once, and its exit wakes the chat with an ordinary wake-up,
// so the Bot ends its turn instead of sleeping to poll, as OpenClaw's exec continues the conversation when a
// backgrounded command exits (tools.exec.notifyOnExit, on by default: src/config/schema.help.runtime.ts:467-470;
// docs/tools/exec.md:171-173; src/agents/bash-tools.exec-notify.ts:216-260). Since issue #1084 each command is a
// systemd user unit of its own, which a restart of the control plane leaves running (coordinator/background.go), as
// OpenClaw's durable commands are (docs/gateway/background-process.md:140-235). Each command has a row while it runs
// (OpenClaw's durable record, bash-durable-process-store.ts); the wake takes it out, once. A command whose unit is
// gone is reported lost from its row, as OpenClaw reports a missing unit (bash-process-durable.ts:420-425). A wake
// that comes while the chat's pi works waits for its turn to settle and goes with the other wake-ups as the next
// prompt (the supervisor's deliver), as OpenClaw holds an exit behind the session's turn in flight and lets those
// waiting share the next one (src/auto-reply/reply/session-event-batch.ts:34-58).

// BackgroundTail is how much of a command's output its wake carries: the last 2,000 characters, OpenClaw's
// (bash-tools.exec-notify.ts:234, docs/tools/exec.md:173).
const BackgroundTail = 2000

// backgroundID is what a command is named: bg_ and 6 to 32 lowercase letters or digits.
var backgroundID = regexp.MustCompile(`^bg_[a-z0-9]{6,32}$`)

// maxBackgroundFile bounds the path of the file a command's output is in.
const maxBackgroundFile = 512

// BackgroundCommand is a command that runs, as its row has it.
type BackgroundCommand struct {
	ChatID, ID string
	StartedAt  time.Time
}

// BackgroundEnd is how a background command ended, as its unit says.
type BackgroundEnd struct {
	ID string
	// ExitCode is its exit code; nil when a signal ended it.
	ExitCode *int
	// Signal is the signal that ended it, such as SIGTERM; empty when it exited.
	Signal string
	// TimedOut says it was killed because its deadline passed.
	TimedOut bool
	// Stopped says it was stopped because its chat asked.
	Stopped bool
	// Ran is how long it ran, as its unit measured it; zero: since its row was written.
	Ran time.Duration
	// Output is the last part of its output, at most BackgroundTail characters.
	Output string
	// OutputBytes is how many bytes it wrote in all.
	OutputBytes int64
	// OutputFile is the file that has all its output, when Output is only its end; empty when not.
	OutputFile string
}

// IsBackgroundID says whether id is a background command's id.
func IsBackgroundID(id string) bool { return backgroundID.MatchString(id) }

// StartBackground records that the pi whose token hashes to pi started command id in the background in its chat.
func (s *Service) StartBackground(ctx context.Context, chatID, pi, id string) error {
	if !backgroundID.MatchString(id) {
		return fmt.Errorf("%w: a background command's id is bg_ and 6 to 32 lowercase letters or digits", ErrInvalid)
	}

	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		return q.InsertChatBackground(ctx, store.InsertChatBackgroundParams{ChatID: chatID, ID: id, Pi: pi, Now: now})
	}); err != nil {
		return fmt.Errorf("recording background command %s of chat %s: %w", id, chatID, err)
	}

	return nil
}

// EndBackground wakes the chat for a background command that ended and takes out its row. It says false, and wakes
// nothing, when the command has no row: it was reported already, as OpenClaw claims an exit once
// (bash-tools.exec-notify.ts:216-229).
func (s *Service) EndBackground(ctx context.Context, chatID string, e BackgroundEnd) (bool, error) {
	if err := e.valid(); err != nil {
		return false, err
	}

	woke := false

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		started, err := q.DeleteChatBackground(ctx, store.DeleteChatBackgroundParams{ChatID: chatID, ID: e.ID})
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}

		if err != nil {
			return err
		}

		woke = true

		ran := e.Ran
		if ran <= 0 {
			ran = time.Duration(now-started) * time.Millisecond
		}

		return wakeForBackground(ctx, q, chatID, endedText(e, ran), now)
	})
	if err != nil {
		return false, fmt.Errorf("waking chat %s for background command %s: %w", chatID, e.ID, err)
	}

	return woke, nil
}

// LoseBackground reports lost a command whose unit is gone, with a wake-up saying why and naming the file its output
// is in when there is one. It says false, and wakes nothing, when the command has no row.
func (s *Service) LoseBackground(ctx context.Context, chatID, id, why, file string) (bool, error) {
	woke := false

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		started, err := q.DeleteChatBackground(ctx, store.DeleteChatBackgroundParams{ChatID: chatID, ID: id})
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}

		if err != nil {
			return err
		}

		woke = true

		return wakeForBackground(ctx, q, chatID, lostText(id, millis(started), why, file), now)
	})
	if err != nil {
		return false, fmt.Errorf("reporting background command %s of chat %s lost: %w", id, chatID, err)
	}

	return woke, nil
}

// ForgetBackground takes out the row of a command that could not start, which wakes nothing.
func (s *Service) ForgetBackground(ctx context.Context, chatID, id string) error {
	err := s.write(ctx, func(q *store.Queries, _ int64) error {
		_, err := q.DeleteChatBackground(ctx, store.DeleteChatBackgroundParams{ChatID: chatID, ID: id})
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}

		return err
	})
	if err != nil {
		return fmt.Errorf("forgetting background command %s of chat %s: %w", id, chatID, err)
	}

	return nil
}

// BackgroundCommands lists the commands that run, oldest first.
func (s *Service) BackgroundCommands(ctx context.Context) ([]BackgroundCommand, error) {
	rows, err := s.read().ListChatBackground(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing background commands: %w", err)
	}

	out := make([]BackgroundCommand, 0, len(rows))
	for _, r := range rows {
		out = append(out, BackgroundCommand{ChatID: r.ChatID, ID: r.ID, StartedAt: millis(r.StartedAt)})
	}

	return out, nil
}

func wakeForBackground(ctx context.Context, q *store.Queries, chatID, text string, now int64) error {
	if err := q.InsertRunAdoptionNotice(ctx, store.InsertRunAdoptionNoticeParams{
		ChatID: chatID, Text: text, Now: now,
	}); err != nil {
		return fmt.Errorf("adding a wake-up to chat %s: %w", chatID, err)
	}

	return q.TouchChat(ctx, store.TouchChatParams{ID: chatID, Now: now})
}

func (e BackgroundEnd) valid() error {
	switch {
	case !backgroundID.MatchString(e.ID):
		return fmt.Errorf("%w: a background command's id is bg_ and 6 to 32 lowercase letters or digits", ErrInvalid)
	case (e.ExitCode == nil) == (e.Signal == ""):
		return fmt.Errorf("%w: a background command ends with an exit code or a signal", ErrInvalid)
	case e.Signal != "" && !signalName.MatchString(e.Signal):
		return fmt.Errorf("%w: a signal is named as SIGTERM", ErrInvalid)
	case !utf8.ValidString(e.Output) || utf8.RuneCountInString(e.Output) > BackgroundTail:
		return fmt.Errorf("%w: a background command's output is at most %d characters of UTF-8", ErrInvalid,
			BackgroundTail)
	case len(e.OutputFile) > maxBackgroundFile || strings.ContainsAny(e.OutputFile, "\n\r"):
		return fmt.Errorf("%w: a background command's output file is one path of at most %d bytes", ErrInvalid,
			maxBackgroundFile)
	case e.OutputBytes < 0:
		return fmt.Errorf("%w: a background command's output size is not negative", ErrInvalid)
	}

	return nil
}

var signalName = regexp.MustCompile(`^SIG[A-Z0-9]{1,10}$`)

// endedText is the wake of a command that ended: how, how long it ran and the end of its output, so the woken turn
// needs nothing more, after OpenClaw's (bash-tools.exec-notify.ts:245-260).
func endedText(e BackgroundEnd, ran time.Duration) string {
	how := "exited with code " + strconv.Itoa(derefInt(e.ExitCode))

	switch {
	case e.Stopped:
		how = "was stopped, as this chat asked,"
	case e.TimedOut && e.ExitCode == nil:
		how = "was killed (" + e.Signal + ") when its timeout ran out"
	case e.TimedOut:
		how = "exited with code " + strconv.Itoa(*e.ExitCode) + " when its timeout ran out"
	case e.ExitCode == nil:
		how = "was killed by " + e.Signal
	}

	head := fmt.Sprintf("Background command %s %s after %s", e.ID, how, ran.Round(time.Second))
	output := strings.TrimSpace(e.Output)

	var b strings.Builder

	switch {
	case output == "":
		b.WriteString(head + " with no output.")
	case e.OutputFile == "":
		b.WriteString(head + ":\n" + output)
	default:
		fmt.Fprintf(&b, "%s, the end of its %d bytes of output:\n%s", head, e.OutputBytes, output)
	}

	if e.OutputFile != "" {
		b.WriteString("\nFull output: " + e.OutputFile + "; read only the part you need.")
	}

	if e.TimedOut && !e.Stopped {
		b.WriteString("\n" + timeoutGuidance)
	}

	return b.String()
}

// timeoutGuidance follows the wake of a command its timeout ended, OpenClaw's (src/agents/bash-tools.exec-output.ts:
// 117-118, 174-182).
const timeoutGuidance = "The command was terminated, but external side effects may already have completed. Verify " +
	"the resulting state before retrying. Do not automatically rerun commands unless repeating them is safe. Use a " +
	"higher timeout only when the command is known to be safe to retry."

// lostText is the wake of a command whose unit is gone, so no result will come, after OpenClaw's missing unit
// ("Durable process unit is missing; outcome unknown", bash-process-durable.ts:420-425) and its lost-command notice
// (bash-process-durable.ts:158-161).
func lostText(id string, started time.Time, why, file string) string {
	text := fmt.Sprintf("Background command %s (started %s) ended with no result: %s, so its outcome is unknown. "+
		"If you still need it, check its output and side effects, then run it again.", id,
		started.UTC().Format(time.RFC3339), why)
	if file != "" {
		text += "\nIts output: " + file + "; read only the part you need."
	}

	return text
}

func derefInt(p *int) int {
	if p == nil {
		return 0
	}

	return *p
}
