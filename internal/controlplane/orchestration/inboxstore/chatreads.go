package inboxstore

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/storeerr"
)

// Which of the owner's chats the secretary reads (issue #1270; owner, 2026-11-27): a flag per chat of a Connection
// (integration:label), "the secretary reads it", which the triage's check reads before any model, as inbox-zero
// keeps an include or exclude item per account in its database (apps/web/prisma/schema.prisma, GroupItem.exclude).
// A chat the owner never set takes its kind's default, the same for every source: a direct chat and a group are read,
// a bot's or an official account's is not. A chat switched off is still kept and read on request; only the triage's
// check skips it.

// The kinds of chat, as the secretary sorts them.
const (
	ChatDirect = "direct"
	ChatGroup  = "group"
	ChatBot    = "bot"
)

// BotPatterns name a bot's or an official account's chat by its name or id, in any case: the dropChats defaults of
// the triage's check before issue #1270 (deploy/thu-ky/new-messages.mjs): Zalo's own accounts, official accounts,
// ads, newsletters and WhatsApp's broadcast lists.
var BotPatterns = []string{`^Zalo `, `\bOA\b`, `official account`, `quảng cáo`, `newsletter`, `@broadcast$`}

var botPatterns = func() []*regexp.Regexp {
	out := make([]*regexp.Regexp, 0, len(BotPatterns))
	for _, p := range BotPatterns {
		out = append(out, regexp.MustCompile(`(?i)`+p))
	}

	return out
}()

// ChatKind sorts a chat by the kind its service gives (Zalo's and WhatsApp's dm, group or broadcast; Lark's p2p,
// group or topic), its name and its id: a bot's or an official account's by BotPatterns, a broadcast list as one.
func ChatKind(serviceKind, name, id string) string {
	if serviceKind == "broadcast" {
		return ChatBot
	}

	for _, p := range botPatterns {
		if p.MatchString(name) || p.MatchString(id) {
			return ChatBot
		}
	}

	switch serviceKind {
	case "group", "topic":
		return ChatGroup
	default:
		return ChatDirect
	}
}

// DefaultReads says whether the secretary reads a chat of that kind the owner never set: direct chats and groups
// yes, bots and official accounts no (owner, 2026-11-27: "the same for every source, I configure it later").
func DefaultReads(kind string) bool { return kind != ChatBot }

// ChatRead is the owner's choice for one chat of a Connection, and when he made it.
type ChatRead struct {
	Integration string
	Label       string
	Chat        string
	Reads       bool
	At          time.Time
}

func chatReadOf(r store.InboxChatRead) ChatRead {
	return ChatRead{
		Integration: r.Integration, Label: r.Label, Chat: r.ChatID, Reads: r.Reads == 1, At: time.UnixMilli(r.SetAt).UTC(),
	}
}

func validChatRead(integration, label, chat string) error {
	for _, f := range []struct {
		name, value string
		most        int
	}{{"integration", integration, 40}, {"label", label, 100}, {"chat", chat, 200}} {
		if strings.TrimSpace(f.value) == "" || utf8.RuneCountInString(f.value) > f.most {
			return fmt.Errorf("%w: a chat's %s is 1 to %d characters", storeerr.ErrInvalid, f.name, f.most)
		}
	}

	return nil
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}

	return 0
}

// SetChatRead records the owner's choice for a chat, replacing the one he made before.
func (s *Store) SetChatRead(ctx context.Context, integration, label, chat string, reads bool) (ChatRead, error) {
	if err := validChatRead(integration, label, chat); err != nil {
		return ChatRead{}, err
	}

	var out ChatRead

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		row, err := q.SetInboxChatRead(ctx, store.SetInboxChatReadParams{
			Integration: integration, Label: label, ChatID: chat, Reads: boolInt(reads), Now: now,
		})
		out = chatReadOf(row)

		return err
	})
	if err != nil {
		return ChatRead{}, fmt.Errorf("setting whether the secretary reads %s:%s %s: %w", integration, label, chat, err)
	}

	return out, nil
}

// AddChatRead records the owner's choice for a chat he has not set yet, and says whether it did: a choice he made
// already stays, so that a move run again changes nothing.
func (s *Store) AddChatRead(ctx context.Context, integration, label, chat string, reads bool) (bool, error) {
	if err := validChatRead(integration, label, chat); err != nil {
		return false, err
	}

	var added bool

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		n, err := q.AddInboxChatRead(ctx, store.AddInboxChatReadParams{
			Integration: integration, Label: label, ChatID: chat, Reads: boolInt(reads), Now: now,
		})
		added = n == 1

		return err
	})
	if err != nil {
		return false, fmt.Errorf("setting whether the secretary reads %s:%s %s: %w", integration, label, chat, err)
	}

	return added, nil
}

// ChatReads lists the owner's choices: of one Connection when integration is set, else every one.
func (s *Store) ChatReads(ctx context.Context, integration, label string) ([]ChatRead, error) {
	rows, err := store.New(s.db).ListInboxChatReads(ctx, store.ListInboxChatReadsParams{
		Integration: integration, Label: label,
	})
	if err != nil {
		return nil, fmt.Errorf("listing which chats the secretary reads: %w", err)
	}

	out := make([]ChatRead, 0, len(rows))
	for _, r := range rows {
		out = append(out, chatReadOf(r))
	}

	return out, nil
}
