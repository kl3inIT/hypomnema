package telegram

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/updates"
	"github.com/gotd/td/tg"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/messaging"
)

// dialogsPage is a slice of dialogs, each with its latest message, of an account with count dialogs in all.
func dialogsPage(count int, top map[tg.PeerClass]tg.MessageClass, order ...tg.PeerClass) *tg.MessagesDialogsSlice {
	res := &tg.MessagesDialogsSlice{Count: count,
		Users: []tg.UserClass{user(7, "Trung")},
		Chats: []tg.ChatClass{&tg.Chat{ID: 8, Title: "Team"}, channel(9, "VMAP AI")},
	}

	for _, p := range order {
		m := top[p]
		res.Dialogs = append(res.Dialogs, &tg.Dialog{Peer: p, TopMessage: m.GetID()})
		res.Messages = append(res.Messages, m)
	}

	return res
}

// Review of #1324, P1 1: the first listing is resumable. A failure keeps the page cursor, as mautrix keeps
// DialogSyncCursor and DialogSyncComplete (chatsync.go:157-187); the next connection goes on from it instead of
// starting over, and a finished listing is not run again.
func TestTheFirstListingResumesAfterAFailure(t *testing.T) {
	t.Parallel()

	a := testAccount(t)
	ctx := t.Context()
	trung, team := &tg.PeerUser{UserID: 7}, &tg.PeerChat{ChatID: 8}
	server := newFakeTelegram()
	server.dialogs = []tg.MessagesDialogsClass{
		dialogsPage(2, map[tg.PeerClass]tg.MessageClass{trung: message(trung, 20, "latest")}, trung),
		dialogsPage(2, map[tg.PeerClass]tg.MessageClass{team: message(team, 3, "latest")}, team),
	}
	server.dialogsFail = map[int]error{2: errors.New("connection reset")}

	require.Error(t, a.syncDialogs(ctx, server.conn()))

	require.NoError(t, a.syncDialogs(ctx, server.conn()), "the next connection resumes")
	require.Len(t, server.dialogsAsked, 3)
	assert.Equal(t, server.dialogsAsked[1], server.dialogsAsked[2], "it asks for the page that failed again")
	assert.Equal(t, 20, server.dialogsAsked[2].OffsetID)

	chats, err := a.Chats(ctx, 0)
	require.NoError(t, err)
	assert.Len(t, chats, 2)

	require.NoError(t, a.syncDialogs(ctx, server.conn()))
	assert.Len(t, server.dialogsAsked, 3, "a finished listing is not run again")
}

// The connection's start resumes an unfinished listing (mautrix client.go:603-612), and a state the updates manager
// did not find restarts it with catching up, so that what was missed meanwhile is fetched.
func TestTheConnectionsStartResumesTheListing(t *testing.T) {
	t.Parallel()

	a := testAccount(t)
	ctx := t.Context()
	trung := &tg.PeerUser{UserID: 7}
	server := newFakeTelegram()
	server.add(trung, ids(1, 20)...)

	_, err := a.keep(ctx, tg.Entities{}, mustConvert(t, message(trung, 5, "x")))
	require.NoError(t, err)
	require.NoError(t, a.keepEntities(ctx, tg.EntitiesFrom([]tg.UserClass{user(7, "Trung")}, nil)))

	server.dialogs = []tg.MessagesDialogsClass{&tg.MessagesDialogs{
		Dialogs:  []tg.DialogClass{&tg.Dialog{Peer: trung, TopMessage: 20}},
		Messages: []tg.MessageClass{message(trung, 20, "latest")},
		Users:    []tg.UserClass{user(7, "Trung")},
	}}

	jobs := &background{}
	jobs.start(ctx, server.conn())

	cfg := a.updatesConfig(jobs, nil)
	cfg.OnLoadUserStateFailed()
	a.onStart(jobs)
	jobs.wg.Wait()
	jobs.stop()

	assert.Equal(t, messaging.StatusConnected, a.State().Status)
	assert.Equal(t, strs(ids(5, 20)), keptIDs(t, a, "user:7"))
	assert.Len(t, server.dialogsAsked, 1, "one listing however many times it was asked for")
}

