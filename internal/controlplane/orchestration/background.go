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
// extension): the command runs on in the box, the tool answers at once, and its exit wakes the chat with an
// ordinary wake-up, so the Bot ends its turn instead of sleeping to poll, as OpenClaw's exec continues the
// conversation when a backgrounded command exits (tools.exec.notifyOnExit, on by default; docs/tools/exec.md:178,
// src/agents/bash-tools.exec-notify.ts). Each command has a row while it runs (OpenClaw's
// bash-process-durable.ts:83-129); the wake takes it out, once. A command whose pi stopped, or that a restart of the
// control plane cut, is reported lost from its row, as OpenClaw reports what the last Gateway lost
// (bash-process-durable.ts:132-161). A wake that comes while the chat's pi works waits for its turn to settle and goes
// with the other wake-ups as the next prompt (the supervisor's deliver), as OpenClaw holds an exit behind the
// session's turn in flight and lets those waiting share the next one (src/auto-reply/reply/session-event-batch.ts:
// 34-58).

// BackgroundTail is how much of a command's output its wake carries: the last 2,000 characters, OpenClaw's
// (bash-tools.exec-notify.ts:234, docs/tools/exec.md:181).
const BackgroundTail = 2000

// backgroundID is what the extension names a command.
var backgroundID = regexp.MustCompile(`^bg_[a-z0-9]{6,32}$`)

// maxBackgroundFile bounds the path of the file a command's output is in.
const maxBackgroundFile = 512

// BackgroundEnd is how a background command ended, as the extension that ran it says.
type BackgroundEnd struct {
	ID string
	// ExitCode is its exit code; nil when a signal ended it.
	ExitCode *int
	// Signal is the signal that ended it, such as SIGTERM; empty when it exited.
	Signal string
	// Output is the last part of its output, at most BackgroundTail characters.
	Output string
	// OutputChars is how many characters it wrote in all.
	OutputChars int64
	// OutputFile is the file in the box that has all its output, when Output is only its end; empty when not.
	OutputFile string
	// Dropped says output past the file's limit was discarded.
	Dropped bool
}

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
// nothing, when the command has no row of that pi: it was reported lost already, or never started, as OpenClaw
// claims an exit once (bash-tools.exec-notify.ts:216-229).
func (s *Service) EndBackground(ctx context.Context, chatID, pi string, e BackgroundEnd) (bool, error) {
	if err := e.valid(); err != nil {
		return false, err
	}

	woke := false

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		started, err := q.DeleteChatBackground(ctx, store.DeleteChatBackgroundParams{ChatID: chatID, ID: e.ID, Pi: pi})
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}

		if err != nil {
			return err
		}

		woke = true

		return wakeForBackground(ctx, q, chatID, endedText(e, time.Duration(now-started)*time.Millisecond), now)
	})
	if err != nil {
		return false, fmt.Errorf("waking chat %s for background command %s: %w", chatID, e.ID, err)
	}

	return woke, nil
}

// LoseBackground reports lost the background commands the pi whose token hashes to pi had in its chat when it
// stopped, each with a wake-up saying why, and returns how many. With chatID empty it reports every command of every
// chat, which only a start of the control plane asks, before any pi runs.
func (s *Service) LoseBackground(ctx context.Context, chatID, pi, why string) (int, error) {
	n := 0

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		var lost []store.DeleteAllChatBackgroundRow

		if chatID == "" {
			rows, err := q.DeleteAllChatBackground(ctx)
			if err != nil {
				return err
			}

			lost = rows
		} else {
			rows, err := q.DeleteLostChatBackground(ctx, store.DeleteLostChatBackgroundParams{ChatID: chatID, Pi: pi})
			if err != nil {
				return err
			}

			for _, r := range rows {
				lost = append(lost, store.DeleteAllChatBackgroundRow(r))
			}
		}

		for _, r := range lost {
			if err := wakeForBackground(ctx, q, r.ChatID, lostText(r.ID, millis(r.StartedAt), why), now); err != nil {
				return err
			}
		}

		n = len(lost)

		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("reporting lost background commands: %w", err)
	}

	return n, nil
}

// ChatsWithBackground lists the chats whose pi runs a command in the background: the supervisor does not stop such a
// pi for being idle, as its box would end the command with it (OpenClaw's process owner keeps a backgrounded command
// until it exits, docs/tools/exec.md:191).
func (s *Service) ChatsWithBackground(ctx context.Context) ([]string, error) {
	ids, err := s.read().ChatsWithBackground(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing chats with background commands: %w", err)
	}

	return ids, nil
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
	case e.OutputChars < 0:
		return fmt.Errorf("%w: a background command's output size is not negative", ErrInvalid)
	}

	return nil
}

var signalName = regexp.MustCompile(`^SIG[A-Z0-9]{1,10}$`)

// endedText is the wake of a command that ended: how, how long it ran and the end of its output, so the woken turn
// needs nothing more, after OpenClaw's (bash-tools.exec-notify.ts:245-260).
func endedText(e BackgroundEnd, ran time.Duration) string {
	how := "exited with code " + strconv.Itoa(derefInt(e.ExitCode))
	if e.ExitCode == nil {
		how = "was killed by " + e.Signal
	}

	head := fmt.Sprintf("Background command %s %s after %s", e.ID, how, ran.Round(time.Second))
	output := strings.TrimSpace(e.Output)
	shown := int64(utf8.RuneCountInString(e.Output))

	var b strings.Builder

	switch {
	case output == "":
		b.WriteString(head + " with no output.")
	case e.OutputChars <= shown:
		b.WriteString(head + ":\n" + output)
	default:
		fmt.Fprintf(&b, "%s, the last %d of %d output characters:\n%s", head, shown, e.OutputChars, output)
	}

	switch {
	case e.OutputFile != "" && e.Dropped:
		b.WriteString("\nFull output, up to its limit (later output was discarded): " + e.OutputFile +
			"; read only the part you need.")
	case e.OutputFile != "":
		b.WriteString("\nFull output: " + e.OutputFile + "; read only the part you need.")
	}

	return b.String()
}

// lostText is the wake of a command whose pi stopped while it ran, after OpenClaw's lost-command notice
// (bash-process-durable.ts:158-161): its box ended it, so no result will come.
func lostText(id string, started time.Time, why string) string {
	return fmt.Sprintf("Background command %s (started %s) was killed: %s while it ran, so its result will not "+
		"arrive. If you still need it, check its output and side effects, then run it again.", id,
		started.UTC().Format(time.RFC3339), why)
}

func derefInt(p *int) int {
	if p == nil {
		return 0
	}

	return *p
}
