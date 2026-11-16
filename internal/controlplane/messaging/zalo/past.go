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

// catchUp runs after the listener started and the groups were named: the files left, then the past. Each page
// is read alone (past), so that a request for more of one chat waits for one page, not for the whole walk.
func (a *account) catchUp(ctx context.Context, b *bridge) {
	a.backfillMedia(ctx)

	if err := a.walkQueue(ctx, b); err != nil {
		a.svc.log.WarnContext(ctx, "zalo: the direct chats' past could not be read", slog.String("error", err.Error()))
	}

	// The direct chats the queue brought are named too.
	a.nameDirectChats(ctx, b)

	if err := a.walkGroups(ctx, b); err != nil {
		a.svc.log.WarnContext(ctx, "zalo: the groups' past could not be read", slog.String("error", err.Error()))
	}
}

// lockPast waits for the turn to read a page of the past, or for ctx to end.
func (a *account) lockPast(ctx context.Context) error {
	select {
	case a.past <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *account) unlockPast() { <-a.past }

// page is one page of the past, as the bridge answers it.
type page struct {
	Messages  []incoming `json:"messages"`
	LastMsgID string     `json:"lastMsgId"`
	IsOld     bool       `json:"isOld"`
	More      bool       `json:"more"`
	Next      string     `json:"next"`
}

// keepPage keeps a page's messages and answers how many were new. A message that could not be stored fails the
// page, whose cursor then stays where it was: the page is read again, and what it kept is kept once.
func (a *account) keepPage(ctx context.Context, p page) (int, error) {
	added := 0

	for _, m := range p.Messages {
		ok, err := a.keepMessage(ctx, m)
		if err != nil {
			return added, err
		}

		if ok {
			added++
		}
	}

	return added, nil
}

// walkGroups walks every group not walked to its start yet, a page at a time.
func (a *account) walkGroups(ctx context.Context, b *bridge) error {
	groups, err := a.svc.mirror.groupsToWalk(ctx, a.uid)
	if err != nil {
		return err
	}

	for _, g := range groups {
		for {
			w, _, err := a.nextGroupPage(ctx, b, g.chat)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}

				a.svc.log.WarnContext(ctx, "zalo: a group's past could not be read", slog.String("group", g.chat),
					slog.String("error", err.Error()))

				break
			}

			if w.done {
				break
			}

			if !sleep(ctx, a.svc.gap) {
				return ctx.Err()
			}
		}
	}

	return nil
}

// nextGroupPage reads the next page of a group's past, from where its walk is now, alone.
func (a *account) nextGroupPage(ctx context.Context, b *bridge, chat string) (walk, int, error) {
	if err := a.lockPast(ctx); err != nil {
		return walk{}, 0, err
	}
	defer a.unlockPast()

	w, err := a.svc.mirror.walkOf(ctx, a.uid, chat)
	if err != nil || w.done {
		return w, 0, err
	}

	var p page
	if err := b.call(ctx, "groupHistory", map[string]any{"group": w.chat, "lastMsgId": w.cursor, "isOld": w.old},
		&p); err != nil {
		return w, 0, err
	}

	added, err := a.keepPage(ctx, p)
	if err != nil {
		return w, added, err
	}

	w.cursor, w.old, w.done = p.LastMsgID, p.IsOld, !p.More || p.LastMsgID == ""

	return w, added, a.svc.mirror.walked(ctx, a.uid, w)
}

// walkQueue walks the direct chats' queue from where it stopped, until a page brings nothing new. A walk that
// reaches maxQueuePages stops there unfinished, and goes on at the next one.
func (a *account) walkQueue(ctx context.Context, b *bridge) error {
	for range maxQueuePages {
		done, err := a.nextQueuePage(ctx, b)
		if err != nil || done {
			return err
		}

		if !sleep(ctx, a.svc.gap) {
			return ctx.Err()
		}
	}

	return nil
}

// nextQueuePage reads the next page of the direct chats' queue, alone, and says whether the queue's end is reached.
func (a *account) nextQueuePage(ctx context.Context, b *bridge) (bool, error) {
	if err := a.lockPast(ctx); err != nil {
		return false, err
	}
	defer a.unlockPast()

	cursor, done, err := a.svc.mirror.queue(ctx, a.uid)
	if err != nil || done {
		return done, err
	}

	var p page
	if err := b.call(ctx, "oldMessages", map[string]any{"lastId": cursor}, &p); err != nil {
		return false, err
	}

	added, err := a.keepPage(ctx, p)
	if err != nil {
		return false, err
	}

	end := added == 0 || p.Next == "" || p.Next == cursor
	if !end {
		cursor = p.Next
	}

	return end, a.svc.mirror.queued(ctx, a.uid, cursor, end)
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

// Backfill asks Zalo for a chat's older messages: up to count of a group, a page at a time and as far apart as
// the walk's; for a direct chat, the rest of the direct chats' queue, of which it answers what this chat gained.
func (a *account) Backfill(ctx context.Context, chat string, count int) (messaging.Backfilled, error) {
	b := a.current()
	if b == nil {
		return messaging.Backfilled{}, errNotConnected
	}

	if a.svc.mirror.chatKind(ctx, a.uid, chat) != "group" {
		return a.backfillDirect(ctx, b, chat)
	}

	out := messaging.Backfilled{}

	for first := true; ; first = false {
		if !first && !sleep(ctx, a.svc.gap) {
			return out, ctx.Err()
		}

		w, added, err := a.nextGroupPage(ctx, b, chat)
		if err != nil {
			return out, fmt.Errorf("zalo: reading the past of %s: %w", chat, err)
		}

		out.Added += added
		out.Complete = w.done

		if w.done || out.Added >= max(count, 1) {
			return out, nil
		}
	}
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
	if err != nil {
		return messaging.Backfilled{}, err
	}

	_, done, err := a.svc.mirror.queue(ctx, a.uid)

	return messaging.Backfilled{Added: after - before, Complete: done}, err
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
