package whatsapp

// Older messages of one chat, asked of the phone (issue #634; owner, 2026-11-16), as wacli's history backfill does
// (internal/app/backfill.go, docs/history.md): the anchor is the oldest message kept in the chat, one request asks
// for up to 500 before it (50 by default), and the phone answers with an on-demand history sync, which the mirror
// keeps as any history. The phone must be online; it decides what it sends. wacli retries a direct chat under its
// other identity (LID or number) when the phone does not answer; that is left out here.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/types"
)

const (
	// defaultBackfill and maxBackfill are how many older messages one request asks for (wacli --count).
	defaultBackfill = 50
	maxBackfill     = 500
	// backfillWait is how long the phone has to answer (wacli --wait).
	backfillWait = 60 * time.Second
)

// Backfill asks the phone for older messages of a chat and answers how many it sent.
func (a *account) Backfill(ctx context.Context, chat string, count int) (int, error) {
	switch {
	case count <= 0:
		count = defaultBackfill
	case count > maxBackfill:
		count = maxBackfill
	}

	jid, err := types.ParseJID(chat)
	if err != nil {
		return 0, fmt.Errorf("whatsapp: chat %q: %w", chat, err)
	}

	anchor, err := a.mirror.oldest(ctx, a.mirrorKey(), chat)
	if err != nil {
		return 0, err
	}

	info := &types.MessageInfo{
		MessageSource: types.MessageSource{Chat: jid, IsFromMe: anchor.fromMe},
		ID:            anchor.id, Timestamp: anchor.at,
	}

	answered := make(chan int, 1)

	a.waitMu.Lock()
	a.waiting[chat] = answered
	a.waitMu.Unlock()

	defer func() {
		a.waitMu.Lock()
		delete(a.waiting, chat)
		a.waitMu.Unlock()
	}()

	if _, err := a.client.SendPeerMessage(ctx, a.client.BuildHistorySyncRequest(info, count)); err != nil {
		return 0, fmt.Errorf("whatsapp: asking the phone for older messages: %w", err)
	}

	select {
	case n := <-answered:
		return n, nil
	case <-time.After(backfillWait):
		return 0, errors.New("whatsapp: the phone did not answer in a minute; it must be online, with WhatsApp open")
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

// answered tells a waiting Backfill how many older messages of its chat an on-demand history sync brought.
func (a *account) answered(sync *waHistorySync.HistorySync, chat string, n int) {
	if sync.GetSyncType() != waHistorySync.HistorySync_ON_DEMAND {
		return
	}

	a.waitMu.Lock()
	ch := a.waiting[chat]
	a.waitMu.Unlock()

	if ch != nil {
		select {
		case ch <- n:
		default:
		}
	}
}
