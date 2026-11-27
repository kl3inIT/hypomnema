package coordinator

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// A steer counts as delivered once pi has read it (issue #1172), as t3code's pi adapter tracks it
// (packages/provider-pi/src/server/adapter.ts): pi answers a steer `queued` and reads it later, after the tool calls
// that run. A user message_start reads the oldest one; when the turn settles without a stop pi has read the rest,
// since pi takes in every queued steer before it settles (pi agent-session.js _startRun). The owner's stop clears pi's
// queue before the abort (live.go), so a steer pi had not read is dropped, and its row, still undelivered, goes as the
// next turn, as OpenClaw and Hermes give an unread steer again (OpenClaw attempt-queue-message.ts, Hermes
// agent/turn_finalizer.py). A pi that exits takes its queue with it, and its rows stay undelivered for the next pi.
// The rows are the store's; what a proc holds of them ends with it.

// carry records the rows a prompt command carries until pi answers it: a steer pi queues is unread until it reads it.
// The rows count as given to this pi from now on, so that a steer is never sent twice. Called with p.mu held.
func (p *proc) carry(id string, seqs []int64) {
	if p.carrying == nil {
		p.carrying = map[string][]int64{}
	}

	if p.given == nil {
		p.given = map[int64]bool{}
	}

	p.carrying[id] = seqs

	for _, seq := range seqs {
		p.given[seq] = true
	}
}

// uncarry ends what carry began, once the command is answered; the rows of one pi refused or never answered are
// not given, and go again. Called with p.mu held.
func (p *proc) uncarry(id string, given bool) {
	seqs := p.carrying[id]
	delete(p.carrying, id)

	if given {
		return
	}

	for _, seq := range seqs {
		delete(p.given, seq)
	}
}

// answered keeps a steer pi queued as unread, and takes the steers clear_queue removed from pi's queue as dropped.
// It runs in the reader, before any event that follows the answer. Called with p.mu held.
func (p *proc) answered(rec record) {
	if !rec.Success {
		return
	}

	if seqs, ok := p.carrying[rec.ID]; ok && dispositionOf(rec.Data) == "queued" {
		p.unread = append(p.unread, seqs)
	}

	if rec.Command == "clear_queue" {
		p.dropped = append(p.dropped, p.unread...)
		p.unread = nil
	}
}

// dispositionOf is what pi did with a prompt (rpc-commands.md): started a run, queued it or handled it.
func dispositionOf(data json.RawMessage) string {
	var d struct {
		Disposition string `json:"disposition"`
	}

	_ = json.Unmarshal(data, &d)

	return d.Disposition
}

// unsent leaves out the rows this pi has been given while it works, and everything while the owner's stop runs or
// once the turn is over: a turn that ends is given no steer, and what was written goes as a turn after it (t3code
// CommandPolicy.ts queues what comes while the provider's turn is not running). The pass that calls it read pi as
// working before; a steer its stop dropped, sent once the turn settled, would start a turn of its own ahead of the
// chat's queue, which goes with it as the owner's next turn instead (nextTurn, issue #1192).
func (p *proc) unsent(msgs []orchestration.ChatMessage) []orchestration.ChatMessage {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.stopAsked || !p.working {
		return nil
	}

	out := msgs[:0:0]

	for _, m := range msgs {
		if !p.given[m.Seq] {
			out = append(out, m)
		}
	}

	return out
}

// forgetGiven is called before an idle pi is given a turn: every row given it before has been read and marked, or
// dropped, by the time its turn settled.
func (p *proc) forgetGiven() {
	p.mu.Lock()
	p.given = nil
	p.mu.Unlock()
}

// steerRead marks the oldest unread steer delivered: pi took it in as a user message.
func (p *proc) steerRead(ctx context.Context) {
	p.mu.Lock()

	var seqs []int64
	if len(p.unread) > 0 {
		seqs, p.unread = p.unread[0], p.unread[1:]
	}

	p.mu.Unlock()

	p.markRead(ctx, seqs)
}

// settleSteers ends the turn's steers as it settles: pi read every one left unless the owner stopped the turn, whose
// clear_queue dropped them; a dropped one is no longer given, so it goes again as the next turn. It runs before pi
// reads as idle, so that no turn starts before the rows are marked.
func (p *proc) settleSteers(ctx context.Context) {
	p.mu.Lock()

	read, dropped := p.unread, p.dropped
	if p.stopAsked {
		read, dropped = nil, append(dropped, read...)
	}

	p.unread, p.dropped = nil, nil

	for _, seqs := range dropped {
		for _, seq := range seqs {
			delete(p.given, seq)
		}
	}

	p.mu.Unlock()

	var seqs []int64
	for _, s := range read {
		seqs = append(seqs, s...)
	}

	p.markRead(ctx, seqs)
}

// markRead marks rows pi read delivered. The write outlives the reader's context: pi read them all the same.
func (p *proc) markRead(ctx context.Context, seqs []int64) {
	if len(seqs) == 0 {
		return
	}

	if err := p.s.svc.MarkDeliveredSeqs(context.WithoutCancel(ctx), p.chatID, seqs); err != nil {
		p.s.cfg.Logger.ErrorContext(ctx, "marking a steer pi read failed",
			slog.String("event", "coordinator.deliver_failed"), slog.String("chat_id", p.chatID), slog.Any("error", err))
	}
}
