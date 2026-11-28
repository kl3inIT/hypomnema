package telegram

import (
	"context"
	"os"
	"sync"
	"testing"

	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/tg"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/messaging"
)

// fakeFiles is the fake Telegram that also sends, uploads and serves files: what each send asked, the parts
// uploaded, the messages getMessages answers, and each file's bytes by its document or photo id.
type fakeFiles struct {
	*fakeTelegram
	// downloader.Client's methods a small file never asks (hashes, CDN, web files) are left nil.
	downloader.Client

	mu        sync.Mutex
	sent      []any
	parts     int
	answer    func(req any) tg.UpdatesClass
	stored    map[string]*tg.Message
	files     map[int64][]byte
	downloads int
}

func newFakeFiles() *fakeFiles {
	return &fakeFiles{fakeTelegram: newFakeTelegram(), stored: map[string]*tg.Message{}, files: map[int64][]byte{}}
}

func (f *fakeFiles) MessagesSendMessage(_ context.Context, req *tg.MessagesSendMessageRequest,
) (tg.UpdatesClass, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.sent = append(f.sent, req)

	return f.answer(req), nil
}

func (f *fakeFiles) MessagesSendMedia(_ context.Context, req *tg.MessagesSendMediaRequest) (tg.UpdatesClass, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.sent = append(f.sent, req)

	return f.answer(req), nil
}

func (f *fakeFiles) UploadSaveFilePart(context.Context, *tg.UploadSaveFilePartRequest) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.parts++

	return true, nil
}

func (f *fakeFiles) UploadSaveBigFilePart(context.Context, *tg.UploadSaveBigFilePartRequest) (bool, error) {
	return true, nil
}

func (f *fakeFiles) MessagesGetMessages(_ context.Context, ids []tg.InputMessageClass) (tg.MessagesMessagesClass,
	error,
) {
	return f.answerMessages("", ids), nil
}

func (f *fakeFiles) ChannelsGetMessages(_ context.Context, req *tg.ChannelsGetMessagesRequest,
) (tg.MessagesMessagesClass, error) {
	in, _ := req.Channel.(*tg.InputChannel)

	return f.answerMessages(channelID(in.ChannelID), req.ID), nil
}

func (f *fakeFiles) answerMessages(chat string, ids []tg.InputMessageClass) tg.MessagesMessagesClass {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := &tg.MessagesMessages{}

	for _, id := range ids {
		in, _ := id.(*tg.InputMessageID)
		for key, m := range f.stored {
			if m.ID == in.ID && (chat == "" || key == chat) {
				out.Messages = append(out.Messages, m)
			}
		}
	}

	return out
}

func (f *fakeFiles) UploadGetFile(_ context.Context, req *tg.UploadGetFileRequest) (tg.UploadFileClass, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	var id int64

	switch loc := req.Location.(type) {
	case *tg.InputDocumentFileLocation:
		id = loc.ID
	case *tg.InputPhotoFileLocation:
		id = loc.ID
	}

	if req.Offset == 0 {
		f.downloads++
	}

	data := f.files[id]
	if req.Offset >= int64(len(data)) {
		return &tg.UploadFile{Type: &tg.StorageFileUnknown{}}, nil
	}

	return &tg.UploadFile{Type: &tg.StorageFileUnknown{}, Bytes: data[req.Offset:]}, nil
}

// connectedAccount is a test account connected to a fake Telegram that knows Trung (user:7) and the PMO channel.
func connectedAccount(t *testing.T) (*Account, *fakeFiles) {
	t.Helper()

	a := testAccount(t)
	server := newFakeFiles()
	ctx := t.Context()

	require.NoError(t, a.states.SetUserAccessHash(ctx, self, 7, 7000))
	require.NoError(t, a.states.SetChannelAccessHash(ctx, self, 900, 900000))

	a.setClient(server)

	return a, server
}

