package whatsapp

// Sending as the owner (issue #645), as wacli sends (internal/wa/send.go, cmd/wacli/send_file.go, send.go): a text
// as a conversation message; a file uploaded as an image, a video, an audio or a document by its type
// (resolveSendMediaType, detectSendFileMIME), with the text as its caption; a reply quotes the message it answers
// with its sender (buildTextReplyContextInfo); members tagged are listed in the same context's MentionedJID, as
// mautrix-whatsapp sends a mention (pkg/msgconv/from-matrix.go: generateContextInfo, convertPill), issue #852. What is sent is kept in the mirror as the owner's, as wacli stores
// what it sends: a linked device is not sent back its own messages. Whether it may be sent at all is the control
// plane's decision (serviceproxy.Judge), made before this is called.

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/kl3inIT/hypomnema/internal/controlplane/messaging"
)

// Send sends a message, or a file with its caption, in a chat as the owner, and keeps it.
func (a *account) Send(ctx context.Context, out messaging.Outgoing) (messaging.Message, error) {
	chat, err := types.ParseJID(strings.TrimSpace(out.Chat))
	if err != nil || chat.User == "" {
		return messaging.Message{}, fmt.Errorf("%w: chat %q is not a WhatsApp chat id", errNoMessage, out.Chat)
	}

	if out.Text == "" && len(out.File) == 0 {
		return messaging.Message{}, errors.New("whatsapp: a message needs a text or a file")
	}

	mentioned, err := mentionJIDs(out.Text, out.Mentions)
	if err != nil {
		return messaging.Message{}, err
	}

	if !a.client.IsConnected() {
		return messaging.Message{}, errors.New("whatsapp: the session is not connected now; try again later")
	}

	info, err := a.contextInfo(ctx, chat, out.ReplyTo, mentioned)
	if err != nil {
		return messaging.Message{}, err
	}

	msg, err := a.build(ctx, out, info)
	if err != nil {
		return messaging.Message{}, err
	}

	resp, err := a.client.SendMessage(ctx, chat, msg)
	if err != nil {
		return messaging.Message{}, fmt.Errorf("whatsapp: sending: %w", err)
	}

	own := types.EmptyJID
	if id := a.client.Store.ID; id != nil {
		own = id.ToNonAD()
	}

	at := resp.Timestamp
	if at.IsZero() {
		at = time.Now()
	}

	a.keep(ctx, parseLive(&events.Message{Info: types.MessageInfo{
		MessageSource: types.MessageSource{
			Chat: chat, Sender: own, IsFromMe: true, IsGroup: chat.Server == types.GroupServer,
		},
		ID: resp.ID, Timestamp: at,
	}, Message: msg}))

	if len(out.File) > 0 {
		a.keepSent(ctx, chat, resp.ID, out)
	}

	return messaging.Message{ID: resp.ID, Chat: chat.String(), At: at.UTC(), FromMe: true, Text: out.Text,
		QuotedID: out.ReplyTo, FileName: out.FileName}, nil
}

// contextInfo is what a message says of its context: the message it quotes and the members it tags, or nil when
// it does neither.
func (a *account) contextInfo(ctx context.Context, chat types.JID, replyTo string, mentioned []string,
) (*waE2E.ContextInfo, error) {
	info, err := a.quote(ctx, chat, replyTo)
	if err != nil || len(mentioned) == 0 {
		return info, err
	}

	if info == nil {
		info = &waE2E.ContextInfo{}
	}

	info.MentionedJID = mentioned

	return info, nil
}

// mentionJIDs are the JIDs of the members a message tags, given by a JID (a number's or a LID) or a bare number,
// once its text writes @ and each one's user part; a member it does not write is refused rather than sent untagged.
func mentionJIDs(text string, mentions []string) ([]string, error) {
	if missing := messaging.UntaggedMentions(text, mentions); len(missing) > 0 {
		return nil, fmt.Errorf("whatsapp: the text does not write @%s for %s: a member is tagged only where the "+
			"text writes @ and the number of their id", messaging.MentionUser(missing[0]), missing[0])
	}

	jids := make([]string, 0, len(mentions))

	for _, id := range mentions {
		id = strings.TrimSpace(id)

		jid := types.NewJID(messaging.MentionUser(id), types.DefaultUserServer)
		if strings.Contains(id, "@") {
			parsed, err := types.ParseJID(id)
			if err != nil {
				return nil, fmt.Errorf("whatsapp: %q is not a member's id", id)
			}

			jid = parsed.ToNonAD()
		}

		if (jid.Server != types.DefaultUserServer && jid.Server != types.HiddenUserServer) ||
			strings.Trim(jid.User, "0123456789") != "" {
			return nil, fmt.Errorf("whatsapp: %q is not a member's id: give it as whatsapp_messages gives a "+
				"sender, such as 84339013010@s.whatsapp.net or 168453096898812@lid", id)
		}

		if !slices.Contains(jids, jid.String()) {
			jids = append(jids, jid.String())
		}
	}

	return jids, nil
}

