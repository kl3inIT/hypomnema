package telegram

import (
	"strconv"
	"testing"

	"github.com/gotd/td/telegram/updates"
	"github.com/gotd/td/tg"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/messaging"
)

// Every chat is kept as its updates arrive, with what their entities say: a direct chat and a group are kept, the
// person who wrote in the group is named but is not a chat of his own, and the access hashes later requests need are
// kept.
func TestEveryChatIsKeptAsItsUpdatesArrive(t *testing.T) {
	t.Parallel()

	a := testAccount(t)
	ctx := t.Context()
	trung, laura := user(7, "Trung"), user(8, "Laura")
	group := channel(9, "VMAP AI")

	dm := message(&tg.PeerUser{UserID: 7}, 1, "did the FAQ sync land?")

	inGroup := message(&tg.PeerChannel{ChannelID: 9}, 50, "standup at ten")
	inGroup.SetFromID(&tg.PeerUser{UserID: 8})

	mine := message(&tg.PeerUser{UserID: 7}, 2, "yes, this morning")
	mine.Out = true

	require.NoError(t, a.handler().Handle(ctx, &tg.Updates{
		Updates: []tg.UpdateClass{
			&tg.UpdateNewMessage{Message: dm},
			&tg.UpdateNewChannelMessage{Message: inGroup},
			&tg.UpdateNewMessage{Message: mine},
		},
		Users: []tg.UserClass{trung, laura},
		Chats: []tg.ChatClass{group},
	}))

	chats, err := a.Chats(ctx, 0)
	require.NoError(t, err)

	byID := map[string]messaging.Chat{}
	for _, c := range chats {
		byID[c.ID] = c
	}

	require.Len(t, byID, 2, "the group's member is not a chat")
	assert.Equal(t, kindDirect, byID["user:7"].Kind)
	assert.Equal(t, "Trung", byID["user:7"].Name)
	assert.Equal(t, kindGroup, byID["channel:9"].Kind)
	assert.Equal(t, "VMAP AI", byID["channel:9"].Name)

	msgs, err := a.Messages(ctx, messaging.Query{Chat: "channel:9"})
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.Equal(t, "user:8", msgs[0].Sender)
	assert.Equal(t, "Laura", msgs[0].SenderName)

	msgs, err = a.Messages(ctx, messaging.Query{Chat: "user:7"})
	require.NoError(t, err)
	require.Len(t, msgs, 2)
	assert.True(t, msgs[0].FromMe)
	assert.Equal(t, userID(self), msgs[0].Sender, "the account's own message is from itself")
	assert.Equal(t, "user:7", msgs[1].Sender, "a direct chat's message is from its other party")

	peer, err := a.inputPeer(ctx, "channel:9")
	require.NoError(t, err)
	assert.Equal(t, &tg.InputPeerChannel{ChannelID: 9, AccessHash: 9000}, peer)

	peer, err = a.inputPeer(ctx, "user:7")
	require.NoError(t, err)
	assert.Equal(t, &tg.InputPeerUser{UserID: 7, AccessHash: 7000}, peer)
}

// A bot's chat and a broadcast channel are kept with the kinds the Inbox skips by default; a chat the account left
// goes off the list when an update says so, and its messages stay.
func TestUpdatesKeepEachChatsKindAndWhetherItIsListed(t *testing.T) {
	t.Parallel()

	a := testAccount(t)
	ctx := t.Context()
	bot := user(5, "BotFather")
	bot.Bot = true
	news := channel(6, "News")
	news.Megagroup, news.Broadcast = false, true
	group := channel(9, "VMAP AI")

	require.NoError(t, a.handler().Handle(ctx, &tg.Updates{
		Updates: []tg.UpdateClass{
			&tg.UpdateNewMessage{Message: message(&tg.PeerUser{UserID: 5}, 1, "/start")},
			&tg.UpdateNewChannelMessage{Message: message(&tg.PeerChannel{ChannelID: 6}, 1, "headline")},
			&tg.UpdateNewChannelMessage{Message: message(&tg.PeerChannel{ChannelID: 9}, 1, "hi")},
		},
		Users: []tg.UserClass{bot},
		Chats: []tg.ChatClass{news, group},
	}))

	left := channel(9, "VMAP AI")
	left.Left = true
	require.NoError(t, a.handler().Handle(ctx, &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdateChannel{ChannelID: 9}},
		Chats:   []tg.ChatClass{left},
	}))

	chats, err := a.Chats(ctx, 0)
	require.NoError(t, err)

	kinds := map[string]string{}
	for _, c := range chats {
		kinds[c.ID] = c.Kind
	}

	assert.Equal(t, map[string]string{"user:5": kindBot, "channel:6": kindBroadcast}, kinds)
	assert.Equal(t, []string{"1"}, keptIDs(t, a, "channel:9"), "a chat left keeps its messages")
}

