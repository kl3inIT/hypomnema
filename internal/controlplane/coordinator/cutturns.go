package coordinator

import (
	"context"
	"log/slog"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// A turn a restart cut continues by itself (issue #716), as Hermes's auto-continue (tui_gateway/
// session_auto_continue.py, gateway/restart_loop_guard.py): each turn is marked in the store while it runs
// (orchestration/cutturns.go), and at start the supervisor wakes each chat whose mark is fresh to go on with it.
const (
	// continueFreshFor is how recent a cut turn must be to continue (Hermes's freshness_minutes, 15).
	continueFreshFor = 15 * time.Minute
	// continueAttempts is how many times one cut turn is continued (Hermes's max_attempts, 2).
	continueAttempts = 2
	// restartLoopBoots chained starts that each found cut turns are a restart loop, in which no turn is continued
	// (Hermes's restart_loop_guard max_restarts, 3).
	restartLoopBoots = 3
	// restartLoopGap is the widest gap between two starts of one loop (Hermes's max_gap_seconds, 300, which the
	// 60-second window does not widen).
	restartLoopGap = 5 * time.Minute
)

// continuedPrompt is what pi is given for ContinuedText, after Hermes's auto-continue note
// (session_auto_continue.py _auto_continue_note): pi's session holds the request and what the cut turn had done.
const continuedPrompt = "The control plane restarted for an update and cut your previous turn before it could " +
	"finish. Some of the work may already be complete; check the current state before redoing anything, then " +
	"finish the task."

// turnStarted marks the chat's turn in the store. The write outlives the request that runs it: a turn that starts
// as the control plane stops is still marked.
func (s *Supervisor) turnStarted(ctx context.Context, chatID string) {
	if err := s.svc.MarkChatTurn(context.WithoutCancel(ctx), chatID); err != nil {
		s.cfg.Logger.ErrorContext(ctx, "marking a turn failed", slog.String("event", "coordinator.turn_mark_failed"),
			slog.String("chat_id", chatID), slog.Any("error", err))
	}
}

// turnSettled clears the chat's mark: its turn ended, however it ended.
func (s *Supervisor) turnSettled(ctx context.Context, chatID string) {
	if err := s.svc.SettleChatTurn(context.WithoutCancel(ctx), chatID); err != nil {
		s.cfg.Logger.ErrorContext(ctx, "clearing a turn mark failed",
			slog.String("event", "coordinator.turn_mark_failed"), slog.String("chat_id", chatID),
			slog.Any("error", err))
	}
}

// exitedMidTurn is called when a chat's pi exits. While the control plane runs, a pi that exits ended its turn,
// which the next message starts again; its mark is cleared. When the control plane stops it, the mark stays: that
// is the cut turn the next start continues. pi gets the same SIGTERM as the control plane on `systemctl stop`, so
// both the stop and the signal's context say it is stopping.
func (s *Supervisor) exitedMidTurn(ctx context.Context, chatID string) {
	if ctx.Err() != nil || s.stopping.Load() {
		return
	}

	s.turnSettled(ctx, chatID)
}

// continueCutTurns runs once, as the supervisor starts. A start that finds no cut turn records nothing. One that
// does is recorded for the restart-loop guard; while it trips, every mark stays as it is and nothing is woken, as
// Hermes skips the auto-resume and keeps the sessions resume-pending (gateway/run_restored_delivery.py). Otherwise a
// fresh mark with attempts left is continued and any other is cleared with a line saying the turn was cut.
func (s *Supervisor) continueCutTurns(ctx context.Context) {
	log := s.cfg.Logger

	cut, err := s.svc.CutTurns(ctx)
	if err != nil {
		log.ErrorContext(ctx, "reading the cut turns failed", slog.String("event", "coordinator.cut_turns_failed"),
			slog.Any("error", err))

		return
	}

	if len(cut) == 0 {
		return
	}

	// A guard that cannot be read fails open, as Hermes's: it never keeps a turn from continuing.
	chained, err := s.svc.RecordRestartBoot(ctx, restartLoopGap)
	if err != nil {
		log.ErrorContext(ctx, "recording the restart failed", slog.String("event", "coordinator.cut_turns_failed"),
			slog.Any("error", err))
	}

	if chained >= restartLoopBoots {
		log.WarnContext(ctx, "restart loop: cut turns are not continued",
			slog.String("event", "coordinator.restart_loop"), slog.Int("chained_starts", chained),
			slog.Int("cut_turns", len(cut)))

		return
	}

	for _, c := range cut {
		fresh := c.Age <= continueFreshFor && c.Attempts < continueAttempts
		if fresh {
			err = s.svc.ContinueCutTurn(ctx, c.ChatID)
		} else {
			err = s.svc.EndCutTurn(ctx, c.ChatID)
		}

		if err != nil {
			log.ErrorContext(ctx, "settling a cut turn failed", slog.String("event", "coordinator.cut_turns_failed"),
				slog.String("chat_id", c.ChatID), slog.Any("error", err))

			continue
		}

		log.InfoContext(ctx, "a turn the restart cut", slog.String("event", "coordinator.cut_turn"),
			slog.String("chat_id", c.ChatID), slog.Bool("continued", fresh), slog.Duration("age", c.Age),
			slog.Int("attempts", c.Attempts))
	}
}

// continued says whether a message is the line that wakes a chat to continue its cut turn.
func continued(m orchestration.ChatMessage) bool {
	return m.Role == orchestration.ChatSystem && m.Kind == "" && m.Text == orchestration.ContinuedText
}
