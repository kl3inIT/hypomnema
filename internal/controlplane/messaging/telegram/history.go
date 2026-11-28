package telegram

// The chats' past and what the updates manager could not recover (issue #1184): Telegram's servers answer
// messages.getHistory, so older messages are fetched by pages as mautrix/telegram fetches them without takeout
// (pkg/connector/backfill.go:173-256: pages of 100, one fetch at a time, a pause that grows between pages), and the
// dialogs are listed by pages as its chat sync lists them (chatsync.go:43-140, 100 a page). No takeout session: mautrix
// keeps it off by default (example-config.yaml, takeout). Nothing here marks a chat read: messages.readHistory is
// never called (Telegram API terms 1.4).

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gotd/td/telegram/updates"
	"github.com/gotd/td/tg"

	"github.com/kl3inIT/hypomnema/internal/controlplane/messaging"
)

const (
	// pageSize is how many messages or dialogs one request asks for (mautrix chunkLimit, backfill.go:173).
	pageSize = 100
	// defaultBackfill and maxBackfill are how many older messages one Backfill asks for, as whatsapp's.
	defaultBackfill = 50
	maxBackfill     = 500
	// maxCatchUp is how many missed messages of a chat are fetched after a gap the updates manager could not recover,
	// as mautrix's bridges catch up after a restart (mautrix/go bridgev2 example-config.yaml, max_catchup_messages).
	maxCatchUp = 500
)

// api is what the account asks of Telegram itself, outside the updates manager; gotd's *tg.Client is it.
type api interface {
	MessagesGetHistory(ctx context.Context, req *tg.MessagesGetHistoryRequest) (tg.MessagesMessagesClass, error)
	MessagesGetDialogs(ctx context.Context, req *tg.MessagesGetDialogsRequest) (tg.MessagesDialogsClass, error)
	AuthLogOut(ctx context.Context) (*tg.AuthLoggedOut, error)
}

// waitPage waits before the page'th request of a fetch that is not the first: two seconds a page, at most fifteen, as
// mautrix waits between batches when it does not use takeout (backfill.go:244-256).
func waitPage(ctx context.Context, page int) error {
	t := time.NewTimer(time.Duration(min(page*2, 15)) * time.Second)
	defer t.Stop()

	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Backfill fetches up to count messages of a chat older than the oldest kept from Telegram, keeps them, and answers
// how many were added and whether the chat's start was reached.
func (a *Account) Backfill(ctx context.Context, chat string, count int) (messaging.Backfilled, error) {
	client, err := a.liveAPI()
	if err != nil {
		return messaging.Backfilled{}, err
	}

	return a.backfill(ctx, client, chat, count)
}

func (a *Account) backfill(ctx context.Context, client api, chat string, count int) (messaging.Backfilled, error) {
	switch {
	case count <= 0:
		count = defaultBackfill
	case count > maxBackfill:
		count = maxBackfill
	}

	peer, err := a.inputPeer(ctx, chat)
	if err != nil {
		return messaging.Backfilled{}, err
	}

	oldest, _, err := a.store.edges(ctx, a.key, chat)
	if err != nil {
		return messaging.Backfilled{}, err
	}

	added, complete, err := a.fetch(ctx, client, peer, 0, oldest, count)

	return messaging.Backfilled{Added: added, Complete: complete}, err
}

// fetch keeps up to count messages of a peer older than offsetID (zero: the latest) and newer than minID, the newest
// first, page by page; it says how many were new and whether Telegram had no more.
func (a *Account) fetch(ctx context.Context, client api, peer tg.InputPeerClass, minID, offsetID, count int,
) (int, bool, error) {
	a.fetchMu.Lock()
	defer a.fetchMu.Unlock()

	added := 0

	for page := 1; count > 0; page++ {
		if page > 1 {
			if err := a.pause(ctx, page-1); err != nil {
				return added, false, err
			}
		}

		limit := min(count, pageSize)

		res, err := client.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: peer, OffsetID: offsetID, MinID: minID,
			Limit: limit})
		if err != nil {
			return added, false, fmt.Errorf("telegram: fetching the history of a chat: %w", err)
		}

		messages, n, err := a.keepPage(ctx, res)
		added += n

		if err != nil {
			return added, false, err
		}

		if len(messages) < limit {
			return added, true, nil
		}

		next := lowestID(messages)
		if next == 0 || (offsetID != 0 && next >= offsetID) {
			return added, false, nil
		}

		offsetID, count = next, count-len(messages)
	}

	return added, false, nil
}

