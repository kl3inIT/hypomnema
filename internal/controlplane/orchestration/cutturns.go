package orchestration

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
)

// A Bot's turn that a restart cut (issue #716), after Hermes's interrupted-turn markers (tui_gateway/turn_marker.py,
// session_auto_continue.py): the chat is marked when its pi starts a turn and the mark goes when the turn settles,
// so only the control plane's death leaves one; as it begins to stop it writes when it cut each (issue #1162). At
// start the coordinator reads the marks, wakes each fresh chat to continue its turn with one line the owner sees,
// and clears the others with a line that says the turn was cut. The partial transcript stays as it was kept.

// Lines a chat gets when the control plane starts and finds its turn cut, of kinds ChatContinued and ChatCut.
const (
	// ContinuedText is the line of a chat woken to continue its turn; pi is given it too.
	ContinuedText = "Restarted for an update; continuing."
	// CutText is the line of a chat whose cut turn is not continued; the owner's next message goes on from there.
	CutText = "A restart cut this turn; send a message to continue."
)

// CutTurn is a chat whose turn was marked and never settled.
type CutTurn struct {
	ChatID string
	// Age is how long ago the control plane began to stop and cut the turn (issue #1162). When it wrote no cut (it
	// crashed), how long ago the turn started, or was last woken to continue.
	Age time.Duration
	// Cut says Age is measured from the cut, not from the turn's start.
	Cut bool
	// Attempts is how many times the chat was woken to continue this turn.
	Attempts int
}

// MarkChatTurn marks that a Bot's turn started in the chat, in the pi process token names.
func (s *Service) MarkChatTurn(ctx context.Context, chatID, token string) error {
	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		return q.MarkChatTurn(ctx, store.MarkChatTurnParams{
			ID: chatID, Now: sql.NullInt64{Int64: now, Valid: true}, Token: nullString(token),
		})
	}); err != nil {
		return fmt.Errorf("marking the turn of chat %s: %w", chatID, err)
	}

	return nil
}

// SettleChatTurn clears the chat's mark when it is the turn of the pi process token names: that turn ended. A
// newer turn's mark, of another pi, stays, as Hermes clears a turn marker by compare-and-swap
// (gateway/session_lifecycle.py clear_active_turn_marker).
func (s *Service) SettleChatTurn(ctx context.Context, chatID, token string) error {
	if err := s.write(ctx, func(q *store.Queries, _ int64) error {
		return q.SettleChatTurn(ctx, store.SettleChatTurnParams{ID: chatID, Token: nullString(token)})
	}); err != nil {
		return fmt.Errorf("clearing the turn mark of chat %s: %w", chatID, err)
	}

	return nil
}

// CutTurns lists the chats whose turn is marked.
func (s *Service) CutTurns(ctx context.Context) ([]CutTurn, error) {
	rows, err := s.read().CutChatTurns(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the marked turns: %w", err)
	}

	now := s.clock.Now()
	out := make([]CutTurn, 0, len(rows))

	for _, r := range rows {
		from := r.TurnStartedAt
		if r.TurnCutAt.Valid {
			from = r.TurnCutAt.Int64
		}

		out = append(out, CutTurn{
			ChatID: r.ID, Age: now.Sub(time.UnixMilli(from)), Cut: r.TurnCutAt.Valid, Attempts: int(r.TurnAttempts),
		})
	}

	return out, nil
}

// CutMarkedTurns writes that every marked turn is cut now (issue #1162): the control plane begins to stop, and this
// is written before it waits for the turns, as Hermes's gateway marks its running sessions resume-pending before its
// drain wait so that a process killed during the drain still leaves the marker (gateway/run_shutdown.py
// _mark_running_sessions_resume_pending). A turn that settles during the wait clears it with its mark.
func (s *Service) CutMarkedTurns(ctx context.Context) error {
	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		return q.CutMarkedTurns(ctx, sql.NullInt64{Int64: now, Valid: true})
	}); err != nil {
		return fmt.Errorf("cutting the marked turns: %w", err)
	}

	return nil
}

