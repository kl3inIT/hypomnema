package zalo

// The past of every chat and the names of direct chats (issue #669, step 2 of #613). After each connection the
// account walks back once through what Zalo still holds, where it stopped before:
//
//   - each group's past with zca-js's getCloudMessage, 50 messages a page, back to when the owner joined
//     (src/apis/getCloudMessage.ts: lastMsgId, isOld, hasMore);
//   - the direct chats' past with the listener's old-messages request (cmd 510), a queue of every direct chat at
//     once paged by the oldest actionId, until a page brings nothing new (zca-js listen.ts; softtyos
//     realtime/handlers/message.handler.ts).
//
// Pages are a little apart, so that the walk is not a burst of requests on the owner's account. History asks one
// more page of a group now; of a direct chat, it walks the queue to its end.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/messaging"
)

const (
	// pageGap is the wait between two pages of the walk (Service.gap). No reference paces it; one page every two
	// seconds keeps the walk of a few thousand pages within hours.
	pageGap = 2 * time.Second
	// maxQueuePages bounds a walk of the direct chats' queue, should Zalo answer the same page again and again.
	maxQueuePages = 500
	// namesBatch is how many people's names one request asks.
	namesBatch = 50
)

// errNotConnected is asking Zalo while the session has no bridge.
var errNotConnected = errors.New("zalo: the session is not connected; try again when it is")

// catchUp runs after the listener started and the groups were named: the files left, then the past.
func (a *account) catchUp(ctx context.Context, b *bridge) {
	a.backfillMedia(ctx)

	a.pastMu.Lock()
	defer a.pastMu.Unlock()

	if err := a.walkQueue(ctx, b); err != nil {
		a.svc.log.WarnContext(ctx, "zalo: the direct chats' past could not be read", slog.String("error", err.Error()))
	}

	// The direct chats the queue brought are named too.
	a.nameDirectChats(ctx, b)

	if err := a.walkGroups(ctx, b); err != nil {
		a.svc.log.WarnContext(ctx, "zalo: the groups' past could not be read", slog.String("error", err.Error()))
	}
}

// page is one page of the past, as the bridge answers it.
type page struct {
	Messages  []incoming `json:"messages"`
	LastMsgID string     `json:"lastMsgId"`
	IsOld     bool       `json:"isOld"`
	More      bool       `json:"more"`
	Next      string     `json:"next"`
}

// keepPage keeps a page's messages and answers how many were new.
func (a *account) keepPage(ctx context.Context, p page) int {
	added := 0

	for _, m := range p.Messages {
		if a.keepMessage(ctx, m) {
			added++
		}
	}

	return added
}

// walkGroups walks every group not walked to its start yet, a page at a time.
func (a *account) walkGroups(ctx context.Context, b *bridge) error {
	groups, err := a.svc.mirror.groupsToWalk(ctx, a.uid)
	if err != nil {
		return err
	}

	for _, w := range groups {
		for !w.done {
			if w, _, err = a.groupPage(ctx, b, w); err != nil {
				a.svc.log.WarnContext(ctx, "zalo: a group's past could not be read", slog.String("group", w.chat),
					slog.String("error", err.Error()))

				break
			}

			if !sleep(ctx, a.svc.gap) {
				return ctx.Err()
			}
		}
	}

	return nil
}

// groupPage reads one page of a group's past, keeps it and where the walk got to.
func (a *account) groupPage(ctx context.Context, b *bridge, w walk) (walk, int, error) {
	var p page
	if err := b.call(ctx, "groupHistory", map[string]any{"group": w.chat, "lastMsgId": w.cursor, "isOld": w.old},
		&p); err != nil {
		return w, 0, err
	}

	added := a.keepPage(ctx, p)
	w.cursor, w.old, w.done = p.LastMsgID, p.IsOld, !p.More || p.LastMsgID == ""

	return w, added, a.svc.mirror.walked(ctx, a.uid, w)
}

// walkQueue walks the direct chats' queue from where it stopped, until a page brings nothing new.
func (a *account) walkQueue(ctx context.Context, b *bridge) error {
	cursor, done, err := a.svc.mirror.queue(ctx, a.uid)
	if err != nil || done {
		return err
	}

	for range maxQueuePages {
		var p page
		if err := b.call(ctx, "oldMessages", map[string]any{"lastId": cursor}, &p); err != nil {
			return err
		}

		added := a.keepPage(ctx, p)
		end := added == 0 || p.Next == "" || p.Next == cursor

		if !end {
			cursor = p.Next
		}

		if err := a.svc.mirror.queued(ctx, a.uid, cursor, end); err != nil || end {
			return err
		}

		if !sleep(ctx, a.svc.gap) {
			return ctx.Err()
		}
	}

	return a.svc.mirror.queued(ctx, a.uid, cursor, true)
}

// person is a name as the bridge answers it.
type person struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// nameDirectChats names the direct chats that have no name, after the person in each (zaloclaw resolveUserName).
func (a *account) nameDirectChats(ctx context.Context, b *bridge) {
	ids, err := a.svc.mirror.unnamed(ctx, a.uid)
	if err != nil {
		a.svc.log.WarnContext(ctx, "zalo: the direct chats to name could not be listed", slog.String("error", err.Error()))

		return
	}

	for i := 0; i < len(ids); i += namesBatch {
		var people []person
		if err := b.call(ctx, "users", map[string]any{"ids": ids[i:min(i+namesBatch, len(ids))]}, &people); err != nil {
			a.svc.log.WarnContext(ctx, "zalo: people's names could not be read", slog.String("error", err.Error()))

			return
		}

		for _, p := range people {
			if err := a.svc.mirror.nameChat(ctx, a.uid, p.ID, "dm", p.Name); err != nil {
				a.svc.log.WarnContext(ctx, "zalo: a direct chat's name could not be kept",
					slog.String("error", err.Error()))
			}
		}
	}
}

// Backfill asks Zalo for a chat's older messages: up to count of a group, a page at a time; for a direct chat, the
// rest of the direct chats' queue, of which it answers what this chat gained.
func (a *account) Backfill(ctx context.Context, chat string, count int) (messaging.Backfilled, error) {
	b := a.current()
	if b == nil {
		return messaging.Backfilled{}, errNotConnected
	}

	a.pastMu.Lock()
	defer a.pastMu.Unlock()

	w, err := a.svc.mirror.walkOf(ctx, a.uid, chat)
	if err != nil {
		return messaging.Backfilled{}, err
	}

	if a.svc.mirror.chatKind(ctx, a.uid, chat) != "group" {
		return a.backfillDirect(ctx, b, chat)
	}

	out := messaging.Backfilled{}

	for out.Added < max(count, 1) && !w.done {
		var added int
		if w, added, err = a.groupPage(ctx, b, w); err != nil {
			return out, fmt.Errorf("zalo: reading the past of %s: %w", chat, err)
		}

		out.Added += added
	}

	out.Complete = w.done

	return out, nil
}

func (a *account) backfillDirect(ctx context.Context, b *bridge, chat string) (messaging.Backfilled, error) {
	before, err := a.svc.mirror.chatMessages(ctx, a.uid, chat)
	if err != nil {
		return messaging.Backfilled{}, err
	}

	if err := a.walkQueue(ctx, b); err != nil {
		return messaging.Backfilled{}, fmt.Errorf("zalo: reading the direct chats' past: %w", err)
	}

	after, err := a.svc.mirror.chatMessages(ctx, a.uid, chat)

	return messaging.Backfilled{Added: after - before, Complete: true}, err
}

// sleep waits d, or answers false when ctx ends first.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
