package whatsapp

import (
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/kl3inIT/hypomnema/internal/controlplane/messaging"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

// A device whatsmeow no longer keeps, because WhatsApp logged it out while the control plane was stopped, opens
// as needing a link rather than failing the start (MemoryOS bots.go, device).
func TestADeviceNoLongerKeptOpensLoggedOut(t *testing.T) {
	t.Parallel()

	svc, err := Open(t.Context(), filepath.Join(t.TempDir(), "whatsapp.db"), slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.Close() })

	acc, err := svc.Open(t.Context(), "84912345678:11@s.whatsapp.net")
	require.NoError(t, err)
	assert.Equal(t, "+84912345678", acc.Name())
	assert.True(t, acc.State().NeedsLink())
}

// The session's state follows WhatsApp's events as MemoryOS's bridge reports them (bots.go, handle): a drop of a
// connected session comes back by itself, while a logout, a ban or a replacement is not a mere drop.
func TestTheSessionFollowsWhatsAppsEvents(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		events []any
		want   messaging.Status
	}{
		"connected":               {[]any{&events.Connected{}}, messaging.StatusConnected},
		"dropped":                 {[]any{&events.Connected{}, &events.Disconnected{}}, messaging.StatusDisconnected},
		"logged out on the phone": {[]any{&events.Connected{}, &events.LoggedOut{}}, messaging.StatusLoggedOut},
		"replaced, then dropped": {
			[]any{&events.Connected{}, &events.StreamReplaced{}, &events.Disconnected{}}, messaging.StatusReplaced,
		},
		"banned":   {[]any{&events.TemporaryBan{Code: events.TempBanBlockedByUsers, Expire: time.Hour}}, messaging.StatusBanned},
		"outdated": {[]any{&events.ClientOutdated{}}, messaging.StatusOutdated},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			acc := linkedAccount(t)
			for _, e := range tc.events {
				acc.handle(e)
			}

			assert.Equal(t, tc.want, acc.State().Status)
		})
	}
}

// A logout WhatsApp did not take keeps the session, to try again, unless WhatsApp had logged the device out
// already (review of #622).
func TestALogoutThatFailedKeepsTheSession(t *testing.T) {
	t.Parallel()

	svc, err := Open(t.Context(), filepath.Join(t.TempDir(), "whatsapp.db"), slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.Close() })

	linked := svc.newAccount(t.Context(), svc.container.NewDevice())
	linked.setState(messaging.State{Status: messaging.StatusConnected})
	require.Error(t, linked.Logout(t.Context()), "WhatsApp was not reached: the device may still be on the phone")

	gone := svc.newAccount(t.Context(), svc.container.NewDevice())
	gone.setState(messaging.State{Status: messaging.StatusLoggedOut})
	require.NoError(t, gone.Logout(t.Context()), "WhatsApp had ended the link: only the session was left")
}

// linkedAccount is an account of a device linked to the owner's number, with no connection: its events are
// given by the test.
func linkedAccount(t *testing.T) *account {
	t.Helper()

	svc, err := Open(t.Context(), filepath.Join(t.TempDir(), "whatsapp.db"), slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.Close() })

	device := svc.container.NewDevice()
	me := types.NewADJID("84900000001", 0, 11)
	device.ID = &me
	device.Account = &waAdv.ADVSignedDeviceIdentity{
		Details: []byte{0}, AccountSignature: make([]byte, 64), AccountSignatureKey: make([]byte, 32),
		DeviceSignature: make([]byte, 64),
	}
	// Saved as a pairing saves it, which gives it its contacts and LIDs.
	require.NoError(t, svc.container.PutDevice(t.Context(), device))

	return svc.newAccount(t.Context(), device)
}

func incoming(chat, sender types.JID, id, push string, at time.Time, m *waE2E.Message) *events.Message {
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: sender, IsGroup: chat.Server == types.GroupServer},
			ID:            id, PushName: push, Timestamp: at,
		},
		Message: m,
	}
}

