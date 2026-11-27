package zalo

// Sending as the owner (issue #680, step 3 of #613): the bridge's send, with zca-js's sendMessage. Whether it may
// be sent is decided before, by the owner's approval (agentapi, zalo.send) or his own Send in the Inbox, which
// alone tags members (issue #1200). A file is written beside zalo.db for the call, as zca-js sends files by path
// (the Hermes Zalo plugin sends them so), and removed after. What is sent is kept as the owner's, as WhatsApp's
// keepSent keeps it; the listener, which hears the owner's own messages (selfListen), brings it back too, and it is
// kept once.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/kl3inIT/hypomnema/internal/controlplane/messaging"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// sendWait bounds a send; a file's upload takes longest.
const sendWait = 2 * time.Minute

// quote is the kept message a reply answers, as zca-js's SendMessageQuote takes it.
type quote struct {
	ID    string `json:"id"`
	CliID string `json:"cliId"`
	From  string `json:"from"`
	At    int64  `json:"at"`
	Kind  string `json:"kind"`
	Text  string `json:"text"`
}

// mention is a member a group's message tags, as zca-js's sendMessage takes it (src/apis/sendMessage.ts, Mention):
// where the text writes "@" and his name and how long that is, counted as JavaScript counts a string, in UTF-16
// code units (issue #1200).
type mention struct {
	Pos int    `json:"pos"`
	UID string `json:"uid"`
	Len int    `json:"len"`
}

// mentions are a text's tags as zca-js takes them. A tag the text does not write is refused rather than sent on the
// wrong words.
func mentions(text string, tags []messaging.Tag) ([]mention, error) {
	out := make([]mention, 0, len(tags))

	for _, t := range tags {
		end := t.At + t.Len
		if t.ID == "" || t.At < 0 || t.Len < 2 || end > len(text) || text[t.At] != '@' ||
			!utf8.ValidString(text[t.At:end]) || (end < len(text) && !utf8.RuneStart(text[end])) {
			return nil, fmt.Errorf("zalo: the text does not write the tag of %s where it says", t.ID)
		}

		out = append(out, mention{Pos: utf16Len(text[:t.At]), UID: t.ID, Len: utf16Len(text[t.At:end])})
	}

	return out, nil
}

// utf16Len is how many UTF-16 code units a string takes.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}

	return n
}

// Send sends a message, or a file with its caption, in a chat as the owner, and keeps it.
func (a *account) Send(ctx context.Context, out messaging.Outgoing) (messaging.Message, error) {
	b := a.current()
	if b == nil {
		return messaging.Message{}, errNotConnected
	}

	kind := a.svc.mirror.chatKind(ctx, a.uid, out.Chat)
	if kind == "" {
		return messaging.Message{}, fmt.Errorf("%w: no Zalo chat %s is kept; list them first",
			orchestration.ErrNotFound, out.Chat)
	}

	params := map[string]any{"chat": out.Chat, "group": kind == "group", "text": out.Text}

	if kind == "group" && len(out.Tags) > 0 {
		tagged, err := mentions(out.Text, out.Tags)
		if err != nil {
			return messaging.Message{}, err
		}

		params["mentions"] = tagged
	}

	if out.ReplyTo != "" {
		q, err := a.svc.mirror.quoteOf(ctx, a.uid, out.Chat, out.ReplyTo)
		if err != nil {
			return messaging.Message{}, err
		}

		params["quote"] = q
	}

	if len(out.File) > 0 {
		path, err := a.svc.outgoing(out.FileName, out.File)
		if err != nil {
			return messaging.Message{}, err
		}
		defer os.RemoveAll(filepath.Dir(path)) //nolint:errcheck // A file left behind is removed with the next.

		params["path"] = path
	}

	ctx, cancel := context.WithTimeout(ctx, sendWait)
	defer cancel()

	// The file and the text are two messages, each with its id (bridge.mjs, send).
	var sent struct {
		FileID string `json:"fileId"`
		TextID string `json:"textId"`
		Error  string `json:"error"`
	}
	if err := b.call(ctx, "send", params, &sent); err != nil {
		return messaging.Message{}, fmt.Errorf("zalo: sending in %s: %w", out.Chat, err)
	}

	now := time.Now().UnixMilli()
	group := kind == "group"

	if sent.FileID != "" {
		a.keepSent(ctx, incoming{ID: sent.FileID, Chat: out.Chat, Group: group, From: a.uid, FromMe: true, At: now,
			Kind: "share.file", Media: &mediaOf{Type: "document", FileName: out.FileName, Size: int64(len(out.File))}})
	}

	if sent.TextID != "" {
		a.keepSent(ctx, incoming{ID: sent.TextID, Chat: out.Chat, Group: group, From: a.uid, FromMe: true, At: now,
			Kind: "webchat", Text: out.Text, QuoteID: out.ReplyTo})
	}

	if sent.Error != "" {
		// The file went; the text did not. Said so, that the file is not sent again.
		return messaging.Message{}, fmt.Errorf("zalo: the file was sent in %s (message %s), but not the text: %s",
			out.Chat, sent.FileID, sent.Error)
	}

	id := sent.TextID
	if id == "" {
		id = sent.FileID
	}

	if id == "" {
		return messaging.Message{}, errors.New("zalo: Zalo took the message but gave no id")
	}

	return messaging.Message{
		ID: id, Chat: out.Chat, At: time.UnixMilli(now).UTC(), FromMe: true, Text: out.Text,
		QuotedID: out.ReplyTo, FileName: out.FileName,
	}, nil
}

// keepSent keeps a message sent as the owner's. The listener brings it back with Zalo's own fields, which fill in
// what this one lacks (mirror.keep).
func (a *account) keepSent(ctx context.Context, m incoming) {
	if _, err := a.keepMessage(ctx, m); err != nil {
		a.svc.log.WarnContext(ctx, "zalo: a message sent could not be kept")
	}
}

// outgoing writes a file to send in a folder of its own beside zalo.db, private, under its own name, which Zalo
// shows the chat.
func (s *Service) outgoing(name string, data []byte) (string, error) {
	base := filepath.Join(filepath.Dir(s.mediaDir), "zalo-outgoing")
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", fmt.Errorf("zalo: making the outgoing folder: %w", err)
	}

	dir, err := os.MkdirTemp(base, "send-")
	if err != nil {
		return "", fmt.Errorf("zalo: making the outgoing folder: %w", err)
	}

	name = unsafeName.ReplaceAllString(filepath.Base(name), "_")
	if name == "" || name == "." || filepath.Ext(name) == "" {
		// zca-js tells a file's kind by its extension.
		name += ".bin"
	}

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		_ = os.RemoveAll(dir)

		return "", fmt.Errorf("zalo: writing the file to send: %w", err)
	}

	return path, nil
}