// An edit replaces a kept message's text, one Telegram hides (a reaction) changes nothing, and deletions of a direct
// chat and of a channel mark theirs.
func TestEditsAndDeletionsReachTheKeptMessages(t *testing.T) {
	t.Parallel()

	a := testAccount(t)
	ctx := t.Context()
	h := a.handler()
	dm, ch := &tg.PeerUser{UserID: 7}, &tg.PeerChannel{ChannelID: 9}

	require.NoError(t, h.Handle(ctx, &tg.Updates{
		Updates: []tg.UpdateClass{
			&tg.UpdateNewMessage{Message: message(dm, 1, "draft")},
			&tg.UpdateNewMessage{Message: message(dm, 2, "second")},
			&tg.UpdateNewChannelMessage{Message: message(ch, 1, "in the channel")},
		},
		Users: []tg.UserClass{user(7, "Trung")},
		Chats: []tg.ChatClass{channel(9, "VMAP AI")},
	}))

	edited := message(dm, 1, "final")
	edited.SetEditDate(1_700_000_100)

	reaction := message(dm, 2, "second, as a reaction's edit")
	reaction.SetEditDate(1_700_000_100)
	reaction.EditHide = true

	require.NoError(t, h.Handle(ctx, &tg.Updates{Updates: []tg.UpdateClass{
		&tg.UpdateEditMessage{Message: edited},
		&tg.UpdateEditMessage{Message: reaction},
		&tg.UpdateDeleteChannelMessages{ChannelID: 9, Messages: []int{1}},
	}}))

	msgs, err := a.Messages(ctx, messaging.Query{Chat: "user:7"})
	require.NoError(t, err)
	require.Len(t, msgs, 2)
	assert.Equal(t, "second", msgs[0].Text)
	assert.False(t, msgs[0].Edited)
	assert.Equal(t, "final", msgs[1].Text)
	assert.True(t, msgs[1].Edited)

	msgs, err = a.Messages(ctx, messaging.Query{Chat: "channel:9"})
	require.NoError(t, err)
	assert.True(t, msgs[0].Revoked)

	require.NoError(t, h.Handle(ctx, &tg.Updates{Updates: []tg.UpdateClass{
		&tg.UpdateDeleteMessages{Messages: []int{2}},
	}}))

	msgs, err = a.Messages(ctx, messaging.Query{Chat: "user:7", ID: "2"})
	require.NoError(t, err)
	assert.True(t, msgs[0].Revoked)
}

// A reply names the message it quotes; a message in a forum's topic names the topic's first message without replying
// to it; a photo's text is its caption, and a document gives its name and type.
func TestAMessageIsConvertedAsWhatsAppsAre(t *testing.T) {
	t.Parallel()

	forum := &tg.PeerChannel{ChannelID: 9}

	reply := message(forum, 10, "agreed")
	header := &tg.MessageReplyHeader{}
	header.SetReplyToMsgID(8)
	reply.SetReplyTo(header)

	inTopic := message(forum, 11, "in the topic")
	topic := &tg.MessageReplyHeader{ForumTopic: true}
	topic.SetReplyToMsgID(3)
	inTopic.SetReplyTo(topic)

	replyInTopic := message(forum, 12, "answer in the topic")
	both := &tg.MessageReplyHeader{ForumTopic: true}
	both.SetReplyToMsgID(10)
	both.SetReplyToTopID(3)
	replyInTopic.SetReplyTo(both)

	photo := message(forum, 13, "the slide")
	media := &tg.MessageMediaPhoto{}
	media.SetPhoto(&tg.Photo{ID: 1})
	photo.SetMedia(media)

	doc := message(forum, 14, "")
	doc.SetMedia(&tg.MessageMediaDocument{Document: &tg.Document{MimeType: "application/pdf",
		Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: "faq.pdf"}}}})

	voice := message(forum, 15, "")
	voice.SetMedia(&tg.MessageMediaDocument{Document: &tg.Document{MimeType: "audio/ogg",
		Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeAudio{Voice: true}}}})

	for msg, want := range map[*tg.Message]kept{
		reply:        {quotedID: "8", text: "agreed"},
		inTopic:      {text: "in the topic"},
		replyInTopic: {quotedID: "10", text: "answer in the topic"},
		photo:        {mediaType: mediaImage, caption: "the slide", mime: "image/jpeg"},
		doc:          {mediaType: mediaDocument, fileName: "faq.pdf", mime: "application/pdf"},
		voice:        {mediaType: mediaAudio, mime: "audio/ogg"},
	} {
		got, ok := convert(self, msg)
		require.True(t, ok)
		assert.Equal(t, want.quotedID, got.quotedID, msg.ID)
		assert.Equal(t, want.text, got.text, msg.ID)
		assert.Equal(t, want.mediaType, got.mediaType, msg.ID)
		assert.Equal(t, want.caption, got.caption, msg.ID)
		assert.Equal(t, want.fileName, got.fileName, msg.ID)
		assert.Equal(t, want.mime, got.mime, msg.ID)
	}

	_, ok := convert(self, &tg.MessageService{ID: 16, PeerID: forum})
	assert.False(t, ok, "a service message is not one people wrote")
}