// Review of #1324, P1 3: a channel listed is tracked from its dialog's pts with its access hash, as gotd says
// channels joined offline are to be tracked (telegram/updates/track_channel.go:20-35), so that what it gets while
// the control plane is stopped is fetched by getChannelDifference; one the account left is not.
func TestTheListingTracksEveryChannel(t *testing.T) {
	t.Parallel()

	a := testAccount(t)
	group, left := &tg.PeerChannel{ChannelID: 9}, &tg.PeerChannel{ChannelID: 10}
	gone := channel(10, "Old")
	gone.Left = true

	page := &tg.MessagesDialogs{
		Dialogs:  []tg.DialogClass{withPts(group, 50, 20), withPts(left, 7, 30)},
		Messages: []tg.MessageClass{message(group, 50, "latest"), message(left, 7, "latest")},
		Chats:    []tg.ChatClass{channel(9, "VMAP AI"), gone},
	}
	server := newFakeTelegram()
	server.dialogs = []tg.MessagesDialogsClass{page}

	require.NoError(t, a.syncDialogs(t.Context(), server.conn()))
	assert.Equal(t, [][3]int64{{9, 9000, 20}}, server.tracked)
}

func withPts(p tg.PeerClass, top, pts int) *tg.Dialog {
	d := &tg.Dialog{Peer: p, TopMessage: top}
	d.SetPts(pts)

	return d
}

// Review of #1324, P1 4: a short slice is not the end while its count says more dialogs follow, and the next page
// starts at the date of the last dialog's latest message, a service message's too (gotd
// telegram/query/dialogs/iter.go:135-147,183-187).
func TestTheListingPagesPastShortSlices(t *testing.T) {
	t.Parallel()

	a := testAccount(t)
	ctx := t.Context()
	trung, team := &tg.PeerUser{UserID: 7}, &tg.PeerChat{ChatID: 8}
	joined := &tg.MessageService{ID: 4, PeerID: team, Date: 1_700_000_500, Action: &tg.MessageActionChatAddUser{}}

	server := newFakeTelegram()
	server.dialogs = []tg.MessagesDialogsClass{
		dialogsPage(2, map[tg.PeerClass]tg.MessageClass{team: joined}, team),
		dialogsPage(2, map[tg.PeerClass]tg.MessageClass{trung: message(trung, 20, "latest")}, trung),
	}

	require.NoError(t, a.syncDialogs(ctx, server.conn()))
	require.Len(t, server.dialogsAsked, 2)
	assert.Equal(t, 4, server.dialogsAsked[1].OffsetID)
	assert.Equal(t, 1_700_000_500, server.dialogsAsked[1].OffsetDate)
	assert.Equal(t, &tg.InputPeerChat{ChatID: 8}, server.dialogsAsked[1].OffsetPeer)

	chats, err := a.Chats(ctx, 0)
	require.NoError(t, err)
	assert.Len(t, chats, 2)
}

// Review of #1324, P1 2: a channel's gap is filled from the last message known before it, read when gotd reports
// it (telegram/updates/config.go:55-65), not when the fetch runs: a newer live update kept meanwhile does not hide
// the gap.
func TestAGapIsFilledFromTheMessageKnownBeforeIt(t *testing.T) {
	t.Parallel()

	a := testAccount(t)
	ctx := t.Context()
	peer := &tg.PeerChannel{ChannelID: 9}
	server := newFakeTelegram()
	server.add(peer, ids(1, 14)...)

	_, err := a.keep(ctx, tg.Entities{}, mustConvert(t, message(peer, 10, "x")))
	require.NoError(t, err)

	jobs := &background{}
	jobs.start(ctx, server.conn())

	// The fetch waits behind another one while the live update lands.
	a.fetchMu.Lock()
	a.updatesConfig(jobs, nil).OnChannelGap(updates.ChannelGap{ChannelID: 9, AccessHash: 9000, TopMessage: 13,
		Messages: []tg.MessageClass{message(peer, 13, "x")}})
	require.NoError(t, a.handler().Handle(ctx, &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdateNewChannelMessage{Message: message(peer, 14, "x")}},
		Chats:   []tg.ChatClass{channel(9, "VMAP AI")},
	}))
	a.fetchMu.Unlock()
	jobs.wg.Wait()
	jobs.stop()

	assert.Equal(t, strs(ids(10, 14)), keptIDs(t, a, "channel:9"))
}