// keepPage keeps a page of messages with its entities, and gives its messages and how many were new.
func (a *Account) keepPage(ctx context.Context, res tg.MessagesMessagesClass) ([]tg.MessageClass, int, error) {
	page, ok := res.AsModified()
	if !ok {
		return nil, 0, nil
	}

	e, err := a.keepRaw(ctx, page.GetUsers(), page.GetChats())
	if err != nil {
		return nil, 0, err
	}

	added := 0

	for _, mc := range page.GetMessages() {
		k, ok := convert(a.userID, mc)
		if !ok {
			continue
		}

		isNew, err := a.keep(ctx, e, k)
		if err != nil {
			return nil, added, err
		}

		if isNew {
			added++
		}
	}

	return page.GetMessages(), added, nil
}

func lowestID(messages []tg.MessageClass) int {
	lowest := 0

	for _, m := range messages {
		if id := m.GetID(); lowest == 0 || id < lowest {
			lowest = id
		}
	}

	return lowest
}

// recoverGap keeps a channel's messages across a gap the updates manager could not recover
// (updates.channelDifferenceTooLong): the latest ones Telegram gave with it, then those between the newest kept
// before and them, as gotd says they are to be fetched (telegram/updates/config.go:55-65, OnChannelGap). A channel
// nothing was kept of starts from the latest.
func (a *Account) recoverGap(ctx context.Context, client api, gap updates.ChannelGap) error {
	chat := channelID(gap.ChannelID)

	if err := a.states.SetChannelAccessHash(ctx, a.userID, gap.ChannelID, gap.AccessHash); err != nil {
		return err
	}

	_, newest, err := a.store.edges(ctx, a.key, chat)
	if err != nil {
		return err
	}

	e, err := a.keepRaw(ctx, gap.Users, gap.Chats)
	if err != nil {
		return err
	}

	for _, mc := range gap.Messages {
		if k, ok := convert(a.userID, mc); ok {
			if _, err := a.keep(ctx, e, k); err != nil {
				return err
			}
		}
	}

	if newest == 0 {
		return nil
	}

	until := lowestID(gap.Messages)
	if until == 0 && gap.TopMessage > 0 {
		until = gap.TopMessage + 1
	}

	if until != 0 && until <= newest+1 {
		return nil
	}

	_, _, err = a.fetch(ctx, client, &tg.InputPeerChannel{ChannelID: gap.ChannelID, AccessHash: gap.AccessHash}, newest,
		until, maxCatchUp)

	return err
}

// syncDialogs lists every dialog of the account by pages and keeps each chat as it is now, with its latest message.
// With catchUp it also fetches what was missed of each chat since the newest message kept, as after a gap of the
// account's own updates the manager could not recover (updates.differenceTooLong) or a state lost; a chat nothing was
// kept of starts from its latest message.
func (a *Account) syncDialogs(ctx context.Context, client api, catchUp bool) error {
	req := &tg.MessagesGetDialogsRequest{OffsetPeer: &tg.InputPeerEmpty{}, Limit: pageSize}

	for {
		res, err := client.MessagesGetDialogs(ctx, req)
		if err != nil {
			return fmt.Errorf("telegram: listing the dialogs: %w", err)
		}

		page, ok := res.AsModified()
		if !ok {
			return nil
		}

		last, err := a.keepDialogs(ctx, client, page, catchUp)
		if err != nil {
			return err
		}

		_, slice := res.(*tg.MessagesDialogsSlice)
		if !slice || len(page.GetDialogs()) < req.Limit || last == nil {
			return nil
		}

		next, err := a.inputPeer(ctx, peerID(last.dialog.Peer))
		if err != nil {
			return err
		}

		if req.OffsetID == last.dialog.TopMessage && req.OffsetDate == last.date {
			return nil
		}

		req.OffsetPeer, req.OffsetID, req.OffsetDate = next, last.dialog.TopMessage, last.date
	}
}