// A channel's gap the updates manager could not recover (channelDifferenceTooLong) is filled: the latest messages
// it came with are kept, and those between the newest kept before and them are fetched from Telegram, page by page.
func TestAChannelsGapIsFilledFromItsHistory(t *testing.T) {
	t.Parallel()

	a := testAccount(t)
	ctx := t.Context()
	peer := &tg.PeerChannel{ChannelID: 9}
	server := newFakeTelegram()
	server.add(peer, ids(1, 260)...)

	for _, id := range ids(1, 10) {
		_, err := a.keep(ctx, tg.Entities{}, mustConvert(t, message(peer, id, "message")))
		require.NoError(t, err)
	}

	require.NoError(t, a.recoverGap(ctx, server, 10, updates.ChannelGap{
		ChannelID: 9, AccessHash: 9000, LocalPts: 10, RemotePts: 300, TopMessage: 260,
		Messages: []tg.MessageClass{message(peer, 260, "x"), message(peer, 259, "x"), message(peer, 258, "x")},
		Chats:    []tg.ChatClass{channel(9, "VMAP AI")},
	}))

	assert.Equal(t, strs(ids(1, 260)), keptIDs(t, a, "channel:9"))

	asked := server.asked["channel:9"]
	require.Len(t, asked, 3, "247 missed messages are three pages of 100")

	for _, req := range asked {
		assert.Equal(t, 10, req.MinID, "nothing older than the newest kept is fetched again")
	}

	assert.Equal(t, 258, asked[0].OffsetID)
}

// A channel nothing was kept of before its gap starts from the messages the gap came with.
func TestAGapInAChannelNeverKeptFetchesNothing(t *testing.T) {
	t.Parallel()

	a := testAccount(t)
	peer := &tg.PeerChannel{ChannelID: 9}
	server := newFakeTelegram()
	server.add(peer, ids(1, 100)...)

	require.NoError(t, a.recoverGap(t.Context(), server, 0, updates.ChannelGap{
		ChannelID: 9, AccessHash: 9000, TopMessage: 100, Messages: []tg.MessageClass{message(peer, 100, "x")},
	}))

	assert.Equal(t, []string{"100"}, keptIDs(t, a, "channel:9"))
	assert.Empty(t, server.asked)
}

// When the account's own updates cannot be recovered (differenceTooLong), every dialog is listed by pages, each chat
// kept with its latest message, and what was missed since the newest kept is fetched; at most maxCatchUp a chat.
func TestATooLongGapCatchesUpEveryDialog(t *testing.T) {
	t.Parallel()

	a := testAccount(t)
	ctx := t.Context()
	trung := &tg.PeerUser{UserID: 7}
	group := &tg.PeerChat{ChatID: 8}
	busy := &tg.PeerChannel{ChannelID: 9}
	server := newFakeTelegram()
	server.add(trung, ids(1, 20)...)
	server.add(busy, ids(1, 1000)...)

	for _, m := range []*tg.Message{message(trung, 5, "x"), message(busy, 10, "x")} {
		_, err := a.keep(ctx, tg.Entities{}, mustConvert(t, m))
		require.NoError(t, err)
	}

	page := func(peers []tg.PeerClass, tops []int) *tg.MessagesDialogsSlice {
		res := &tg.MessagesDialogsSlice{Count: 101, Users: []tg.UserClass{user(7, "Trung")},
			Chats: []tg.ChatClass{&tg.Chat{ID: 8, Title: "Team"}, channel(9, "Busy")}}
		for i, p := range peers {
			res.Dialogs = append(res.Dialogs, &tg.Dialog{Peer: p, TopMessage: tops[i]})
			res.Messages = append(res.Messages, message(p, tops[i], "latest"))
		}

		return res
	}

	// A full first page: the busy channel, 98 quiet basic groups, and Trung last, where the next page starts.
	first := []tg.PeerClass{busy}
	tops := []int{1000}

	for id := int64(100); id < 198; id++ {
		first = append(first, &tg.PeerChat{ChatID: id})
		tops = append(tops, 1)
	}

	first, tops = append(first, trung), append(tops, 20)
	server.dialogs = []tg.MessagesDialogsClass{page(first, tops), page([]tg.PeerClass{group}, []int{3})}

	require.NoError(t, a.store.restartDialogs(ctx, a.key, true))
	require.NoError(t, a.syncDialogs(ctx, server.conn()))

	chats, err := a.Chats(ctx, messaging.MaxChats)
	require.NoError(t, err)
	assert.Len(t, chats, 101)

	assert.Equal(t, strs(ids(5, 20)), keptIDs(t, a, "user:7"))
	assert.Equal(t, []string{"3"}, keptIDs(t, a, "chat:8"), "a chat nothing was kept of starts from its latest")

	var count, oldest int
	require.NoError(t, a.store.db.QueryRowContext(ctx, `SELECT COUNT(*), MIN(msg_id) FROM tg_messages
		WHERE chat_id = 'channel:9'`).Scan(&count, &oldest))
	assert.Equal(t, 2+maxCatchUp, count, "the newest kept, the latest, and maxCatchUp missed ones")
	assert.Equal(t, 10, oldest)

	require.Len(t, server.dialogsAsked, 2)
	assert.Equal(t, 20, server.dialogsAsked[1].OffsetID, "the next page starts after the last dialog")
	assert.Equal(t, &tg.InputPeerUser{UserID: 7, AccessHash: 7000}, server.dialogsAsked[1].OffsetPeer)
}