// Review of #1324, P1 6: a channel gotd finds inaccessible (CHANNEL_PRIVATE, telegram/updates/config.go:66-70) goes
// off the list, its messages kept.
func TestAnInaccessibleChannelLeavesTheList(t *testing.T) {
	t.Parallel()

	a := testAccount(t)
	ctx := t.Context()

	require.NoError(t, a.handler().Handle(ctx, &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdateNewChannelMessage{Message: message(&tg.PeerChannel{ChannelID: 9}, 1, "hi")}},
		Chats:   []tg.ChatClass{channel(9, "VMAP AI")},
	}))

	jobs := &background{}
	jobs.start(ctx, newFakeTelegram().conn())
	a.updatesConfig(jobs, nil).OnChannelInaccessible(9)
	jobs.wg.Wait()
	jobs.stop()

	chats, err := a.Chats(ctx, 0)
	require.NoError(t, err)
	assert.Empty(t, chats)
	assert.Equal(t, []string{"1"}, keptIDs(t, a, "channel:9"))
}

// Review of #1324, P1 6: Telegram's min copy of a channel does not carry the account's membership
// (core.telegram.org/api/min), so it never lists again a channel the account left.
func TestAMinChannelDoesNotUndoALeave(t *testing.T) {
	t.Parallel()

	a := testAccount(t)
	ctx := t.Context()
	left := channel(9, "VMAP AI")
	left.Left = true
	minimal := &tg.Channel{ID: 9, Title: "VMAP AI", Megagroup: true, Min: true}

	for _, c := range []tg.ChatClass{channel(9, "VMAP AI"), left, minimal} {
		require.NoError(t, a.handler().Handle(ctx, &tg.Updates{
			Updates: []tg.UpdateClass{&tg.UpdateNewChannelMessage{Message: message(&tg.PeerChannel{ChannelID: 9}, 1, "hi")}},
			Chats:   []tg.ChatClass{c},
		}))
	}

	chats, err := a.Chats(ctx, 0)
	require.NoError(t, err)
	assert.Empty(t, chats)
}

// Review of #1324, P1 5: a group the owner creates or is added to as it is made appears at once, from its
// MessageActionChatCreate (mautrix handletelegram.go:511-527, which creates the portal from it); one he is removed
// from goes off the list (handletelegram.go:495-497).
func TestAGroupAppearsWhenItIsCreated(t *testing.T) {
	t.Parallel()

	a := testAccount(t)
	ctx := t.Context()
	team := &tg.PeerChat{ChatID: 8}

	require.NoError(t, a.handler().Handle(ctx, &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdateNewMessage{Message: &tg.MessageService{ID: 1, PeerID: team,
			Date: 1_700_000_001, Action: &tg.MessageActionChatCreate{Title: "Team", Users: []int64{self, 7}}}}},
		Chats: []tg.ChatClass{&tg.Chat{ID: 8, Title: "Team"}},
	}))

	chats, err := a.Chats(ctx, 0)
	require.NoError(t, err)
	require.Len(t, chats, 1)
	assert.Equal(t, "chat:8", chats[0].ID)
	assert.Equal(t, "Team", chats[0].Name)
	assert.Equal(t, kindGroup, chats[0].Kind)

	require.NoError(t, a.handler().Handle(ctx, &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdateNewMessage{Message: &tg.MessageService{ID: 2, PeerID: team,
			Date: 1_700_000_002, Action: &tg.MessageActionChatDeleteUser{UserID: self}}}},
	}))

	chats, err = a.Chats(ctx, 0)
	require.NoError(t, err)
	assert.Empty(t, chats)
}