// Scenario (issue #1184): the owner approved a reply to Trung. It goes as his message, replying to the one it answers,
// and is kept as his, so that the next read shows it.
func TestATextIsSentAsAReplyAndKept(t *testing.T) {
	t.Parallel()

	a, server := connectedAccount(t)
	server.answer = func(any) tg.UpdatesClass {
		return &tg.UpdateShortSentMessage{Out: true, ID: 100, Date: 1_800_000_000}
	}

	sent, err := a.Send(t.Context(), messaging.Outgoing{Chat: "user:7", Text: "Dạ vâng ạ", ReplyTo: "5"})
	require.NoError(t, err)
	assert.Equal(t, "100", sent.ID)
	assert.True(t, sent.FromMe)
	assert.Equal(t, "Dạ vâng ạ", sent.Text)
	assert.Equal(t, "5", sent.QuotedID)

	require.Len(t, server.sent, 1)
	req, ok := server.sent[0].(*tg.MessagesSendMessageRequest)
	require.True(t, ok)
	assert.Equal(t, &tg.InputPeerUser{UserID: 7, AccessHash: 7000}, req.Peer)
	assert.Equal(t, "Dạ vâng ạ", req.Message)
	assert.Equal(t, &tg.InputReplyToMessage{ReplyToMsgID: 5}, req.ReplyTo)
	assert.NotZero(t, req.RandomID)

	assert.Equal(t, []string{"100"}, keptIDs(t, a, "user:7"))
}

// A file is uploaded and sent with the text as its caption: a PNG as a photo, a PDF as a document with its name. The
// sent file is kept as the message's, as whatsapp keeps what it sends.
func TestAFileIsSentWithItsCaptionAndKept(t *testing.T) {
	t.Parallel()

	a, server := connectedAccount(t)
	next := 200
	server.answer = func(req any) tg.UpdatesClass {
		next++
		media := &tg.MessageMediaDocument{}
		media.SetDocument(&tg.Document{ID: int64(next), MimeType: "application/pdf",
			Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: "quote.pdf"}}})

		m := &tg.Message{ID: next, Out: true, PeerID: &tg.PeerChannel{ChannelID: 900}, Date: 1_800_000_000,
			Message: req.(*tg.MessagesSendMediaRequest).Message}
		m.SetMedia(media)

		return &tg.Updates{Updates: []tg.UpdateClass{
			&tg.UpdateMessageID{ID: next, RandomID: req.(*tg.MessagesSendMediaRequest).RandomID},
			&tg.UpdateNewChannelMessage{Message: m},
		}}
	}

	sent, err := a.Send(t.Context(), messaging.Outgoing{
		Chat: "channel:900", Text: "Báo giá", File: []byte("%PDF-1.7"), FileName: "quote.pdf",
	})
	require.NoError(t, err)
	assert.Equal(t, "201", sent.ID)
	assert.Equal(t, "Báo giá", sent.Caption)
	assert.Equal(t, "quote.pdf", sent.FileName)
	assert.True(t, sent.MediaKept, "the file sent is kept")

	_, err = a.Send(t.Context(), messaging.Outgoing{Chat: "channel:900", File: []byte("\x89PNG"), FileName: "a.png"})
	require.NoError(t, err)

	require.Len(t, server.sent, 2)
	doc, ok := server.sent[0].(*tg.MessagesSendMediaRequest)
	require.True(t, ok)
	assert.Equal(t, &tg.InputPeerChannel{ChannelID: 900, AccessHash: 900000}, doc.Peer)
	assert.Equal(t, "Báo giá", doc.Message)
	media, ok := doc.Media.(*tg.InputMediaUploadedDocument)
	require.True(t, ok, "%T", doc.Media)
	assert.Equal(t, "application/pdf", media.MimeType)
	assert.Contains(t, media.Attributes, &tg.DocumentAttributeFilename{FileName: "quote.pdf"})

	photo, ok := server.sent[1].(*tg.MessagesSendMediaRequest)
	require.True(t, ok)
	assert.IsType(t, &tg.InputMediaUploadedPhoto{}, photo.Media, "a PNG goes as a photo")
	assert.Positive(t, server.parts)

	got, err := a.Media(t.Context(), "channel:900", "201")
	require.NoError(t, err)
	data, err := os.ReadFile(got.Path)
	require.NoError(t, err)
	assert.Equal(t, "%PDF-1.7", string(data))
	assert.Zero(t, server.downloads, "a file sent is not downloaded again")
}

