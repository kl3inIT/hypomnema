package coordinator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// How full a chat's context window is (issue #384) is pi's own report, as Orca
// and t3code show the agent's: after each of pi's turns and after a compaction
// the supervisor asks it get_session_stats (pi docs/rpc-commands.md, Session),
// as Orca's refreshUsage does on turn_end and on the end of a compaction
// (main/pi/rpc/rpc-turns.ts), keeps the report with the chat, and tells the
// chat's watchers when it changed. The owner compacts by hand with pi's RPC
// compact, as t3code's Compact (ChatView.tsx onCompactContext) and
// OpenMausBot's "compact now" (POST /api/bots/:id/compact, run as a turn of
// its own) do; pi's own compaction settings are kept.

// FrameUsage says how full the context window is: {context_window, tokens, percent}.
const FrameUsage = "usage"

// compactTimeout bounds a compaction: a model writes the summary.
// OpenMausBot gives its summary five minutes (WARM_SUMMARY_TIMEOUT_MS).
const compactTimeout = 5 * time.Minute

// compactFailedPrefix starts the row that says the owner's compaction failed.
const compactFailedPrefix = "pi's compaction failed: "

// UsageFrame is the data of a usage frame, the API's ChatContextUsage.
type UsageFrame struct {
	ContextWindow int64    `json:"context_window"`
	Tokens        *int64   `json:"tokens,omitempty"`
	Percent       *float64 `json:"percent,omitempty"`
}

// sessionStats is the part of pi's get_session_stats the supervisor reads. A
// pi without a model or a window leaves contextUsage out; tokens and percent
// are null right after a compaction.
type sessionStats struct {
	ContextUsage *struct {
		Tokens        *float64 `json:"tokens"`
		ContextWindow float64  `json:"contextWindow"`
		Percent       *float64 `json:"percent"`
	} `json:"contextUsage"`
}

// refreshUsage asks pi for its context usage and keeps it. It runs on its own
// goroutine: pi's answer is read by the reader that called it.
func (p *proc) refreshUsage(ctx context.Context) {
	go func() {
		p.usageMu.Lock()
		defer p.usageMu.Unlock()

		r, err := p.call(ctx, map[string]any{"type": "get_session_stats"})
		if err != nil {
			if !p.exited() && ctx.Err() == nil {
				p.s.cfg.Logger.WarnContext(ctx, "reading pi's context usage failed",
					slog.String("event", "coordinator.usage_failed"), slog.String("chat_id", p.chatID),
					slog.Any("error", err))
			}

			return
		}

		var stats sessionStats
		if json.Unmarshal(r.Data, &stats) != nil || stats.ContextUsage == nil || stats.ContextUsage.ContextWindow <= 0 {
			return
		}

		c := stats.ContextUsage
		u := orchestration.ContextUsage{Window: int64(c.ContextWindow), Percent: c.Percent}

		if c.Tokens != nil {
			t := int64(*c.Tokens)
			u.Tokens = &t
		}

		if same(p.usage, u) {
			return
		}

		if err := p.s.svc.SetChatContextUsage(ctx, p.chatID, u); err != nil {
			if ctx.Err() == nil {
				p.s.cfg.Logger.ErrorContext(ctx, "keeping pi's context usage failed",
					slog.String("chat_id", p.chatID), slog.Any("error", err))
			}

			return
		}

		p.usage = &u
		p.s.publish(p.chatID, Frame{Event: FrameUsage, Data: UsageFrame{
			ContextWindow: u.Window, Tokens: u.Tokens, Percent: u.Percent,
		}})
	}()
}

// same says whether a report is the one kept last by this pi.
func same(last *orchestration.ContextUsage, u orchestration.ContextUsage) bool {
	eq := func(a, b *int64) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }
	eqf := func(a, b *float64) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }

	return last != nil && last.Window == u.Window && eq(last.Tokens, u.Tokens) && eqf(last.Percent, u.Percent)
}

// Compact asks the chat's pi to compact its context now (issue #384), starting
// pi when none runs: pi takes its session again. The compaction runs on as a
// turn of its own: the chat reads working until it ends, a message sent
// meanwhile waits for it, and Abort stops it. pi aborts a running turn to
// compact, so a chat whose pi works is refused.
func (s *Supervisor) Compact(ctx context.Context, chatID string) error {
	if !s.Enabled() {
		return ErrDisabled
	}

	// A pi started here outlives the request: it keeps what it does with the context it is started with.
	p, err := s.procFor(context.WithoutCancel(ctx), chatID)
	if err != nil {
		return err
	}

	if !p.beginCompact() {
		return fmt.Errorf("%w: pi is working on chat %s; compact when it is done", orchestration.ErrConflict, chatID)
	}

	p.setWorking(true)

	go p.compact(context.WithoutCancel(ctx))

	return nil
}

// compact runs pi's compact and ends the turn it makes: a compaction the owner
// stopped ends in a stopped row, a failed one in a failed row, as t3code keeps
// a failed compaction in the thread. pi's compaction_end refreshes the usage.
func (p *proc) compact(ctx context.Context) {
	_, err := p.callWithin(ctx, map[string]any{"type": "compact"}, compactTimeout)

	switch {
	case p.stopping():
		p.keepMessages(ctx, orchestration.NewChatMessage{
			Role: orchestration.ChatSystem, Kind: orchestration.ChatStopped, Text: stoppedText,
		})
	case err != nil:
		reason := strings.TrimPrefix(err.Error(), "pi refused compact: ")
		p.keepMessages(ctx, orchestration.NewChatMessage{
			Role: orchestration.ChatSystem, Kind: orchestration.ChatFailed, Text: compactFailedPrefix + reason,
		})
	}

	p.stopSettled()

	p.mu.Lock()
	p.compacting = false
	p.mu.Unlock()

	p.setWorking(false)
	p.s.settled()
}

// beginCompact marks a compaction as running, unless pi works or compacts.
func (p *proc) beginCompact() bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.working || p.compacting {
		return false
	}

	p.compacting = true

	return true
}

func (p *proc) isCompacting() bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.compacting
}
