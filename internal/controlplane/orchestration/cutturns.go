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
// so only the control plane's death leaves one. At start the coordinator reads the marks, wakes each fresh chat to
// continue its turn with one line the owner sees, and clears the others with a line that says the turn was cut. The
// partial transcript stays as it was kept.

// Lines a chat gets when the control plane starts and finds its turn cut.
const (
	// ContinuedText is the line of a chat woken to continue its turn; pi is given it too, as a wake-up.
	ContinuedText = "Restarted for an update; continuing."
	// CutText is the line of a chat whose cut turn is not continued; the owner's next message goes on from there.
	CutText = "A restart cut this turn; send a message to continue."
)

// CutTurn is a chat whose turn was marked and never settled.
type CutTurn struct {
	ChatID string
	// Age is how long ago the turn started, or was last woken to continue.
	Age time.Duration
	// Attempts is how many times the chat was woken to continue this turn.
	Attempts int
}

// MarkChatTurn marks that a Bot's turn started in the chat.
func (s *Service) MarkChatTurn(ctx context.Context, chatID string) error {
	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		return q.MarkChatTurn(ctx, store.MarkChatTurnParams{ID: chatID, Now: sql.NullInt64{Int64: now, Valid: true}})
	}); err != nil {
		return fmt.Errorf("marking the turn of chat %s: %w", chatID, err)
	}

	return nil
}

// SettleChatTurn clears the chat's mark: its turn ended.
func (s *Service) SettleChatTurn(ctx context.Context, chatID string) error {
	if err := s.write(ctx, func(q *store.Queries, _ int64) error {
		return q.SettleChatTurn(ctx, chatID)
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
		out = append(out, CutTurn{
			ChatID: r.ID, Age: now.Sub(time.UnixMilli(r.TurnStartedAt)), Attempts: int(r.TurnAttempts),
		})
	}

	return out, nil
}

// ContinueCutTurn wakes the chat to continue its cut turn: one attempt more, the mark from now, and ContinuedText in
// the chat for pi, in one write.
func (s *Service) ContinueCutTurn(ctx context.Context, chatID string) error {
	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		if err := q.ContinueChatTurn(ctx, store.ContinueChatTurnParams{
			ID: chatID, Now: sql.NullInt64{Int64: now, Valid: true},
		}); err != nil {
			return err
		}

		return notice(ctx, q, chatID, ContinuedText, true, now)
	}); err != nil {
		return fmt.Errorf("waking chat %s to continue its turn: %w", chatID, err)
	}

	return nil
}

// EndCutTurn clears the chat's mark and says in the chat that a restart cut its turn, in one write.
func (s *Service) EndCutTurn(ctx context.Context, chatID string) error {
	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		if err := q.SettleChatTurn(ctx, chatID); err != nil {
			return err
		}

		return notice(ctx, q, chatID, CutText, false, now)
	}); err != nil {
		return fmt.Errorf("ending the cut turn of chat %s: %w", chatID, err)
	}

	return nil
}

func notice(ctx context.Context, q *store.Queries, chatID, text string, forPi bool, now int64) error {
	var to int64
	if forPi {
		to = 1
	}

	if _, err := q.InsertChatNotice(ctx, store.InsertChatNoticeParams{
		ChatID: chatID, Text: text, ToCoordinator: to, Now: now,
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