// The first listing after a link keeps every chat with its latest message and fetches no history.
func TestTheFirstListingKeepsEveryChatWithoutItsHistory(t *testing.T) {
	t.Parallel()

	a := testAccount(t)
	trung := &tg.PeerUser{UserID: 7}
	server := newFakeTelegram()
	server.add(trung, ids(1, 20)...)
	server.dialogs = []tg.MessagesDialogsClass{&tg.MessagesDialogs{
		Dialogs:  []tg.DialogClass{&tg.Dialog{Peer: trung, TopMessage: 20}},
		Messages: []tg.MessageClass{message(trung, 20, "latest")},
		Users:    []tg.UserClass{user(7, "Trung")},
	}}

	require.NoError(t, a.store.restartDialogs(t.Context(), a.key, true))
	require.NoError(t, a.syncDialogs(t.Context(), server.conn()))

	assert.Equal(t, []string{"20"}, keptIDs(t, a, "user:7"))
	assert.Empty(t, server.asked)
	assert.Len(t, server.dialogsAsked, 1, "a whole list in one answer has no next page")
}

// telegram_history's fetch: older messages than the oldest kept, by pages of 100, until the count or the chat's start.
func TestBackfillFetchesOlderMessagesByPages(t *testing.T) {
	t.Parallel()

	a := testAccount(t)
	ctx := t.Context()
	peer := &tg.PeerUser{UserID: 7}
	server := newFakeTelegram()
	server.add(peer, ids(1, 400)...)

	require.NoError(t, a.keepEntities(ctx, tg.EntitiesFrom([]tg.UserClass{user(7, "Trung")}, nil)))
	_, err := a.keep(ctx, tg.Entities{}, mustConvert(t, message(peer, 400, "latest")))
	require.NoError(t, err)

	got, err := a.backfill(ctx, server, "user:7", 150)
	require.NoError(t, err)
	assert.Equal(t, messaging.Backfilled{Added: 150, Complete: false}, got)
	assert.Equal(t, strs(ids(250, 400)), keptIDs(t, a, "user:7"))

	asked := server.asked["user:7"]
	require.Len(t, asked, 2)
	assert.Equal(t, tg.MessagesGetHistoryRequest{Peer: &tg.InputPeerUser{UserID: 7, AccessHash: 7000}, OffsetID: 400,
		Limit: 100}, asked[0])
	assert.Equal(t, 300, asked[1].OffsetID)
	assert.Equal(t, 50, asked[1].Limit)

	got, err = a.backfill(ctx, server, "user:7", 500)
	require.NoError(t, err)
	assert.Equal(t, messaging.Backfilled{Added: 249, Complete: true}, got)
}

// A chat whose access hash was never seen cannot be asked for, and a disconnected account asks nothing.
func TestBackfillNeedsAChatItCanName(t *testing.T) {
	t.Parallel()

	a := testAccount(t)

	_, err := a.backfill(t.Context(), newFakeTelegram(), "user:7", 10)
	require.ErrorIs(t, err, errNoAccessHash)

	_, err = a.Backfill(t.Context(), "chat:8", 10)
	require.Error(t, err)
}

func ids(from, to int) []int {
	out := make([]int, 0, to-from+1)
	for id := from; id <= to; id++ {
		out = append(out, id)
	}

	return out
}

func strs(ids []int) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = strconv.Itoa(id)
	}

	return out
}

func mustConvert(t *testing.T, m *tg.Message) kept {
	t.Helper()

	k, ok := convert(self, m)
	require.True(t, ok)

	return k
}
