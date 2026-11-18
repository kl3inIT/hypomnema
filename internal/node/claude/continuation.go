package claude

import (
	"context"
	"time"

	"github.com/kl3inIT/hypomnema/internal/node/runner"
	"github.com/kl3inIT/hypomnema/internal/node/shim"
)

const (
	settleWait      = 2 * time.Second
	runningWait     = 10 * time.Minute
	closeGrace      = 30 * time.Second
	maxContinuation = 4 * time.Hour
)

type continuation struct {
	Active  bool      `json:"active,omitempty"`
	Running bool      `json:"running,omitempty"`
	IdleAt  time.Time `json:"idle_at,omitzero"`
	HardAt  time.Time `json:"hard_at,omitzero"`
	CloseBy time.Time `json:"close_by,omitzero"`
	Failure string    `json:"failure,omitempty"`
}

func (t *turn) armIdle(now time.Time) {
	if t.wait.HardAt.IsZero() || !t.wait.CloseBy.IsZero() || t.background || t.wait.Active || len(t.toolCalls) > 0 {
		t.wait.IdleAt = time.Time{}
	} else if t.wait.IdleAt.IsZero() {
		wait := settleWait
		if t.wait.Running {
			wait = runningWait
		}
		t.wait.IdleAt = now.Add(wait)
	}
}

func (t *turn) closeInput(ctx context.Context, w *runner.Worker, now time.Time) {
	if !t.wait.HardAt.IsZero() && t.wait.CloseBy.IsZero() {
		t.wait.CloseBy = now.Add(closeGrace)
	}
	w.Report(ctx, w.Send(ctx, []byte(shim.CloseLine)))
}

func (t *turn) Poll(ctx context.Context, w *runner.Worker, caughtUp bool) (bool, string) {
	now := time.Now()
	reason := t.wait.Failure
	switch {
	case reason != "":
		return false, reason
	case !t.wait.HardAt.IsZero() && !now.Before(t.wait.HardAt):
		reason = "Claude Code exceeded the background continuation deadline"
	case !t.wait.CloseBy.IsZero() && !now.Before(t.wait.CloseBy):
		reason = "Claude Code did not exit after its input was closed"
	case caughtUp && !t.wait.IdleAt.IsZero() && !now.Before(t.wait.IdleAt):
		t.closeInput(ctx, w, now)
		t.armIdle(now)
		return true, ""
	default:
		return false, ""
	}
	t.wait.Failure = reason
	return true, reason
}