// Scenario: every chat is kept as WhatsApp sends it (issue #631). A group's text, a reply with an image and its
// caption, and the owner's own message from his phone are kept under the group's name, the latest first; a
// sender's edit replaces the text and a revoke blanks it; a search finds a word of a caption; the history sent
// after the link adds an older direct chat, named by the person. Nothing is kept twice.
func TestEveryChatIsKeptAsWhatsAppSendsIt(t *testing.T) {
	t.Parallel()

	acc := linkedAccount(t)
	ctx := t.Context()
	group := types.NewJID("120363000000001", types.GroupServer)
	lan := types.NewJID("84911111111", types.DefaultUserServer)
	start := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)

	acc.nameGroup(group, "Kho hàng")
	acc.handle(incoming(group, lan, "m1", "Lan", start, &waE2E.Message{Conversation: proto.String("Hàng về chưa?")}))
	acc.handle(incoming(group, lan, "m2", "Lan", start.Add(time.Minute), &waE2E.Message{
		ImageMessage: &waE2E.ImageMessage{
			Caption: proto.String("phiếu nhập kho"), Mimetype: proto.String("image/jpeg"),
			ContextInfo: &waE2E.ContextInfo{StanzaID: proto.String("m1")},
		},
	}))
	mine := incoming(group, *acc.client.Store.ID, "m3", "", start.Add(2*time.Minute),
		&waE2E.Message{Conversation: proto.String("Về rồi")})
	mine.Info.IsFromMe = true
	acc.handle(mine)
	acc.handle(mine) // Sent twice, kept once.

	// Lan edits her first message, then revokes the image.
	acc.handle(incoming(group, lan, "e1", "Lan", start.Add(3*time.Minute), &waE2E.Message{
		ProtocolMessage: &waE2E.ProtocolMessage{
			Type:          waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(),
			Key:           &waCommon.MessageKey{ID: proto.String("m1"), RemoteJID: proto.String(group.String())},
			EditedMessage: &waE2E.Message{Conversation: proto.String("Hàng về chưa vậy?")},
		},
	}))

	found, err := acc.Messages(ctx, messaging.Query{Search: "nhập kho"})
	require.NoError(t, err)
	require.Len(t, found, 1, "a word of a caption is found")
	assert.Equal(t, "m2", found[0].ID)
	assert.Equal(t, "m1", found[0].QuotedID)
	assert.Equal(t, "image", found[0].MediaType)

	acc.handle(incoming(group, lan, "r1", "Lan", start.Add(4*time.Minute), &waE2E.Message{
		ProtocolMessage: &waE2E.ProtocolMessage{
			Type: waE2E.ProtocolMessage_REVOKE.Enum(),
			Key:  &waCommon.MessageKey{ID: proto.String("m2"), RemoteJID: proto.String(group.String())},
		},
	}))

	msgs, err := acc.Messages(ctx, messaging.Query{Chat: group.String()})
	require.NoError(t, err)
	require.Len(t, msgs, 3)
	assert.Equal(t, []string{"m3", "m2", "m1"}, []string{msgs[0].ID, msgs[1].ID, msgs[2].ID}, "the latest first")
	assert.True(t, msgs[0].FromMe)
	assert.Equal(t, "Kho hàng", msgs[0].ChatName)
	assert.True(t, msgs[1].Revoked)
	assert.Empty(t, msgs[1].Caption, "a revoked message keeps no content")
	assert.Equal(t, "Hàng về chưa vậy?", msgs[2].Text)
	assert.True(t, msgs[2].Edited)
	assert.Equal(t, "Lan", msgs[2].SenderName)

	found, err = acc.Messages(ctx, messaging.Query{Search: "nhập kho"})
	require.NoError(t, err)
	assert.Empty(t, found, "a revoked caption is no longer found")

	// The history WhatsApp sends after the link: an older direct chat with Minh.
	minh := types.NewJID("84922222222", types.DefaultUserServer)
	acc.handle(&events.HistorySync{Data: &waHistorySync.HistorySync{
		Conversations: []*waHistorySync.Conversation{{
			ID: proto.String(minh.String()),
			Messages: []*waHistorySync.HistorySyncMsg{{Message: &waWeb.WebMessageInfo{
				Key:              &waCommon.MessageKey{ID: proto.String("h1"), RemoteJID: proto.String(minh.String())},
				MessageTimestamp: proto.Uint64(1763107200), // 2025-11-14
				PushName:         proto.String("Minh"),
				Message:          &waE2E.Message{Conversation: proto.String("Gửi báo giá nhé")},
			}}},
		}},
	}})

	chats, err := acc.Chats(ctx, 0)
	require.NoError(t, err)
	require.Len(t, chats, 2)
	assert.Equal(t, messaging.Chat{
		ID: group.String(), Kind: "group", Name: "Kho hàng", LastMessageAt: start.Add(2 * time.Minute),
	}, chats[0])
	assert.Equal(t, "dm", chats[1].Kind)
	assert.Equal(t, "Minh", chats[1].Name)

	older, err := acc.Messages(ctx, messaging.Query{Before: start.Add(-24 * time.Hour)})
	require.NoError(t, err)
	require.Len(t, older, 1, "paging back reaches the history")
	assert.Equal(t, "Gửi báo giá nhé", older[0].Text)
}