// A send needs a connected session and a chat whose access hash is known, and a text or a file.
func TestASendThatCannotGoSaysWhy(t *testing.T) {
	t.Parallel()

	a, _ := connectedAccount(t)
	ctx := t.Context()

	_, err := a.Send(ctx, messaging.Outgoing{Chat: "user:8", Text: "hi"})
	require.ErrorIs(t, err, errNoAccessHash)

	_, err = a.Send(ctx, messaging.Outgoing{Chat: "user:7"})
	require.Error(t, err)

	a.setClient(nil)
	_, err = a.Send(ctx, messaging.Outgoing{Chat: "user:7", Text: "hi"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be connected")
}

// Scenario (issue #1184): a Bot opens the file of a message Trung sent. Telegram's file references expire, so the
// message is asked again for its file, as mautrix's direct download refetches it (directdownload.go:46-90); the file is
// downloaded once and kept, and the kept one answers next time.
func TestAMessagesFileIsDownloadedOnceAndKept(t *testing.T) {
	t.Parallel()

	a, server := connectedAccount(t)
	ctx := t.Context()

	m := &tg.Message{ID: 9, PeerID: &tg.PeerUser{UserID: 7}, Date: 1_800_000_000, Message: "the quote"}
	media := &tg.MessageMediaDocument{}
	media.SetDocument(&tg.Document{ID: 77, AccessHash: 1, FileReference: []byte("ref"), Size: 9,
		MimeType:   "application/pdf",
		Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: "quote.pdf"}}})
	m.SetMedia(media)

	k, ok := convert(self, m)
	require.True(t, ok)
	_, err := a.store.keepMessage(ctx, a.key, k, fallbackChat("user:7"))
	require.NoError(t, err)

	server.stored["user:7"] = m
	server.files[77] = []byte("%PDF-quote")

	got, err := a.Media(ctx, "user:7", "9")
	require.NoError(t, err)
	assert.Equal(t, "application/pdf", got.MimeType)
	assert.Equal(t, "quote.pdf", got.FileName)

	data, err := os.ReadFile(got.Path)
	require.NoError(t, err)
	assert.Equal(t, "%PDF-quote", string(data))

	again, err := a.Media(ctx, "user:7", "9")
	require.NoError(t, err)
	assert.Equal(t, got.Path, again.Path)
	assert.Equal(t, 1, server.downloads, "downloaded once")

	msgs, err := a.Messages(ctx, messaging.Query{Chat: "user:7", ID: "9"})
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.True(t, msgs[0].MediaKept)

	_, err = a.Media(ctx, "user:7", "404")
	require.Error(t, err, "a message not kept has no file")
}

// A channel's message is asked of the channel.
func TestAChannelMessagesFileIsAskedOfTheChannel(t *testing.T) {
	t.Parallel()

	a, server := connectedAccount(t)
	ctx := t.Context()

	m := &tg.Message{ID: 3, PeerID: &tg.PeerChannel{ChannelID: 900}, Date: 1_800_000_000}
	photo := &tg.MessageMediaPhoto{}
	photo.SetPhoto(&tg.Photo{ID: 55, AccessHash: 2, FileReference: []byte("ref"),
		Sizes: []tg.PhotoSizeClass{&tg.PhotoSize{Type: "x", W: 800, H: 600, Size: 4}}})
	m.SetMedia(photo)

	k, ok := convert(self, m)
	require.True(t, ok)
	_, err := a.store.keepMessage(ctx, a.key, k, fallbackChat("channel:900"))
	require.NoError(t, err)

	server.stored["channel:900"] = m
	server.files[55] = []byte("JPEG")

	got, err := a.Media(ctx, "channel:900", "3")
	require.NoError(t, err)
	assert.Equal(t, "image/jpeg", got.MimeType)

	data, err := os.ReadFile(got.Path)
	require.NoError(t, err)
	assert.Equal(t, "JPEG", string(data))
}
