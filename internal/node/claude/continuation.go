package claude

import (
	"context"
	"time"

	"github.com/kl3inIT/hypomnema/internal/node/runner"
	"github.com/kl3inIT/hypomnema/internal/node/shim"
)

const (
	settleWait     = 2 * time.Second
	staleWorkGrace = 5 * time.Second
	closeGrace     = 30 * time.Second
)

type continuation struct {
	Held    bool      `json:"held,omitempty"`
	Active  bool      `json:"active,omitempty"`
	Running bool      `json:"running,omitempty"`
	Idle    bool      `json:"idle,omitempty"`
	IdleAt  time.Time `json:"idle_at,omitzero"`
	CloseBy time.Time `json:"close_by,omitzero"`
	Failure string    `json:"failure,omitempty"`
}

func (t *turn) armIdle(now time.Time) {
	staleHints := t.result != nil && t.result.Queued > 0 || len(t.toolCalls) > 0
	if !t.wait.Held || !t.wait.CloseBy.IsZero() || t.background || t.result == nil ||
		t.wait.Active || t.wait.Running || (staleHints && !t.wait.Idle) {
		t.wait.IdleAt = time.Time{}
	} else if t.wait.IdleAt.IsZero() {
		t.wait.IdleAt = now.Add(settleWait)
		if staleHints {
			t.wait.IdleAt = now.Add(staleWorkGrace)
		}
	}
}

func (t *turn) closeInput(ctx context.Context, w *runner.Worker, now time.Time) {
	if t.wait.Held && t.wait.CloseBy.IsZero() {
		t.wait.CloseBy = now.Add(t.closeGrace)
	}
	w.Report(ctx, w.Send(ctx, []byte(shim.CloseLine)))
}

func (t *turn) Poll(ctx context.Context, w *runner.Worker, caughtUp bool) (bool, string) {
	now := time.Now()
	reason := t.wait.Failure
	switch {
	case reason != "":
		return false, reason
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