// ContinueCutTurn wakes the chat to continue its cut turn: one attempt more, the mark from now, and ContinuedText in
// the chat for pi, in one write. A line of an earlier start that pi never had goes to pi no more.
func (s *Service) ContinueCutTurn(ctx context.Context, chatID string) error {
	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		if err := q.ContinueChatTurn(ctx, store.ContinueChatTurnParams{
			ID: chatID, Now: sql.NullInt64{Int64: now, Valid: true},
		}); err != nil {
			return err
		}

		return restartLine(ctx, q, chatID, ChatContinued, ContinuedText, now)
	}); err != nil {
		return fmt.Errorf("waking chat %s to continue its turn: %w", chatID, err)
	}

	return nil
}

// EndCutTurn clears the chat's mark and says in the chat that a restart cut its turn, in one write. A line of an
// earlier start that woke pi to continue it, and that pi never had, goes to pi no more.
func (s *Service) EndCutTurn(ctx context.Context, chatID string) error {
	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		if err := q.ClearChatTurn(ctx, chatID); err != nil {
			return err
		}

		return restartLine(ctx, q, chatID, ChatCut, CutText, now)
	}); err != nil {
		return fmt.Errorf("ending the cut turn of chat %s: %w", chatID, err)
	}

	return nil
}

// HoldCutTurns keeps every cut turn as it is and wakes none, when restarts are in a loop: a line of an earlier start
// that woke pi to continue one, and that pi never had, goes to pi no more.
func (s *Service) HoldCutTurns(ctx context.Context) error {
	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		return q.VoidContinuations(ctx, sql.NullInt64{Int64: now, Valid: true})
	}); err != nil {
		return fmt.Errorf("holding the cut turns: %w", err)
	}

	return nil
}

func restartLine(ctx context.Context, q *store.Queries, chatID string, kind ChatKind, text string, now int64) error {
	if err := q.VoidChatContinuations(ctx, store.VoidChatContinuationsParams{
		ChatID: chatID, Now: sql.NullInt64{Int64: now, Valid: true},
	}); err != nil {
		return err
	}

	var forPi int64
	if kind == ChatContinued {
		forPi = 1
	}

	if _, err := q.InsertChatRestartLine(ctx, store.InsertChatRestartLineParams{
		ChatID: chatID, Kind: string(kind), Text: text, ToCoordinator: forPi, Now: now,
	}); err != nil {
		return err
	}

	return q.TouchChat(ctx, store.TouchChatParams{ID: chatID, Now: now})
}

// maxRestartBoots bounds the starts kept: only the newest few decide, as Hermes keeps 50
// (gateway/restart_loop_guard.py, _MAX_STORED_BOOTS).
const maxRestartBoots = 50

// RecordRestartBoot records a start that found cut turns and returns how many such starts, this one included, are
// chained with no gap wider than gap (Hermes's restart-loop guard, gateway/restart_loop_guard.py
// _chain_ending_at): the first wider gap ends the chain, and the starts before it are forgotten.
func (s *Service) RecordRestartBoot(ctx context.Context, gap time.Duration) (int, error) {
	chained := 0

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		boots, err := q.ListRestartBoots(ctx)
		if err != nil {
			return err
		}

		chain := chainEndingAt(boots, now, gap.Milliseconds())
		chain = append(chain, now)

		if len(chain) > maxRestartBoots {
			chain = chain[len(chain)-maxRestartBoots:]
		}

		if err := q.DeleteRestartBoots(ctx); err != nil {
			return err
		}

		for _, at := range chain {
			if err := q.InsertRestartBoot(ctx, at); err != nil {
				return err
			}
		}

		chained = len(chain)

		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("recording a restart: %w", err)
	}

	return chained, nil
}

// chainEndingAt is the unbroken chain of boots, newest first in, leading up to now, oldest first out: it walks back
// while each gap stays within gap. A boot after now (the clock went back) is adjacent, not a break, as Hermes's.
func chainEndingAt(newestFirst []int64, now, gap int64) []int64 {
	var chain []int64

	prev := now

	for _, at := range newestFirst {
		if at > now {
			chain = append(chain, at)

			continue
		}

		if prev-at > gap {
			break
		}

		chain = append(chain, at)
		prev = at
	}

	slices.Reverse(chain)

	return chain
}