// Scenario: a media message keeps what downloading it needs (issue #634). A file over 100 MiB is refused with why
// and is no longer pending; a file kept is answered from the disk without WhatsApp; the link ending removes the
// files with the mirror.
func TestAMediaMessageKeepsWhatDownloadingItNeeds(t *testing.T) {
	t.Parallel()

	acc := linkedAccount(t)
	ctx := t.Context()
	group := types.NewJID("120363000000002", types.GroupServer)
	lan := types.NewJID("84911111111", types.DefaultUserServer)
	doc := func(id string, length uint64) *events.Message {
		return incoming(group, lan, id, "Lan", time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC), &waE2E.Message{
			DocumentMessage: &waE2E.DocumentMessage{
				FileName: proto.String("bao-gia.pdf"), Mimetype: proto.String("application/pdf"),
				DirectPath: proto.String("/v/t62/doc-" + id), MediaKey: make([]byte, 32),
				FileSHA256: make([]byte, 32), FileEncSHA256: make([]byte, 32), FileLength: proto.Uint64(length),
			},
		})
	}

	// Kept, not downloaded: the account has no connection, so the queue's workers fail quietly.
	acc.Close()
	acc.handle(doc("small", 1024))
	acc.handle(doc("huge", 200<<20))

	pending, err := acc.mirror.pendingMedia(ctx, acc.mirrorKey(), 10)
	require.NoError(t, err)
	assert.Len(t, pending, 2, "both wait for their file")

	_, err = acc.Media(ctx, group.String(), "huge")
	require.ErrorContains(t, err, "larger than 100 MiB")

	pending, err = acc.mirror.pendingMedia(ctx, acc.mirrorKey(), 10)
	require.NoError(t, err)
	require.Len(t, pending, 1, "a file too big is not tried again")
	assert.Equal(t, "small", pending[0].id)

	// A file already kept is answered from the disk.
	path := filepath.Join(acc.mediaDir, acc.mirrorKey(), "kept.pdf")
	require.NoError(t, writePrivate(path, []byte("%PDF")))
	require.NoError(t, acc.mirror.keptMedia(ctx, acc.mirrorKey(), group.String(), "small", path, ""))

	media, err := acc.Media(ctx, group.String(), "small")
	require.NoError(t, err)
	assert.Equal(t, messaging.Media{Path: path, MimeType: "application/pdf", FileName: "bao-gia.pdf"}, media)

	msgs, err := acc.Messages(ctx, messaging.Query{Chat: group.String()})
	require.NoError(t, err)
	require.Len(t, msgs, 2)
	assert.True(t, msgs[0].MediaKept || msgs[1].MediaKept)

	acc.forgetMirror(ctx)
	assert.NoFileExists(t, path, "the link ending removes the files")
}

// A mirror made before files were downloaded (#631) is brought to this version as it opens.
func TestAMirrorOfTheFirstVersionIsUpgraded(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "whatsapp.db")
	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: path})
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), schema)
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), `INSERT INTO wa_messages (account, chat_jid, msg_id, ts, from_me, text)
		VALUES ('84900000001', '120363@g.us', 'old', 1, 0, 'kept before')`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	svc, err := Open(t.Context(), path, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.Close() })

	msgs, err := svc.mirror.messages(t.Context(), "84900000001", messaging.Query{})
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.False(t, msgs[0].MediaKept)
}

