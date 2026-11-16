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

	"github.com/kl3inIT/hypomnema/internal/controlplane/messaging"
)

const (
	// defaultBackfill and maxBackfill are how many older messages one request asks for (wacli --count).
	defaultBackfill = 50
	maxBackfill     = 500
	// backfillWait is how long the phone has to answer (wacli --wait).
	backfillWait = 60 * time.Second
)

// backfilled is the phone's answer for a chat: whether it said the chat has no older messages.
type backfilled struct{ complete bool }

// Backfill asks the phone for older messages of a chat and answers how many were added, counted in the mirror
// before and after as wacli counts MessagesAdded: the phone's answer may repeat messages already kept (issue #651).
func (a *account) Backfill(ctx context.Context, chat string, count int) (messaging.Backfilled, error) {
	switch {
	case count <= 0:
		count = defaultBackfill
	case count > maxBackfill:
		count = maxBackfill
	}

	jid, err := types.ParseJID(chat)
	if err != nil {
		return messaging.Backfilled{}, fmt.Errorf("whatsapp: chat %q: %w", chat, err)
	}

	anchor, err := a.mirror.oldest(ctx, a.mirrorKey(), chat)
	if err != nil {
		return messaging.Backfilled{}, err
	}

	before, err := a.mirror.count(ctx, a.mirrorKey(), chat)
	if err != nil {
		return messaging.Backfilled{}, err
	}

	info := &types.MessageInfo{
		MessageSource: types.MessageSource{Chat: jid, IsFromMe: anchor.fromMe},
		ID:            anchor.id, Timestamp: anchor.at,
	}

	answered := make(chan backfilled, 1)

	a.waitMu.Lock()
	a.waiting[chat] = answered
	a.waitMu.Unlock()

	defer func() {
		a.waitMu.Lock()
		delete(a.waiting, chat)
		a.waitMu.Unlock()
	}()

	if _, err := a.client.SendPeerMessage(ctx, a.client.BuildHistorySyncRequest(info, count)); err != nil {
		return messaging.Backfilled{}, fmt.Errorf("whatsapp: asking the phone for older messages: %w", err)
	}

	var got backfilled

	select {
	case got = <-answered:
	case <-time.After(backfillWait):
		return messaging.Backfilled{}, errors.New("whatsapp: the phone did not answer in a minute; it must be " +
			"online, with WhatsApp open")
	case <-ctx.Done():
		return messaging.Backfilled{}, ctx.Err()
	}

	after, err := a.mirror.count(ctx, a.mirrorKey(), chat)
	if err != nil {
		return messaging.Backfilled{}, err
	}

	return messaging.Backfilled{Added: after - before, Complete: got.complete}, nil
}

// answered tells a waiting Backfill that an on-demand history sync of its chat was kept, and whether the phone
// said the chat has no older messages (wacli: COMPLETE_AND_NO_MORE_MESSAGE_REMAIN_ON_PRIMARY).
func (a *account) answered(sync *waHistorySync.HistorySync, chat string, conv *waHistorySync.Conversation) {
	if sync.GetSyncType() != waHistorySync.HistorySync_ON_DEMAND {
		return
	}

	a.waitMu.Lock()
	ch := a.waiting[chat]
	a.waitMu.Unlock()

	if ch != nil {
		select {
		case ch <- backfilled{complete: conv.GetEndOfHistoryTransferType() ==
			waHistorySync.Conversation_COMPLETE_AND_NO_MORE_MESSAGE_REMAIN_ON_PRIMARY}:
		default:
		}
	}
}
