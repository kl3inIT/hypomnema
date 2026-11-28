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

	a.fetchMu.Lock()
	defer a.fetchMu.Unlock()

	oldest, _, err := a.store.edges(ctx, a.key, chat)
	if err != nil {
		return messaging.Backfilled{}, err
	}

	added, complete, err := a.fetch(ctx, client, peer, 0, oldest, count)

	return messaging.Backfilled{Added: added, Complete: complete}, err
}

// fetch keeps up to count messages of a peer older than offsetID (zero: the latest) and newer than minID, the newest
// first, page by page; it says how many were new and whether Telegram had no more. The caller holds fetchMu: one
// fetch at a time, as mautrix's fetchMessagesSema (client.go:209).
func (a *Account) fetch(ctx context.Context, client api, peer tg.InputPeerClass, minID, offsetID, count int,
) (int, bool, error) {
	added := 0

	for page := 1; count > 0; page++ {
		if page > 1 {
			if err := a.pause(ctx, page-1); err != nil {
				return added, false, err
			}
		}

		limit := min(count, pageSize)

		res, err := client.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: peer, OffsetID: offsetID,
			MinID: minID, Limit: limit})
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
		isNew, err := a.keepFetched(ctx, e, mc)
		if err != nil {
			return nil, added, err
		}

		if isNew {
			added++
		}
	}

	return page.GetMessages(), added, nil
}