// Older messages of a chat (issues #634, #651): a chat with nothing kept has no anchor to ask from; the phone's
// on-demand answer is kept, tells the waiting request whether the chat's start was reached, and an answer that
// repeats kept messages adds none.
func TestThePhonesAnswerForOlderMessagesIsKept(t *testing.T) {
	t.Parallel()

	acc := linkedAccount(t)
	ctx := t.Context()
	minh := types.NewJID("84922222222", types.DefaultUserServer)

	_, err := acc.Backfill(ctx, minh.String(), 0)
	require.ErrorIs(t, err, errNoMessage, "nothing kept, nothing to ask from")

	waiting := make(chan backfilled, 2)
	acc.waiting[minh.String()] = waiting

	answer := &events.HistorySync{Data: &waHistorySync.HistorySync{
		SyncType: waHistorySync.HistorySync_ON_DEMAND.Enum(),
		Conversations: []*waHistorySync.Conversation{{
			ID:                       proto.String(minh.String()),
			EndOfHistoryTransferType: waHistorySync.Conversation_COMPLETE_AND_NO_MORE_MESSAGE_REMAIN_ON_PRIMARY.Enum(),
			Messages: []*waHistorySync.HistorySyncMsg{{Message: &waWeb.WebMessageInfo{
				Key:              &waCommon.MessageKey{ID: proto.String("old1"), RemoteJID: proto.String(minh.String())},
				MessageTimestamp: proto.Uint64(1700000000),
				Message:          &waE2E.Message{Conversation: proto.String("Tin cũ")},
			}}},
		}},
	}}

	acc.handle(answer)
	assert.True(t, (<-waiting).complete, "the phone said this is the start of the chat")

	// The phone may answer with what is kept already (#651): the count Backfill answers from does not grow.
	acc.handle(answer)
	<-waiting

	n, err := acc.mirror.count(ctx, acc.mirrorKey(), minh.String())
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	msgs, err := acc.Messages(ctx, messaging.Query{Chat: minh.String()})
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.Equal(t, "Tin cũ", msgs[0].Text)
}

// A reply quotes the message it answers with its sender and text, as wacli's buildTextReplyContextInfo does, so
// that WhatsApp shows it as a reply (issue #645); a message not kept cannot be replied to.
func TestAReplyQuotesTheMessageItAnswers(t *testing.T) {
	t.Parallel()

	acc := linkedAccount(t)
	ctx := t.Context()
	group := types.NewJID("120363000000003", types.GroupServer)
	lan := types.NewJID("84911111111", types.DefaultUserServer)

	acc.handle(incoming(group, lan, "q1", "Lan", time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC),
		&waE2E.Message{Conversation: proto.String("Báo giá đâu anh?")}))

	quote, err := acc.quote(ctx, group, "q1")
	require.NoError(t, err)

	msg, err := acc.build(ctx, messaging.Outgoing{Chat: group.String(), Text: "Đây em"}, quote)
	require.NoError(t, err)

	got := msg.GetExtendedTextMessage()
	require.NotNil(t, got)
	assert.Equal(t, "Đây em", got.GetText())
	assert.Equal(t, "q1", got.GetContextInfo().GetStanzaID())
	assert.Equal(t, lan.String(), got.GetContextInfo().GetParticipant())
	assert.Equal(t, "Báo giá đâu anh?", got.GetContextInfo().GetQuotedMessage().GetConversation())

	_, err = acc.quote(ctx, group, "never-kept")
	require.ErrorIs(t, err, errNoMessage)
}

// A file given up by the mirror's first version, which did not ask the phone, waits again for its download once
// the mirror is upgraded (issue #651); one given up for its size stays given up.
func TestAFileGivenUpBeforeIsAskedOfThePhone(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "whatsapp.db")
	db, err := sqlite.Open(ctx, sqlite.Config{Path: path})
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, schema+upgrades[0]+`
		CREATE TABLE wa_version (version INTEGER NOT NULL); INSERT INTO wa_version VALUES (1);
		INSERT INTO wa_messages (account, chat_jid, msg_id, ts, from_me, direct_path, media_error) VALUES
			('84900000001', '120363@g.us', 'gone', 2, 0, '/v/gone', 'WhatsApp no longer has it'),
			('84900000001', '120363@g.us', 'huge', 1, 0, '/v/huge', 'larger than 100 MiB');`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	svc, err := Open(ctx, path, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.Close() })

	pending, err := svc.mirror.pendingMedia(ctx, "84900000001", 10)
	require.NoError(t, err)
	assert.Equal(t, []mediaJob{{account: "84900000001", chat: "120363@g.us", id: "gone"}}, pending)
}
