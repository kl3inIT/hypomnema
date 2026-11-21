package coordinator

import (
	"context"
	"log/slog"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
)

// A Bot's background commands (issue #1058, orchestration/background.go) run in its pi's box, which ends them when
// pi ends (--unshare-pid, --die-with-parent). So the supervisor keeps a pi that runs one, however long it is idle, and
// reports lost the ones a pi had when it stopped, as OpenClaw's process owner keeps a backgrounded command until it
// exits and reports the ones the last Gateway lost (docs/tools/exec.md:191; src/agents/bash-process-durable.ts:
// 132-161, 196-225).

// lostOnStop and lostOnRestart say why a command was lost, in its chat's wake-up.
const (
	lostOnStop    = "this chat's pi stopped"
	lostOnRestart = "the control plane restarted"
)

// loseBackground reports lost the background commands of a pi that exited. A pi stopped because the control plane
// stops leaves them for its next start to report (loseAllBackground), as its wake-ups wait for it anyway.
func (s *Supervisor) loseBackground(ctx context.Context, p *proc) {
	token := p.gatewayKey().token
	if s.stopping.Load() || s.shuttingDown() || token == "" {
		return
	}

	n, err := s.svc.LoseBackground(context.WithoutCancel(ctx), p.chatID, bearertoken.Hash(token), lostOnStop)
	if err != nil {
		s.cfg.Logger.ErrorContext(ctx, "reporting a pi's lost background commands failed",
			slog.String("event", "coordinator.background_lost_failed"), slog.String("chat_id", p.chatID),
			slog.Any("error", err))

		return
	}

	if n > 0 {
		s.cfg.Logger.InfoContext(ctx, "a pi stopped with background commands running",
			slog.String("event", "coordinator.background_lost"), slog.String("chat_id", p.chatID), slog.Int("commands", n))
	}
}

// loseAllBackground runs once, as the supervisor starts and before any pi does: every command still recorded was
// running in a box that the last control plane ended.
func (s *Supervisor) loseAllBackground(ctx context.Context) {
	n, err := s.svc.LoseBackground(ctx, "", "", lostOnRestart)
	if err != nil {
		s.cfg.Logger.ErrorContext(ctx, "reporting the background commands a restart ended failed",
			slog.String("event", "coordinator.background_lost_failed"), slog.Any("error", err))

		return
	}

	if n > 0 {
		s.cfg.Logger.InfoContext(ctx, "a restart ended background commands",
			slog.String("event", "coordinator.background_lost"), slog.Int("commands", n))
	}
}

// runningBackground lists the chats whose pi runs a background command, which reapIdle keeps; false, and nothing is
// reaped, when it cannot be read.
func (s *Supervisor) runningBackground(ctx context.Context) ([]string, bool) {
	ids, err := s.svc.ChatsWithBackground(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.cfg.Logger.ErrorContext(ctx, "listing chats with background commands failed",
				slog.String("event", "coordinator.background_list_failed"), slog.Any("error", err))
		}

		return nil, false
	}

	return ids, true
}