// lastDialog is a page's last dialog and the date of its latest message, where the next page starts.
type lastDialog struct {
	dialog *tg.Dialog
	date   int
}

func (a *Account) keepDialogs(ctx context.Context, client api, page tg.ModifiedMessagesDialogs, catchUp bool,
) (*lastDialog, error) {
	e, err := a.keepRaw(ctx, page.GetUsers(), page.GetChats())
	if err != nil {
		return nil, err
	}

	raw := map[string]chatInfo{}

	for _, c := range page.GetChats() {
		if info, ok := groupChat(c); ok {
			raw[info.id] = info
		}
	}

	tops := map[string]*tg.Message{}

	for _, mc := range page.GetMessages() {
		if m, ok := mc.(*tg.Message); ok && m.PeerID != nil {
			tops[fmt.Sprintf("%s/%d", peerID(m.PeerID), m.ID)] = m
		}
	}

	var last *lastDialog

	for _, dc := range page.GetDialogs() {
		d, ok := dc.(*tg.Dialog)
		if !ok {
			continue
		}

		chat := peerID(d.Peer)
		if chat == "" {
			continue
		}

		info, ok := entityChat(e, chat)
		if !ok {
			info, ok = raw[chat]
		}

		if !ok {
			info = fallbackChat(chat)
		}

		if err := a.store.keepChat(ctx, a.key, info); err != nil {
			return nil, err
		}

		_, newest, err := a.store.edges(ctx, a.key, chat)
		if err != nil {
			return nil, err
		}

		last = &lastDialog{dialog: d}

		if top, ok := tops[fmt.Sprintf("%s/%d", chat, d.TopMessage)]; ok {
			last.date = top.Date

			if k, ok := convert(a.userID, top); ok {
				if _, err := a.keep(ctx, e, k); err != nil {
					return nil, err
				}
			}
		}

		if catchUp && newest > 0 && d.TopMessage > newest+1 && info.listed {
			peer, err := a.inputPeer(ctx, chat)
			if err != nil {
				return nil, err
			}

			if _, _, err := a.fetch(ctx, client, peer, newest, d.TopMessage, maxCatchUp); err != nil {
				return nil, err
			}
		}
	}

	return last, nil
}

// errNoAccessHash is a chat whose access hash was never seen, which no request can name.
var errNoAccessHash = errors.New("telegram: the chat's access hash is not known yet")

// inputPeer is how a request names a chat: a person or a channel with the access hash kept for it.
func (a *Account) inputPeer(ctx context.Context, chat string) (tg.InputPeerClass, error) {
	kind, id, err := parseID(chat)
	if err != nil {
		return nil, err
	}

	switch kind {
	case peerChat:
		return &tg.InputPeerChat{ChatID: id}, nil
	case peerUser:
		if id == a.userID {
			return &tg.InputPeerSelf{}, nil
		}

		hash, ok, err := a.states.GetUserAccessHash(ctx, a.userID, id)
		if err != nil {
			return nil, err
		}

		if !ok {
			return nil, fmt.Errorf("%w: %s", errNoAccessHash, chat)
		}

		return &tg.InputPeerUser{UserID: id, AccessHash: hash}, nil
	default:
		hash, ok, err := a.states.GetChannelAccessHash(ctx, a.userID, id)
		if err != nil {
			return nil, err
		}

		if !ok {
			return nil, fmt.Errorf("%w: %s", errNoAccessHash, chat)
		}

		return &tg.InputPeerChannel{ChannelID: id, AccessHash: hash}, nil
	}
}