// Review round 2 of #1324, P1 1: a listing cut short by a dropped connection goes on when gotd connects again within
// the same session (telegram/connect.go:148-175 reconnects without a new client.Run), not only at the next session's
// start.
func TestAReconnectResumesAnUnfinishedListing(t *testing.T) {
	t.Parallel()

	a := testAccount(t)
	a.retry = func() backoff.BackOff { return backoff.NewConstantBackOff(time.Hour) }
	ctx := t.Context()
	server := newFakeTelegram()
	server.dialogs = []tg.MessagesDialogsClass{oneDialog()}
	server.dialogsFail = map[int]error{1: errors.New("connection reset")}

	jobs := &background{}
	jobs.start(ctx, server.conn())
	a.setClient(server)
	a.onStart(jobs)

	require.Eventually(t, func() bool { return dialogsAsked(server) == 1 }, 5*time.Second, time.Millisecond)

	a.connectionChanged(jobs, telegram.ConnectionStateDisconnected)
	a.connectionChanged(jobs, telegram.ConnectionStateReady)

	require.Eventually(t, func() bool { return listingDone(t, a) }, 5*time.Second, time.Millisecond)
	jobs.stop()

	assert.Equal(t, messaging.StatusConnected, a.State().Status)
	assert.Equal(t, 2, dialogsAsked(server))
	assert.Len(t, mustChats(t, a), 1)
}

// Review round 2 of #1324, P1 1: a request that fails for good (its retries spent, or a flood wait longer than the
// middleware waits, gotd/contrib middleware/floodwait/waiter.go:20-24) does not end the listing: it is tried again
// after gotd's reconnection backoff while the connection lasts.
func TestAFailedListingIsTriedAgain(t *testing.T) {
	t.Parallel()

	a := testAccount(t)
	a.retry = func() backoff.BackOff { return &backoff.ZeroBackOff{} }
	ctx := t.Context()
	server := newFakeTelegram()
	server.dialogs = []tg.MessagesDialogsClass{oneDialog()}
	server.dialogsFail = map[int]error{1: errors.New("FLOOD_WAIT_300")}

	jobs := &background{}
	jobs.start(ctx, server.conn())
	a.onStart(jobs)

	require.Eventually(t, func() bool { return listingDone(t, a) }, 5*time.Second, time.Millisecond)
	jobs.stop()

	assert.Equal(t, 2, dialogsAsked(server))
	assert.Len(t, mustChats(t, a), 1)
}

// Review round 2 of #1324, P1 2: a catch-up whose history request fails keeps the chat's anchor, the newest message
// kept before it, so that the listing resumed fills the same gap (mautrix backfills from its anchor,
// backfill.go:270-287): the dialog's latest message is kept only once what lies before it is.
func TestACatchUpThatFailsKeepsItsAnchor(t *testing.T) {
	t.Parallel()

	a := testAccount(t)
	ctx := t.Context()
	trung := &tg.PeerUser{UserID: 7}
	server := newFakeTelegram()
	server.add(trung, ids(1, 14)...)
	server.historyFail = map[int]error{1: errors.New("connection reset")}

	page := &tg.MessagesDialogs{
		Dialogs:  []tg.DialogClass{&tg.Dialog{Peer: trung, TopMessage: 14}},
		Messages: []tg.MessageClass{message(trung, 14, "latest")},
		Users:    []tg.UserClass{user(7, "Trung")},
	}
	server.dialogs = []tg.MessagesDialogsClass{page, page}

	require.NoError(t, a.keepEntities(ctx, tg.EntitiesFrom([]tg.UserClass{user(7, "Trung")}, nil)))
	_, err := a.keep(ctx, tg.Entities{}, mustConvert(t, message(trung, 10, "x")))
	require.NoError(t, err)

	require.NoError(t, a.store.restartDialogs(ctx, a.key, true))
	require.Error(t, a.syncDialogs(ctx, server.conn()))
	require.NoError(t, a.syncDialogs(ctx, server.conn()))

	assert.True(t, listingDone(t, a))
	assert.Equal(t, strs(ids(10, 14)), keptIDs(t, a, "user:7"))
}

