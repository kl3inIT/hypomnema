package telegram

// A Telegram message as kept: what whatsapp.db keeps of one (whatsapp/convert.go), read from gotd's tg.Message.

import (
	"strconv"
	"time"

	"github.com/gotd/td/tg"
)

// The media types, as messaging.Message.MediaType.
const (
	mediaImage    = "image"
	mediaVideo    = "video"
	mediaAudio    = "audio"
	mediaDocument = "document"
	mediaSticker  = "sticker"
)

// convert reads a message of the account whose user id is self. ok is false for what is not a message people wrote:
// an empty one, or a service message (a member joined, a title changed).
func convert(self int64, mc tg.MessageClass) (kept, bool) {
	m, ok := mc.(*tg.Message)
	if !ok || m.PeerID == nil {
		return kept{}, false
	}

	k := kept{
		chat:   peerID(m.PeerID),
		id:     m.ID,
		at:     time.Unix(int64(m.Date), 0).UTC(),
		fromMe: m.Out,
		text:   m.Message,
		sender: senderOf(self, m),
	}

	if _, ok := m.GetEditDate(); ok && !m.EditHide {
		k.edited = true
	}

	if reply, ok := m.ReplyTo.(*tg.MessageReplyHeader); ok {
		k.quotedID = quotedID(reply)
	}

	if media, ok := m.GetMedia(); ok {
		k.mediaType, k.fileName, k.mime = mediaOf(media)
		if k.mediaType != "" {
			// Telegram's text of a message with a file is the file's caption.
			k.caption, k.text = m.Message, ""
		}
	}

	return k, k.chat != ""
}

// senderOf is who sent a message: the peer it is from when Telegram says (a member of a group, an anonymous admin as
// the group), else the account itself for one it sent, else the chat (a direct chat's other party, a channel's post).
func senderOf(self int64, m *tg.Message) string {
	if from, ok := m.GetFromID(); ok {
		return peerID(from)
	}

	if m.Out {
		return userID(self)
	}

	return peerID(m.PeerID)
}

// quotedID is the message a reply quotes, by mautrix's rule (pkg/connector/tomatrix.go:305): a header is a reply
// unless it only says the forum topic the message is in (ForumTopic set without reply_to_top_id: reply_to_msg_id is
// then the topic's first message), and unless it names the topic's first message itself (reply_to_top_id equal to
// reply_to_msg_id). A reply to a message of another chat (reply_to_peer_id) is left out: mautrix names it by that
// chat (tomatrix.go:307-308), and messaging.Message.QuotedID can name a message of its own chat only.
func quotedID(h *tg.MessageReplyHeader) string {
	id, ok := h.GetReplyToMsgID()
	if !ok || id == 0 {
		return ""
	}

	top, _ := h.GetReplyToTopID()
	if (top == 0 && h.ForumTopic) || top == id {
		return ""
	}

	if _, other := h.GetReplyToPeerID(); other {
		return ""
	}

	return strconv.Itoa(id)
}

// mediaOf is a message's file: a photo is an image; a document is a sticker, a video (a round video and a GIF too),
// an audio (a voice note too) or a file, by its attributes. Anything else (a link preview, a place, a poll) is not a
// file and gives no type.
func mediaOf(media tg.MessageMediaClass) (kind, fileName, mime string) {
	switch media := media.(type) {
	case *tg.MessageMediaPhoto:
		if _, ok := media.GetPhoto(); ok {
			return mediaImage, "", "image/jpeg"
		}
	case *tg.MessageMediaDocument:
		doc, ok := media.Document.(*tg.Document)
		if !ok {
			return "", "", ""
		}

		return documentKind(doc), documentName(doc), doc.MimeType
	}

	return "", "", ""
}

func documentKind(doc *tg.Document) string {
	kind := mediaDocument

	for _, attr := range doc.Attributes {
		switch attr.(type) {
		case *tg.DocumentAttributeSticker:
			return mediaSticker
		case *tg.DocumentAttributeVideo, *tg.DocumentAttributeAnimated:
			kind = mediaVideo
		case *tg.DocumentAttributeAudio:
			kind = mediaAudio
		}
	}

	return kind
}

func documentName(doc *tg.Document) string {
	for _, attr := range doc.Attributes {
		if name, ok := attr.(*tg.DocumentAttributeFilename); ok {
			return name.FileName
		}
	}

	return ""
}
