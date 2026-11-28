package telegram

import (
	"errors"
	"testing"

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
