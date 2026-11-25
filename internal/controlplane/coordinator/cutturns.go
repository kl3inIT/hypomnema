package coordinator

import (
	"context"
	"log/slog"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// A turn a restart cut continues by itself (issue #716), as Hermes's gateway auto-continues an interrupted session
// (gateway/run_startup.py, gateway/restart_loop_guard.py): each turn is marked in the store while it runs
// (orchestration/cutturns.go), the control plane writes when it cut each as it begins to stop (issue #1162), and at
// start the supervisor wakes each chat whose cut is fresh to go on with it.
const (
	// continueFreshFor is how recent a cut turn must be to continue: an hour from the cut, or from the turn's start
	// when no cut was written (a crash), as Hermes's gateway (gateway/run.py _AUTO_CONTINUE_FRESHNESS_SECS_DEFAULT,
	// from the resume-pending mark; session_lifecycle.py recover_interrupted_turns, from the turn's start).
	continueFreshFor = time.Hour
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
// (session_auto_continue.py _auto_continue_note): pi's session holds the request and what the cut turn had done. A
// command the cut turn ran on a node goes on there, and its id went with the cut turn's tools server: node_process
// lists the chat's own (issue #1060, Coder's process list). A command of pi's own bash ended with pi, unless it ran
// with background, whose unit outlives the restart and whose end wakes the chat (issue #1084): the notice says which,
// as #1060's says what became of a node's command, and as OpenClaw tells which commands a restart stopped and which
// go on (docs/gateway/background-process.md:58).
const continuedPrompt = "The control plane restarted for an update and cut your previous turn before it could " +
	"finish. Some of the work may already be complete; check the current state before redoing anything, then " +
	"finish the task. A command your bash was running was stopped with the restart and gave no result; a command " +
	"you started with background: true goes on, and its end wakes you. A command you ran on a node may still run " +
	"there or have ended: list yours with node_process given only the node before running it again."

// cutTurns writes that every marked turn is cut now: the control plane begins to stop, at the update's begin or when
// the supervisor stops every pi, before it waits for the turns, as Hermes's gateway marks its running sessions
// before its drain (gateway/run_shutdown.py _mark_running_sessions_resume_pending). A write that fails is logged and
// the stop goes on, as Hermes's: a turn with no cut is continued within the hour of its start.
func (s *Supervisor) cutTurns(ctx context.Context) {
	if err := s.svc.CutMarkedTurns(context.WithoutCancel(ctx)); err != nil {
		s.cfg.Logger.ErrorContext(ctx, "writing the cut of the turns failed",
			slog.String("event", "coordinator.turn_mark_failed"), slog.Any("error", err))
	}
}

// turnStarted marks the chat's turn in the store as p's. The write outlives the request that runs it: a turn that
// starts as the control plane stops is still marked.
func (s *Supervisor) turnStarted(ctx context.Context, p *proc) {
	if err := s.svc.MarkChatTurn(context.WithoutCancel(ctx), p.chatID, p.token); err != nil {
		s.cfg.Logger.ErrorContext(ctx, "marking a turn failed", slog.String("event", "coordinator.turn_mark_failed"),
			slog.String("chat_id", p.chatID), slog.Any("error", err))
	}
}

// turnSettled clears the chat's mark if it is p's turn: it ended, however it ended. The mark of a newer turn, of
// another pi of the chat, stays.
func (s *Supervisor) turnSettled(ctx context.Context, p *proc) {
	if err := s.svc.SettleChatTurn(context.WithoutCancel(ctx), p.chatID, p.token); err != nil {
		s.cfg.Logger.ErrorContext(ctx, "clearing a turn mark failed",
			slog.String("event", "coordinator.turn_mark_failed"), slog.String("chat_id", p.chatID),
			slog.Any("error", err))
	}
}

// exitedMidTurn is called when a chat's pi exits. While the control plane runs, a pi that exits ended its turn,
// which the next message starts again; its mark is cleared, and only its own. When the control plane stops it, the
// mark stays: that is the cut turn the next start continues. pi gets the same SIGTERM as the control plane on
// `systemctl stop`, so both the stop and the signal's context (Run's, whatever context pi reads with) say it is
// stopping.
func (s *Supervisor) exitedMidTurn(ctx context.Context, p *proc) {
	if ctx.Err() != nil || s.stopping.Load() || s.shuttingDown() {
		return
	}

	s.turnSettled(ctx, p)
}

// shuttingDown says Run's context ended: the control plane stops. The context pi reads with is not always Run's: one
// a compaction started has a context of its own, which the stop does not end.
func (s *Supervisor) shuttingDown() bool {
	ctx := s.running.Load()

	return ctx != nil && (*ctx).Err() != nil
}

// continueCutTurns runs once, as the supervisor starts. A start that finds no cut turn records nothing. One that
// does is recorded for the restart-loop guard; while it trips, every mark stays as it is and nothing is woken, as
// Hermes skips the auto-resume and keeps the sessions resume-pending (gateway/run_restored_delivery.py). Otherwise a
// fresh cut with attempts left is continued and any other is cleared with a line saying the turn was cut.
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

		// A line an earlier start wrote to continue a turn, which pi never had, is not given now either.
		if err := s.svc.HoldCutTurns(ctx); err != nil {
			log.ErrorContext(ctx, "holding the cut turns failed",
				slog.String("event", "coordinator.cut_turns_failed"), slog.Any("error", err))
		}

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

		from := "start"
		if c.Cut {
			from = "cut"
		}

		log.InfoContext(ctx, "a turn the restart cut", slog.String("event", "coordinator.cut_turn"),
			slog.String("chat_id", c.ChatID), slog.Bool("continued", fresh), slog.Duration("age", c.Age),
			slog.String("age_from", from), slog.Int("attempts", c.Attempts))
	}
}

// continued says whether a message is the line that wakes a chat to continue its cut turn.
func continued(m orchestration.ChatMessage) bool {
	return m.Kind == orchestration.ChatContinued
}