// quote is the context of a reply: the message answered and who sent it, as the mirror keeps it.
func (a *account) quote(ctx context.Context, chat types.JID, replyTo string) (*waE2E.ContextInfo, error) {
	replyTo = strings.TrimSpace(replyTo)
	if replyTo == "" {
		return nil, nil //nolint:nilnil // No reply: no context.
	}

	q, err := a.mirror.quoted(ctx, a.mirrorKey(), chat.String(), replyTo)
	if err != nil {
		return nil, err
	}

	participant := q.sender
	if q.fromMe || participant == "" {
		if id := a.client.Store.ID; id != nil {
			participant = id.ToNonAD().String()
		}
	}

	return &waE2E.ContextInfo{
		StanzaID: proto.String(replyTo), Participant: proto.String(participant),
		QuotedMessage: &waE2E.Message{Conversation: proto.String(q.text)},
	}, nil
}

// build is the message: a text, or a file uploaded with its caption, in the context given (a quote, mentions).
func (a *account) build(ctx context.Context, out messaging.Outgoing, quote *waE2E.ContextInfo) (*waE2E.Message, error) {
	if len(out.File) == 0 {
		if quote == nil {
			return &waE2E.Message{Conversation: proto.String(out.Text)}, nil
		}

		return &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text: proto.String(out.Text), ContextInfo: quote,
		}}, nil
	}

	mimeType := sendMIME(out.FileName, out.File)
	kind := sendKind(mimeType)

	up, err := a.client.Upload(ctx, out.File, mediaKind(kind))
	if err != nil {
		return nil, fmt.Errorf("whatsapp: uploading the file: %w", err)
	}

	length := proto.Uint64(up.FileLength)

	switch kind {
	case "image":
		return &waE2E.Message{ImageMessage: &waE2E.ImageMessage{
			URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath), MediaKey: up.MediaKey,
			FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: length,
			Mimetype: proto.String(mimeType), Caption: proto.String(out.Text), ContextInfo: quote,
		}}, nil
	case "video":
		return &waE2E.Message{VideoMessage: &waE2E.VideoMessage{
			URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath), MediaKey: up.MediaKey,
			FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: length,
			Mimetype: proto.String(mimeType), Caption: proto.String(out.Text), ContextInfo: quote,
		}}, nil
	case "audio":
		return &waE2E.Message{AudioMessage: &waE2E.AudioMessage{
			URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath), MediaKey: up.MediaKey,
			FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: length,
			Mimetype: proto.String(mimeType), ContextInfo: quote,
		}}, nil
	}

	return &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{
		URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath), MediaKey: up.MediaKey,
		FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: length,
		Mimetype: proto.String(mimeType), FileName: proto.String(out.FileName), Title: proto.String(out.FileName),
		Caption: proto.String(out.Text), ContextInfo: quote,
	}}, nil
}

// keepSent keeps the file sent beside the others, so that it is read without downloading it back.
func (a *account) keepSent(ctx context.Context, chat types.JID, id string, out messaging.Outgoing) {
	key := a.mirrorKey()
	ext := extension(mediaRow{fileName: out.FileName, mime: sendMIME(out.FileName, out.File)})
	path := filepath.Join(a.mediaDir, unsafeName.ReplaceAllString(key, "_"),
		unsafeName.ReplaceAllString(chat.String(), "_"), unsafeName.ReplaceAllString(id, "_")+ext)

	if err := writePrivate(path, out.File); err != nil {
		return
	}

	_ = a.mirror.keptMedia(ctx, key, chat.String(), id, path, "")
}

// sendMIME is a file's type: by its name's extension, else by its first bytes (wacli detectSendFileMIME).
func sendMIME(name string, data []byte) string {
	if t := mime.TypeByExtension(strings.ToLower(filepath.Ext(name))); t != "" {
		return t
	}

	return http.DetectContentType(data[:min(len(data), 512)])
}

// sendKind is how a file is sent: an image, a video or an audio by its type, any other as a document (wacli
// resolveSendMediaType).
func sendKind(mimeType string) string {
	switch {
	case strings.HasPrefix(mimeType, "image/"):
		return "image"
	case strings.HasPrefix(mimeType, "video/"):
		return "video"
	case strings.HasPrefix(mimeType, "audio/"):
		return "audio"
	}

	return "document"
}
