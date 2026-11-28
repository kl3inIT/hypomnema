package telegram

// Sending as the owner (issue #1184), as mautrix/telegram sends a message (pkg/connector/handlematrix.go:540-680): a
// text with messages.sendMessage; a file uploaded with gotd's uploader and sent with messages.sendMedia, a JPEG or a
// PNG as a photo and anything else as a document with its name and type (transferMediaToTelegram, :371-415), the
// text as its caption; a reply names the message it answers (makeInputReplyTo); each with a random id, and the sent
// message read back from Telegram's answer (makeSendResponse, :682-760). What is sent is kept as the owner's, its
// file too, as whatsapp keeps what it sends (whatsapp/send.go). Whether it may be sent at all is the control plane's
// decision (serviceproxy's telegram.send), made before this is called.

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gotd/td/crypto"
	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"

	"github.com/kl3inIT/hypomnema/internal/controlplane/messaging"
)

// fullAPI is what sending and the files ask of Telegram besides api; gotd's *tg.Client is it.
type fullAPI interface {
	api
	uploader.Client
	downloader.Client
	MessagesSendMessage(ctx context.Context, req *tg.MessagesSendMessageRequest) (tg.UpdatesClass, error)
	MessagesSendMedia(ctx context.Context, req *tg.MessagesSendMediaRequest) (tg.UpdatesClass, error)
	MessagesGetMessages(ctx context.Context, ids []tg.InputMessageClass) (tg.MessagesMessagesClass, error)
	ChannelsGetMessages(ctx context.Context, req *tg.ChannelsGetMessagesRequest) (tg.MessagesMessagesClass, error)
}

// liveFull is the connection's API for sending and files.
func (a *Account) liveFull() (fullAPI, error) {
	client, err := a.liveAPI()
	if err != nil {
		return nil, err
	}

	full, ok := client.(fullAPI)
	if !ok {
		return nil, errors.New("telegram: this connection cannot send")
	}

	return full, nil
}

// Send sends a text, or a file with the text as its caption, in a chat as the owner, replying to ReplyTo when it is
// set, and keeps it.
func (a *Account) Send(ctx context.Context, out messaging.Outgoing) (messaging.Message, error) {
	if strings.TrimSpace(out.Text) == "" && len(out.File) == 0 {
		return messaging.Message{}, errors.New("telegram: a message needs a text or a file")
	}

	client, err := a.liveFull()
	if err != nil {
		return messaging.Message{}, err
	}

	chat := strings.TrimSpace(out.Chat)

	peer, err := a.inputPeer(ctx, chat)
	if err != nil {
		return messaging.Message{}, err
	}

	var replyTo tg.InputReplyToClass

	if out.ReplyTo != "" {
		id, err := strconv.Atoi(strings.TrimSpace(out.ReplyTo))
		if err != nil || id <= 0 {
			return messaging.Message{}, fmt.Errorf("telegram: reply_to %q is not a message's id", out.ReplyTo)
		}

		replyTo = &tg.InputReplyToMessage{ReplyToMsgID: id}
	}

	randomID, err := crypto.RandInt64(crypto.DefaultRand())
	if err != nil {
		return messaging.Message{}, err
	}

	var res tg.UpdatesClass

	if len(out.File) == 0 {
		req := &tg.MessagesSendMessageRequest{Peer: peer, Message: out.Text, RandomID: randomID}
		if replyTo != nil {
			req.SetReplyTo(replyTo)
		}

		res, err = client.MessagesSendMessage(ctx, req)
	} else {
		var media tg.InputMediaClass

		media, err = a.upload(ctx, client, out.FileName, out.File)
		if err != nil {
			return messaging.Message{}, err
		}

		req := &tg.MessagesSendMediaRequest{Peer: peer, Media: media, Message: out.Text, RandomID: randomID}
		if replyTo != nil {
			req.SetReplyTo(replyTo)
		}

		res, err = client.MessagesSendMedia(ctx, req)
	}

	if err != nil {
		return messaging.Message{}, fmt.Errorf("telegram: sending: %w", err)
	}

	return a.keepSent(ctx, chat, out, res, randomID)
}