// Review round 2 of #1324, P1 4: a page whose last dialogs have no message to start the next page at (an empty one)
// keeps the offset of the page before, as gotd's iterator keeps offsetID and offsetDate (query/dialogs/iter.go:179-
// 185): Telegram ignores offset_peer without them (mautrix chatsync.go:203-204) and would answer the first page again.
func TestAPageWithoutAnAnchorKeepsTheOffset(t *testing.T) {
	t.Parallel()

	a := testAccount(t)
	trung, team, group := &tg.PeerUser{UserID: 7}, &tg.PeerChat{ChatID: 8}, &tg.PeerChannel{ChannelID: 9}
	pages := map[string]*tg.MessagesDialogsSlice{
		"first":  dialogsPage(3, map[tg.PeerClass]tg.MessageClass{trung: message(trung, 20, "latest")}, trung),
		"second": dialogsPage(3, map[tg.PeerClass]tg.MessageClass{team: &tg.MessageEmpty{ID: 3}}, team),
		"third":  dialogsPage(3, map[tg.PeerClass]tg.MessageClass{group: message(group, 50, "latest")}, group),
	}

	server := newFakeTelegram()
	server.dialogsBy = func(req *tg.MessagesGetDialogsRequest) tg.MessagesDialogsClass {
		switch {
		case req.OffsetID == 0 && req.OffsetDate == 0:
			// Telegram ignores the offset peer alone.
			return pages["first"]
		case inputChat(req.OffsetPeer) == "user:7":
			return pages["second"]
		case inputChat(req.OffsetPeer) == "chat:8" && req.OffsetID == 20:
			return pages["third"]
		}

		return &tg.MessagesDialogsSlice{Count: 3}
	}

	require.NoError(t, a.syncDialogs(t.Context(), server.conn()))
	require.Len(t, server.dialogsAsked, 3)
	assert.Equal(t, 20, server.dialogsAsked[2].OffsetID)
	assert.Equal(t, 1_700_000_020, server.dialogsAsked[2].OffsetDate)
	assert.Len(t, mustChats(t, a), 3)
}

// Review round 2 of #1324, P1 5: old history does not undo the account's membership now. A basic group's history
// given after it was added back holds its former removal; a fetch keeps what people wrote and skips service
// messages, as mautrix's backfill skips every message that is not a tg.Message (backfill.go:289-293) and reads the
// actions from updates alone.
func TestOldHistoryDoesNotUnlistARejoinedGroup(t *testing.T) {
	t.Parallel()

	a := testAccount(t)
	ctx := t.Context()
	team := &tg.PeerChat{ChatID: 8}
	server := newFakeTelegram()
	server.chats = []tg.ChatClass{&tg.Chat{ID: 8, Title: "Team"}}
	server.add(team, 4)
	server.messages["chat:8"] = append(server.messages["chat:8"],
		&tg.MessageService{ID: 3, PeerID: team, Date: 1_700_000_003, Action: &tg.MessageActionChatDeleteUser{UserID: self}},
		&tg.MessageService{ID: 5, PeerID: team, Date: 1_700_000_005,
			Action: &tg.MessageActionChatAddUser{Users: []int64{self}}})

	require.NoError(t, a.handler().Handle(ctx, &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdateNewMessage{Message: message(team, 10, "welcome back")}},
		Chats:   []tg.ChatClass{&tg.Chat{ID: 8, Title: "Team"}},
	}))
	require.Len(t, mustChats(t, a), 1)

	_, err := a.backfill(ctx, server, "chat:8", 50)
	require.NoError(t, err)

	assert.Len(t, mustChats(t, a), 1, "the group is still listed")
	assert.Equal(t, []string{"4", "10"}, keptIDs(t, a, "chat:8"))
}

// Review round 2 of #1324, P1 6: a channel gotd finds lost after a gap (CHANNEL_PRIVATE on the next
// getChannelDifference, telegram/updates/state_channel.go:394-396,705-716, in the same goroutine as the gap's
// callback) stays off the list though the gap's fetch waited behind another: what the gap brought is kept when gotd
// reports it, in the order gotd reports it, and the fetch keeps only what Telegram answers it.
func TestALostChannelStaysOffTheListAfterAPendingGap(t *testing.T) {
	t.Parallel()

	a := testAccount(t)
	ctx := t.Context()
	peer := &tg.PeerChannel{ChannelID: 9}
	server := newFakeTelegram()
	server.add(peer, ids(1, 13)...)
	server.historyFail = map[int]error{1: errors.New("CHANNEL_PRIVATE")}

	require.NoError(t, a.handler().Handle(ctx, &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdateNewChannelMessage{Message: message(peer, 10, "x")}},
		Chats:   []tg.ChatClass{channel(9, "VMAP AI")},
	}))

	jobs := &background{}
	jobs.start(ctx, server.conn())
	cfg := a.updatesConfig(jobs, nil)

	a.fetchMu.Lock()
	cfg.OnChannelGap(updates.ChannelGap{ChannelID: 9, AccessHash: 9000, TopMessage: 13,
		Messages: []tg.MessageClass{message(peer, 13, "x")}, Chats: []tg.ChatClass{channel(9, "VMAP AI")}})
	cfg.OnChannelInaccessible(9)
	a.fetchMu.Unlock()
	jobs.wg.Wait()
	jobs.stop()

	assert.Empty(t, mustChats(t, a))
}

