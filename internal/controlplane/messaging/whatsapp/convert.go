package whatsapp

// What a WhatsApp message holds, as it is kept (issue #631). Adapted from wacli's parser
// (github.com/openclaw/wacli, internal/wa/messages.go: ParseLiveMessage, ParseHistoryMessage, extractWAProto,
// extractProtocolMutation, applyDeviceSentDestination, extractPlainText; MIT License, Copyright (c) 2026 Peter
// Steinberger), to the part kept here: text, media, replies, edits and revokes. Reactions, polls, locations,
// contacts and call logs are left out.

import (
	"math"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// parsed is one message as kept, or the change it makes to one kept before.
type parsed struct {
	chat     types.JID
	id       string
	sender   string
	pushName string
	at       time.Time
	fromMe   bool

	text      string
	quotedID  string
	mediaType string
	caption   string
	fileName  string
	mime      string
	// What downloading the media needs (issue #634), as wacli keeps it.
	directPath  string
	mediaKey    []byte
	fileSHA256  []byte
	fileEncHash []byte
	fileLength  uint64

	// edited replaces the text of the message id names; revoked marks it.
	edited  bool
	revoked bool
}

// hasContent says whether there is anything to keep or change.
func (p *parsed) hasContent() bool {
	return p.text != "" || p.mediaType != "" || p.revoked
}

// parseLive reads a message as it arrives (wacli ParseLiveMessage).
func parseLive(evt *events.Message) parsed {
	p := parsed{
		chat: evt.Info.Chat, id: evt.Info.ID, sender: evt.Info.Sender.String(), pushName: evt.Info.PushName,
		at: evt.Info.Timestamp.UTC(), fromMe: evt.Info.IsFromMe, edited: evt.IsEdit,
	}
	if meta := evt.Info.DeviceSentMeta; meta != nil {
		p.sentTo(meta.DestinationJID)
	}

	p.read(evt.Message)

	return p
}

// parseHistory reads a message of a history sync (wacli ParseHistoryMessage): whatsmeow does not unwrap those.
func parseHistory(chat types.JID, hist *waWeb.WebMessageInfo) parsed {
	p := parsed{
		chat: chat, id: hist.GetKey().GetID(), at: unixTime(hist.GetMessageTimestamp()),
		fromMe: hist.GetKey().GetFromMe(), pushName: hist.GetPushName(),
	}

	p.sender = strings.TrimSpace(hist.GetParticipant())
	if p.sender == "" {
		p.sender = strings.TrimSpace(hist.GetKey().GetParticipant())
	}

	if p.sender == "" {
		p.sender = strings.TrimSpace(hist.GetKey().GetRemoteJID())
	}

	if raw := hist.GetMessage(); raw != nil {
		evt := (&events.Message{RawMessage: raw}).UnwrapRaw()
		if evt.Info.DeviceSentMeta != nil {
			p.sentTo(evt.Info.DeviceSentMeta.DestinationJID)
		}

		p.edited = p.edited || evt.IsEdit
		p.read(evt.Message)
	}

	return p
}

// sentTo is a message the owner sent from another of his devices: its chat is where it went.
func (p *parsed) sentTo(destination string) {
	p.fromMe = true
	if chat, err := types.ParseJID(strings.TrimSpace(destination)); err == nil && !chat.IsEmpty() {
		p.chat = chat
	}
}

// read takes the content out of a message, through its wrappers (wacli extractWAProto).
func (p *parsed) read(m *waE2E.Message) {
	if m == nil {
		return
	}

	if sent := m.GetDeviceSentMessage(); sent.GetMessage() != nil {
		p.sentTo(sent.GetDestinationJID())
		p.read(sent.GetMessage())

		return
	}

	if edited := m.GetEditedMessage().GetMessage(); edited != nil {
		p.edited = true
		p.read(edited)

		return
	}

	if protocol := m.GetProtocolMessage(); protocol != nil {
		switch protocol.GetType() {
		case waE2E.ProtocolMessage_MESSAGE_EDIT:
			p.key(protocol.GetKey())
			p.edited = true
			p.read(protocol.GetEditedMessage())
		case waE2E.ProtocolMessage_REVOKE:
			if protocol.GetKey() != nil {
				p.key(protocol.GetKey())
				p.revoked = true
			}
		default:
			// Other protocol messages (history notices, app state keys, ephemeral settings) carry nothing kept.
		}

		return
	}

	switch {
	case m.GetConversation() != "":
		p.text = m.GetConversation()
	case m.GetExtendedTextMessage() != nil:
		p.text = m.GetExtendedTextMessage().GetText()
	}

	p.media(m)

	if ctx := contextOf(m); ctx != nil {
		p.quotedID = strings.TrimSpace(ctx.GetStanzaID())
	}
}

// key is the message an edit or a revoke changes (wacli applyProtocolKey).
func (p *parsed) key(k *waCommon.MessageKey) {
	if id := strings.TrimSpace(k.GetID()); id != "" {
		p.id = id
	}

	if remote, err := types.ParseJID(strings.TrimSpace(k.GetRemoteJID())); err == nil && !remote.IsEmpty() {
		p.chat = remote
	}
}

// downloadable is a media message's download keys (whatsmeow.DownloadableMessage).
type downloadable interface {
	GetDirectPath() string
	GetMediaKey() []byte
	GetFileSHA256() []byte
	GetFileEncSHA256() []byte
	GetFileLength() uint64
}

func (p *parsed) keys(d downloadable) {
	p.directPath, p.mediaKey, p.fileSHA256 = d.GetDirectPath(), d.GetMediaKey(), d.GetFileSHA256()
	p.fileEncHash, p.fileLength = d.GetFileEncSHA256(), d.GetFileLength()
}

func (p *parsed) media(m *waE2E.Message) {
	defer func() {
		switch {
		case m.GetImageMessage() != nil:
			p.keys(m.GetImageMessage())
		case m.GetVideoMessage() != nil:
			p.keys(m.GetVideoMessage())
		case m.GetAudioMessage() != nil:
			p.keys(m.GetAudioMessage())
		case m.GetDocumentMessage() != nil:
			p.keys(m.GetDocumentMessage())
		case m.GetStickerMessage() != nil:
			p.keys(m.GetStickerMessage())
		}
	}()

	switch {
	case m.GetImageMessage() != nil:
		img := m.GetImageMessage()
		p.mediaType, p.caption, p.mime = "image", img.GetCaption(), img.GetMimetype()
	case m.GetVideoMessage() != nil:
		v := m.GetVideoMessage()
		p.mediaType, p.caption, p.mime = "video", v.GetCaption(), v.GetMimetype()
	case m.GetAudioMessage() != nil:
		p.mediaType, p.mime = "audio", m.GetAudioMessage().GetMimetype()
	case m.GetDocumentMessage() != nil:
		d := m.GetDocumentMessage()
		p.mediaType, p.caption, p.fileName, p.mime = "document", d.GetCaption(), d.GetFileName(), d.GetMimetype()
	case m.GetStickerMessage() != nil:
		p.mediaType, p.mime = "sticker", m.GetStickerMessage().GetMimetype()
	}
}

// contextOf is the context a message carries: what it replies to.
func contextOf(m *waE2E.Message) *waE2E.ContextInfo {
	switch {
	case m.GetExtendedTextMessage() != nil:
		return m.GetExtendedTextMessage().GetContextInfo()
	case m.GetImageMessage() != nil:
		return m.GetImageMessage().GetContextInfo()
	case m.GetVideoMessage() != nil:
		return m.GetVideoMessage().GetContextInfo()
	case m.GetAudioMessage() != nil:
		return m.GetAudioMessage().GetContextInfo()
	case m.GetDocumentMessage() != nil:
		return m.GetDocumentMessage().GetContextInfo()
	case m.GetStickerMessage() != nil:
		return m.GetStickerMessage().GetContextInfo()
	}

	return nil
}

// unixTime is a history message's time, in seconds since 1970; one past what a time can hold is the start.
func unixTime(secs uint64) time.Time {
	if secs > math.MaxInt64 {
		return time.Unix(0, 0).UTC()
	}

	return time.Unix(int64(secs), 0).UTC()
}