// upload uploads a file and says how it is sent: a JPEG or a PNG as a photo, anything else as a document with its
// name and type, as mautrix does.
func (a *Account) upload(ctx context.Context, client fullAPI, name string, data []byte) (tg.InputMediaClass, error) {
	name = filepath.Base(strings.TrimSpace(name))
	if name == "" || name == "." || name == "/" {
		name = "file"
	}

	file, err := uploader.NewUploader(client).FromBytes(ctx, name, data)
	if err != nil {
		return nil, fmt.Errorf("telegram: uploading %s: %w", name, err)
	}

	kind := fileType(name, data)
	if kind == "image/jpeg" || kind == "image/png" {
		return &tg.InputMediaUploadedPhoto{File: file}, nil
	}

	return &tg.InputMediaUploadedDocument{
		File: file, MimeType: kind,
		Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: name}},
	}, nil
}

// fileType is a file's type by its name, else by its bytes, as whatsapp's send detects it.
func fileType(name string, data []byte) string {
	if t := mime.TypeByExtension(strings.ToLower(filepath.Ext(name))); t != "" {
		t, _, _ = strings.Cut(t, ";")

		return t
	}

	t, _, _ := strings.Cut(http.DetectContentType(data), ";")

	return t
}

// keepSent keeps the message Telegram's answer says was sent, with its file, and answers it as kept. A short answer
// (updateShortSentMessage) has no message: it is made of what was sent.
func (a *Account) keepSent(ctx context.Context, chat string, out messaging.Outgoing, res tg.UpdatesClass,
	randomID int64,
) (messaging.Message, error) {
	m, e := sentMessage(res, randomID)
	if m == nil {
		short, ok := res.(*tg.UpdateShortSentMessage)
		if !ok {
			return messaging.Message{}, fmt.Errorf("telegram: Telegram's answer %T names no message sent", res)
		}

		m = &tg.Message{ID: short.ID, Date: short.Date, Out: true, Message: out.Text, PeerID: peerOf(chat)}
		if replyTo, err := strconv.Atoi(out.ReplyTo); err == nil && replyTo > 0 {
			header := &tg.MessageReplyHeader{}
			header.SetReplyToMsgID(replyTo)
			m.SetReplyTo(header)
		}

		if media, ok := short.GetMedia(); ok {
			m.SetMedia(media)
		}
	}

	if m.Date == 0 {
		m.Date = int(time.Now().Unix())
	}

	k, ok := convert(a.userID, m)
	if !ok {
		return messaging.Message{}, errors.New("telegram: the message sent could not be read back")
	}

	if _, err := a.keep(ctx, e, k); err != nil {
		return messaging.Message{}, err
	}

	id := strconv.Itoa(m.ID)

	if len(out.File) > 0 {
		if err := a.keepFile(ctx, k.chat, id, k, out.File); err != nil {
			return messaging.Message{}, err
		}
	}

	msgs, err := a.store.messages(ctx, a.key, messaging.Query{Chat: k.chat, ID: id, Limit: 1})
	if err != nil || len(msgs) == 0 {
		return messaging.Message{}, errors.Join(errors.New("telegram: the message sent is not kept"), err)
	}

	return msgs[0], nil
}

// sentMessage is the message an answer of messages.sendMessage or sendMedia says was sent, with its entities: the
// new message whose id updateMessageID gives for the random id (mautrix's makeSendResponse).
func sentMessage(res tg.UpdatesClass, randomID int64) (*tg.Message, tg.Entities) {
	var (
		updates []tg.UpdateClass
		e       tg.Entities
	)

	switch u := res.(type) {
	case *tg.Updates:
		updates, e = u.Updates, tg.EntitiesFrom(u.Users, u.Chats)
	case *tg.UpdatesCombined:
		updates, e = u.Updates, tg.EntitiesFrom(u.Users, u.Chats)
	default:
		return nil, e
	}

	id := 0

	for _, u := range updates {
		if u, ok := u.(*tg.UpdateMessageID); ok && u.RandomID == randomID {
			id = u.ID
		}
	}

	for _, u := range updates {
		var mc tg.MessageClass

		switch u := u.(type) {
		case *tg.UpdateNewMessage:
			mc = u.Message
		case *tg.UpdateNewChannelMessage:
			mc = u.Message
		}

		if m, ok := mc.(*tg.Message); ok && (id == 0 || m.ID == id) {
			return m, e
		}
	}

	return nil, e
}

// peerOf is a chat's peer, from its id.
func peerOf(chat string) tg.PeerClass {
	kind, id, err := parseID(chat)
	if err != nil {
		return nil
	}

	switch kind {
	case peerUser:
		return &tg.PeerUser{UserID: id}
	case peerChat:
		return &tg.PeerChat{ChatID: id}
	default:
		return &tg.PeerChannel{ChannelID: id}
	}
}