// keepFetched keeps a message fetched from Telegram: one people wrote, or what a service message says of its chat.
func (a *Account) keepFetched(ctx context.Context, e tg.Entities, mc tg.MessageClass) (bool, error) {
	if svc, ok := mc.(*tg.MessageService); ok {
		return false, a.keepService(ctx, e, svc)
	}

	k, ok := convert(a.userID, mc)
	if !ok {
		return false, nil
	}

	return a.keep(ctx, e, k)
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
// (updates.channelDifferenceTooLong): the latest ones Telegram gave with it, then those between newest, the last
// message known before the gap, and them, as gotd says they are to be fetched (telegram/updates/config.go:55-65,
// OnChannelGap). newest is read when gotd reports the gap (updatesConfig), before the channel's next updates are kept;
// a channel nothing was kept of (newest zero) starts from the latest.
func (a *Account) recoverGap(ctx context.Context, client api, newest int, gap updates.ChannelGap) error {
	a.fetchMu.Lock()
	defer a.fetchMu.Unlock()

	if err := a.states.SetChannelAccessHash(ctx, a.userID, gap.ChannelID, gap.AccessHash); err != nil {
		return err
	}

	e, err := a.keepRaw(ctx, gap.Users, gap.Chats)
	if err != nil {
		return err
	}

	for _, mc := range gap.Messages {
		if _, err := a.keepFetched(ctx, e, mc); err != nil {
			return err
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

// syncDialogs goes on with the listing of every dialog of the account, page by page from where it stopped, keeping
// each chat as it is now with its latest message and tracking each channel with the updates manager. A listing that
// catches up also fetches what was missed of each chat since the newest message kept (after a gap of the account's own
// updates the manager could not recover, updates.differenceTooLong, or a state it did not find); a chat nothing was
// kept of starts from its latest message. A listing done is not run again until restartDialogs.
//
// Paging follows gotd's dialogs iterator (telegram/query/dialogs/iter.go:135-147,183-203): a short slice is not the
// end, only an empty page, a whole list (messages.dialogs) or as many dialogs as the count says; the next page starts
// at the last dialog's peer, and at the id and date of the latest message of the last dialog that has one, a service
// message too; a page that does not move the offset ends it. The cursor is kept after each page, as mautrix's
// (chatsync.go:157-168), so that a failure resumes from it on the next connection.
func (a *Account) syncDialogs(ctx context.Context, c conn) error {
	a.syncMu.Lock()
	defer a.syncMu.Unlock()

	cur, err := a.store.dialogCursor(ctx, a.key)
	if err != nil || cur.done {
		return err
	}

	for {
		offset, err := cur.offsetPeer()
		if err != nil {
			return err
		}

		req := &tg.MessagesGetDialogsRequest{OffsetPeer: offset, OffsetID: cur.offsetID, OffsetDate: cur.offsetDate,
			Limit: pageSize}

		res, err := c.api.MessagesGetDialogs(ctx, req)
		if err != nil {
			return fmt.Errorf("telegram: listing the dialogs: %w", err)
		}

		page, ok := res.AsModified()
		if !ok {
			return nil
		}

		next, err := a.keepDialogs(ctx, c, page, cur.catchUp)
		if err != nil {
			return err
		}

		dialogs := page.GetDialogs()
		cur.received += len(dialogs)

		end := len(dialogs) == 0
		if slice, ok := res.(*tg.MessagesDialogsSlice); !ok || (slice.Count > 0 && cur.received >= slice.Count) {
			end = true
		}

		moved := next.peer != cur.peer || next.offsetID != cur.offsetID || next.offsetDate != cur.offsetDate
		if !end && moved {
			cur.peer, cur.hash, cur.offsetID, cur.offsetDate = next.peer, next.hash, next.offsetID, next.offsetDate
		} else {
			cur.done = true
		}

		if kept, err := a.store.advanceDialogs(ctx, a.key, cur); err != nil || !kept || cur.done {
			return err
		}
	}
}

// offsetPeer is how the next page's request names the peer the last page ended at; the first page starts at none.
func (c dialogCursor) offsetPeer() (tg.InputPeerClass, error) {
	if c.peer == "" {
		return &tg.InputPeerEmpty{}, nil
	}

	kind, id, err := parseID(c.peer)
	if err != nil {
		return nil, err
	}

	switch kind {
	case peerUser:
		return &tg.InputPeerUser{UserID: id, AccessHash: c.hash}, nil
	case peerChat:
		return &tg.InputPeerChat{ChatID: id}, nil
	default:
		return &tg.InputPeerChannel{ChannelID: id, AccessHash: c.hash}, nil
	}
}

// keepDialogs keeps a page of dialogs, and gives where the next page starts.
func (a *Account) keepDialogs(ctx context.Context, c conn, page tg.ModifiedMessagesDialogs, catchUp bool,
) (dialogCursor, error) {
	e, err := a.keepRaw(ctx, page.GetUsers(), page.GetChats())
	if err != nil {
		return dialogCursor{}, err
	}

	raw := map[string]chatInfo{}

	for _, ch := range page.GetChats() {
		if info, ok := groupChat(ch); ok {
			raw[info.id] = info
		}
	}

	tops := map[string]tg.MessageClass{}

	for _, mc := range page.GetMessages() {
		if m, ok := mc.AsNotEmpty(); ok && m.GetPeerID() != nil {
			tops[fmt.Sprintf("%s/%d", peerID(m.GetPeerID()), m.GetID())] = mc
		}
	}

	var next dialogCursor

	for _, dc := range page.GetDialogs() {
		p := dialogPeer(dc)

		chat := peerID(p)
		if chat == "" {
			continue
		}

		next.peer, next.hash = chat, offsetHash(e, p)

		d, ok := dc.(*tg.Dialog)
		if !ok {
			continue
		}

		top := tops[fmt.Sprintf("%s/%d", chat, d.TopMessage)]
		if m, ok := top.(interface{ GetDate() int }); ok {
			next.offsetID, next.offsetDate = top.GetID(), m.GetDate()
		}

		if err := a.keepDialog(ctx, c, e, raw, d, top, catchUp); err != nil {
			return dialogCursor{}, err
		}
	}

	return next, nil
}

// keepDialog keeps one dialog's chat, its latest message and what was missed of it, and tracks a channel.
func (a *Account) keepDialog(ctx context.Context, c conn, e tg.Entities, raw map[string]chatInfo, d *tg.Dialog,
	top tg.MessageClass, catchUp bool,
) error {
	chat := peerID(d.Peer)

	info, ok := entityChat(e, chat)
	if !ok {
		info, ok = raw[chat]
	}

	if !ok {
		info = fallbackChat(chat)
	}

	if err := a.store.keepChat(ctx, a.key, info); err != nil {
		return err
	}

	if err := a.trackChannel(ctx, c, e, d, info); err != nil {
		return err
	}

	a.fetchMu.Lock()
	defer a.fetchMu.Unlock()

	_, newest, err := a.store.edges(ctx, a.key, chat)
	if err != nil {
		return err
	}

	if top != nil {
		if _, err := a.keepFetched(ctx, e, top); err != nil {
			return err
		}
	}

	if !catchUp || newest == 0 || d.TopMessage <= newest+1 || !info.listed {
		return nil
	}

	peer, err := a.inputPeer(ctx, chat)
	if err != nil {
		return err
	}

	_, _, err = a.fetch(ctx, c.api, peer, newest, d.TopMessage, maxCatchUp)

	return err
}

// trackChannel has the updates manager track a listed channel from its dialog's pts with its access hash, as gotd
// says channels joined while offline are to be tracked (telegram/updates/track_channel.go:20-35): it learns of the
// others only from their next update, and would miss what came before. A channel already tracked goes on from its
// own pts.
func (a *Account) trackChannel(ctx context.Context, c conn, e tg.Entities, d *tg.Dialog, info chatInfo) error {
	p, ok := d.Peer.(*tg.PeerChannel)
	if !ok || !info.listed {
		return nil
	}

	pts, ok := d.GetPts()
	if !ok || pts <= 0 {
		return nil
	}

	ch, ok := e.Channels[p.ChannelID]
	if !ok || ch.Min {
		return nil
	}

	hash, ok := ch.GetAccessHash()
	if !ok {
		return nil
	}

	if err := c.tracker.TrackChannel(ctx, p.ChannelID, hash, pts); err != nil {
		return fmt.Errorf("telegram: tracking channel %d: %w", p.ChannelID, err)
	}

	return nil
}

// dialogPeer is a dialog's peer, a folder's too, as gotd's dialogs iterator reads it.
func dialogPeer(dc tg.DialogClass) tg.PeerClass {
	switch d := dc.(type) {
	case *tg.Dialog:
		return d.Peer
	case *tg.DialogFolder:
		return d.Peer
	}

	return nil
}

// offsetHash is the access hash a page's offset peer is named with: the one its entities give, a min one too, else
// zero, as gotd names an offset peer (telegram/message/peer/extract.go:310-333): the server uses it to order only.
func offsetHash(e tg.Entities, p tg.PeerClass) int64 {
	switch p := p.(type) {
	case *tg.PeerUser:
		if u, ok := e.Users[p.UserID]; ok {
			return u.AccessHash
		}
	case *tg.PeerChannel:
		if c, ok := e.Channels[p.ChannelID]; ok {
			return c.AccessHash
		}
	}

	return 0
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