func oneDialog() *tg.MessagesDialogs {
	trung := &tg.PeerUser{UserID: 7}

	return &tg.MessagesDialogs{
		Dialogs:  []tg.DialogClass{&tg.Dialog{Peer: trung, TopMessage: 20}},
		Messages: []tg.MessageClass{message(trung, 20, "latest")},
		Users:    []tg.UserClass{user(7, "Trung")},
	}
}

func dialogsAsked(f *fakeTelegram) int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return len(f.dialogsAsked)
}

func listingDone(t *testing.T, a *Account) bool {
	t.Helper()

	cur, err := a.store.dialogCursor(t.Context(), a.key)
	require.NoError(t, err)

	return cur.done
}

func mustChats(t *testing.T, a *Account) []messaging.Chat {
	t.Helper()

	chats, err := a.Chats(t.Context(), 0)
	require.NoError(t, err)

	return chats
}

// Review round 3 of #1324, P1: a catch-up holds its pages until every one is fetched, and the dialog's latest message
// until then; the updates manager goes on meanwhile, and a deletion it hands over while the catch-up waits between
// pages reaches no kept row yet. It is applied once the pages and the latest message are kept, so that what was
// deleted is not kept again with its text (updates.go, OnDeleteMessages).
func TestADeletionDuringACatchUpIsKept(t *testing.T) {
	t.Parallel()

	a := testAccount(t)
	ctx := t.Context()
	trung := &tg.PeerUser{UserID: 7}
	server := newFakeTelegram()
	server.add(trung, ids(1, 249)...)
	server.messages["user:7"] = append(server.messages["user:7"], message(trung, 250, "latest"))
	server.dialogs = []tg.MessagesDialogsClass{&tg.MessagesDialogs{
		Dialogs:  []tg.DialogClass{&tg.Dialog{Peer: trung, TopMessage: 250}},
		Messages: []tg.MessageClass{message(trung, 250, "latest")},
		Users:    []tg.UserClass{user(7, "Trung")},
	}}

	require.NoError(t, a.keepEntities(ctx, tg.EntitiesFrom([]tg.UserClass{user(7, "Trung")}, nil)))
	_, err := a.keep(ctx, tg.Entities{}, mustConvert(t, message(trung, 10, "x")))
	require.NoError(t, err)

	// Telegram deletes three messages while the catch-up waits before its second page: one of the first page, one of
	// the second, and the latest.
	a.pause = func(ctx context.Context, page int) error {
		if page == 1 {
			return a.handler().Handle(ctx, &tg.Updates{Updates: []tg.UpdateClass{
				&tg.UpdateDeleteMessages{Messages: []int{200, 110, 250}},
			}})
		}

		return nil
	}

	require.NoError(t, a.store.restartDialogs(ctx, a.key, true))
	require.NoError(t, a.syncDialogs(ctx, server.conn()))
	require.Len(t, keptIDs(t, a, "user:7"), 241)

	for _, id := range []string{"110", "200", "250"} {
		msgs, err := a.Messages(ctx, messaging.Query{Chat: "user:7", ID: id})
		require.NoError(t, err)
		require.Len(t, msgs, 1, id)
		assert.True(t, msgs[0].Revoked, id)
		assert.Empty(t, msgs[0].Text, id)
	}

	msgs, err := a.Messages(ctx, messaging.Query{Chat: "user:7", ID: "111"})
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.False(t, msgs[0].Revoked, "a message not deleted stays")
}
