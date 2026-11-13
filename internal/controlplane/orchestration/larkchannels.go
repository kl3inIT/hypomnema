package orchestration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
)

// Lark Channels (issues #136 and #169): a Channel is a door to one Bot, a Lark
// app and who may talk through it. This package keeps what each Channel is
// and who may talk through it; what talks to Lark is
// internal/controlplane/larkbot. An app's secret is in the vault; only its
// name is here.

// LarkChannelMode says who may talk through a Channel, after OpenClaw's
// dmPolicy for Feishu (closed by default, an allowlist, open).
type LarkChannelMode string

// The modes (owner, 2026-11-10).
const (
	// LarkChannelOwner: only the owner.
	LarkChannelOwner LarkChannelMode = "owner"
	// LarkChannelAllowlist: the owner and the people he added.
	LarkChannelAllowlist LarkChannelMode = "allowlist"
	// LarkChannelEveryone: every member of the app's Lark organization.
	LarkChannelEveryone LarkChannelMode = "everyone"
)

// ValidLarkChannelMode says whether m is a mode.
func ValidLarkChannelMode(m LarkChannelMode) bool {
	return m == LarkChannelOwner || m == LarkChannelAllowlist || m == LarkChannelEveryone
}

// MaxLarkChannelName is the most characters of a Channel's name: the vault
// secret of its app is named after it, and a secret's name is 63 at most.
const MaxLarkChannelName = 50

// ValidLarkChannelName says whether name can be a Channel's: as a Bot's,
// lowercase letters, digits and dashes, starting with a letter.
func ValidLarkChannelName(name string) bool {
	return len(name) <= MaxLarkChannelName && namePattern.MatchString(name)
}

// LarkChannel is a Channel as the owner set it up.
type LarkChannel struct {
	Name string
	// AppID is the Lark app's id; Secret names the vault secret with its
	// secret. One app is one Channel.
	AppID  string
	Secret string
	// Bot is the Bot a chat opened through the Channel talks with. Several
	// Channels may talk to one Bot (OpenClaw's bindings).
	Bot  string
	Mode LarkChannelMode
	// OwnerOpenID is the owner's id at Lark, in the app's own namespace.
	OwnerOpenID string
	// Enabled says the Channel answers. One that is off takes no message.
	Enabled bool
	// WebLink says a reply in a direct chat ends with a link to the chat in
	// the web client (issue #305); a group's reply never has one.
	WebLink   bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// LarkChannelPerson is someone the owner added, who may talk through a
// Channel when its mode is the allowlist. Lark gives a person another open_id
// in each app, so a person is added to each Channel apart.
type LarkChannelPerson struct {
	OpenID    string
	Email     string
	Name      string
	CreatedAt time.Time
}

// LarkChannelChat joins a person's direct chat with a Channel's app at Lark
// to a chat with a Bot.
type LarkChannelChat struct {
	Channel    string
	LarkChatID string
	OpenID     string
	ChatID     string
	// Bot is the Bot of the chat: the Channel's when the chat was opened.
	Bot string
	// SentSeq is the last message of the chat that was sent to Lark.
	SentSeq int64
	// Group says the Lark chat is a group, whose one chat with the Bot is the
	// whole group's (OpenClaw's Feishu groupSessionScope "group"); OpenID is
	// then empty.
	Group bool
}

func larkChannelFromRow(r store.LarkChannel) LarkChannel {
	return LarkChannel{
		Name: r.Name, AppID: r.AppID, Secret: r.Secret, Bot: r.Bot, Mode: LarkChannelMode(r.Mode),
		OwnerOpenID: r.OwnerOpenID, Enabled: r.Enabled != 0, WebLink: r.WebLink != 0,
		CreatedAt: millis(r.CreatedAt), UpdatedAt: millis(r.UpdatedAt),
	}
}

// SetLarkChannel records a Channel, or changes it. An app that is another
// Channel's is refused, as Hermes refuses two profiles on one app.
func (s *Service) SetLarkChannel(ctx context.Context, c LarkChannel) (LarkChannel, error) {
	switch {
	case !ValidLarkChannelName(c.Name):
		return LarkChannel{}, fmt.Errorf("%w: a channel's name is lowercase letters, digits and dashes, starting "+
			"with a letter, at most %d characters", ErrInvalid, MaxLarkChannelName)
	case c.AppID == "" || c.Secret == "":
		return LarkChannel{}, fmt.Errorf("%w: a Lark channel needs its app's id and secret", ErrInvalid)
	case c.Bot == "":
		return LarkChannel{}, fmt.Errorf("%w: a Lark channel names the bot it talks to", ErrInvalid)
	case !ValidLarkChannelMode(c.Mode):
		return LarkChannel{}, fmt.Errorf("%w: the mode is %s, %s or %s", ErrInvalid, LarkChannelOwner,
			LarkChannelAllowlist, LarkChannelEveryone)
	case !strings.HasPrefix(c.OwnerOpenID, "ou_"):
		return LarkChannel{}, fmt.Errorf("%w: the owner is named by his Lark open_id, which starts with ou_",
			ErrInvalid)
	}

	enabled, webLink := int64(0), int64(0)
	if c.Enabled {
		enabled = 1
	}

	if c.WebLink {
		webLink = 1
	}

	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		if _, err := q.GetBot(ctx, c.Bot); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: no bot is named %q", ErrInvalid, c.Bot)
			}

			return err
		}

		other, err := q.GetLarkChannelByApp(ctx, c.AppID)
		if err == nil && other.Name != c.Name {
			return fmt.Errorf("%w: the Lark app %s is the app of channel %s; one app is one channel", ErrConflict,
				c.AppID, other.Name)
		}

		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}

		return q.UpsertLarkChannel(ctx, store.UpsertLarkChannelParams{
			Name: c.Name, AppID: c.AppID, Secret: c.Secret, Bot: c.Bot, Mode: string(c.Mode),
			OwnerOpenID: c.OwnerOpenID, Enabled: enabled, WebLink: webLink, Now: now,
		})
	}); err != nil {
		return LarkChannel{}, fmt.Errorf("recording Lark channel %s: %w", c.Name, err)
	}

	return s.LarkChannel(ctx, c.Name)
}

// LarkChannel returns one Channel; ErrNotFound when the owner set none up
// under that name.
func (s *Service) LarkChannel(ctx context.Context, name string) (LarkChannel, error) {
	row, err := s.read().GetLarkChannel(ctx, name)
	if err != nil {
		return LarkChannel{}, notFound(err, "Lark channel", name)
	}

	return larkChannelFromRow(row), nil
}

// LarkChannels returns the Channels, oldest first.
func (s *Service) LarkChannels(ctx context.Context) ([]LarkChannel, error) {
	rows, err := s.read().ListLarkChannels(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing the Lark channels: %w", err)
	}

	out := make([]LarkChannel, 0, len(rows))
	for _, r := range rows {
		out = append(out, larkChannelFromRow(r))
	}

	return out, nil
}

// RemoveLarkChannel forgets a Channel with its people, the Lark chats it
// answered in and its question cards. The chats with the Bot stay, on the
// web.
func (s *Service) RemoveLarkChannel(ctx context.Context, name string) error {
	var removed int64

	if err := s.write(ctx, func(q *store.Queries, _ int64) error {
		var err error

		removed, err = q.DeleteLarkChannel(ctx, name)

		return err
	}); err != nil {
		return fmt.Errorf("forgetting Lark channel %s: %w", name, err)
	}

	if removed == 0 {
		return fmt.Errorf("%w: Lark channel %q", ErrNotFound, name)
	}

	return nil
}

// AddLarkChannelPerson adds someone who may talk through a Channel, or
// changes the email and name kept for them.
func (s *Service) AddLarkChannelPerson(ctx context.Context, channel string, p LarkChannelPerson,
) (LarkChannelPerson, error) {
	if !strings.HasPrefix(p.OpenID, "ou_") {
		return LarkChannelPerson{}, fmt.Errorf("%w: a person is named by a Lark open_id, which starts with ou_",
			ErrInvalid)
	}

	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		if _, err := q.GetLarkChannel(ctx, channel); err != nil {
			return notFound(err, "Lark channel", channel)
		}

		return q.UpsertLarkChannelPerson(ctx, store.UpsertLarkChannelPersonParams{
			Channel: channel, OpenID: p.OpenID, Email: p.Email, Name: p.Name, Now: now,
		})
	}); err != nil {
		return LarkChannelPerson{}, fmt.Errorf("adding %s to Lark channel %s: %w", p.OpenID, channel, err)
	}

	return s.LarkChannelPerson(ctx, channel, p.OpenID)
}

// LarkChannelPerson returns one person added to a Channel; ErrNotFound when
// they were not added to it.
func (s *Service) LarkChannelPerson(ctx context.Context, channel, openID string) (LarkChannelPerson, error) {
	row, err := s.read().GetLarkChannelPerson(ctx, store.GetLarkChannelPersonParams{Channel: channel, OpenID: openID})
	if err != nil {
		return LarkChannelPerson{}, notFound(err, "person of Lark channel "+channel, openID)
	}

	return larkChannelPersonFromRow(row), nil
}

// LarkChannelPeople returns the people the owner added to a Channel, oldest
// first.
func (s *Service) LarkChannelPeople(ctx context.Context, channel string) ([]LarkChannelPerson, error) {
	rows, err := s.read().ListLarkChannelPeople(ctx, channel)
	if err != nil {
		return nil, fmt.Errorf("listing the people of Lark channel %s: %w", channel, err)
	}

	out := make([]LarkChannelPerson, 0, len(rows))
	for _, r := range rows {
		out = append(out, larkChannelPersonFromRow(r))
	}

	return out, nil
}

// RemoveLarkChannelPerson takes a person off a Channel's list.
func (s *Service) RemoveLarkChannelPerson(ctx context.Context, channel, openID string) error {
	var removed int64

	if err := s.write(ctx, func(q *store.Queries, _ int64) error {
		var err error

		removed, err = q.DeleteLarkChannelPerson(ctx, store.DeleteLarkChannelPersonParams{
			Channel: channel, OpenID: openID,
		})

		return err
	}); err != nil {
		return fmt.Errorf("removing %s from Lark channel %s: %w", openID, channel, err)
	}

	if removed == 0 {
		return fmt.Errorf("%w: person %q of Lark channel %s", ErrNotFound, openID, channel)
	}

	return nil
}

func larkChannelPersonFromRow(r store.LarkChannelPerson) LarkChannelPerson {
	return LarkChannelPerson{OpenID: r.OpenID, Email: r.Email, Name: r.Name, CreatedAt: millis(r.CreatedAt)}
}

// TakeLarkMessage records that a Channel took a Lark message and says
// whether it is the first time. Lark delivers an event again when it was not
// acknowledged within three seconds, so a second delivery is dropped here.
func (s *Service) TakeLarkMessage(ctx context.Context, channel, messageID string) (first bool, err error) {
	var inserted int64

	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		var err error

		inserted, err = q.InsertLarkChannelMessage(ctx, store.InsertLarkChannelMessageParams{
			MessageID: messageID, Channel: channel, Now: now,
		})

		return err
	}); err != nil {
		return false, fmt.Errorf("taking Lark message %s: %w", messageID, err)
	}

	return inserted == 1, nil
}

// ForgetLarkMessagesBefore drops the record of messages taken before a time:
// Lark's last delivery of an event comes about seven hours after the first.
func (s *Service) ForgetLarkMessagesBefore(ctx context.Context, before time.Time) error {
	if err := s.write(ctx, func(q *store.Queries, _ int64) error {
		return q.DeleteLarkChannelMessagesBefore(ctx, before.UnixMilli())
	}); err != nil {
		return fmt.Errorf("forgetting old Lark messages: %w", err)
	}

	return nil
}

// larkChannelChat is a Lark chat's row with the Bot of its chat.
func (s *Service) larkChannelChat(ctx context.Context, r store.LarkChannelChat) (LarkChannelChat, error) {
	chat, err := s.read().GetChat(ctx, r.ChatID)
	if err != nil {
		return LarkChannelChat{}, fmt.Errorf("reading the chat of Lark chat %s: %w", r.LarkChatID, err)
	}

	return LarkChannelChat{
		Channel: r.Channel, LarkChatID: r.LarkChatID, OpenID: r.OpenID, ChatID: r.ChatID, Bot: chat.Bot,
		SentSeq: r.SentSeq, Group: chat.LarkGroup != 0,
	}, nil
}

// LarkChannelChatFor returns the chat that a person's direct chat with a
// Channel's app is, starting one with the Channel's Bot the first time.
func (s *Service) LarkChannelChatFor(ctx context.Context, channel, larkChatID, openID, title string,
) (LarkChannelChat, error) {
	return s.larkChatFor(ctx, channel, larkChatID, openID, title, false)
}

// LarkGroupChatFor returns the chat that a Lark group is, the whole group's
// one, starting one with the Channel's Bot the first time.
func (s *Service) LarkGroupChatFor(ctx context.Context, channel, larkChatID, title string) (LarkChannelChat, error) {
	return s.larkChatFor(ctx, channel, larkChatID, "", title, true)
}

func (s *Service) larkChatFor(ctx context.Context, channel, larkChatID, openID, title string, group bool,
) (LarkChannelChat, error) {
	row, err := s.read().GetLarkChannelChat(ctx, store.GetLarkChannelChatParams{
		Channel: channel, LarkChatID: larkChatID,
	})
	if err == nil {
		return s.larkChannelChat(ctx, row)
	}

	if !errors.Is(err, sql.ErrNoRows) {
		return LarkChannelChat{}, fmt.Errorf("reading Lark chat %s: %w", larkChatID, err)
	}

	ch, err := s.LarkChannel(ctx, channel)
	if err != nil {
		return LarkChannelChat{}, err
	}

	chat, err := s.CreateChat(ctx, ch.Bot, title)
	if err != nil {
		return LarkChannelChat{}, err
	}

	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		// A group's chat is marked on the chat itself, so it stays one once the group has started another.
		if group {
			if err := q.MarkLarkGroupChat(ctx, chat.ID); err != nil {
				return err
			}
		}

		return q.InsertLarkChannelChat(ctx, store.InsertLarkChannelChatParams{
			LarkChatID: larkChatID, Channel: channel, OpenID: openID, ChatID: chat.ID, Now: now,
		})
	}); err != nil {
		return LarkChannelChat{}, fmt.Errorf("recording Lark chat %s: %w", larkChatID, err)
	}

	return LarkChannelChat{
		Channel: channel, LarkChatID: larkChatID, OpenID: openID, ChatID: chat.ID, Bot: chat.Bot, Group: group,
	}, nil
}

// ResetLarkChannelChat gives a Lark chat a new chat with its Channel's Bot:
// the person's /new, or a group's conversation that was idle too long. The
// chat it had stays, on the web. ErrNotFound when the Lark chat has none yet.
func (s *Service) ResetLarkChannelChat(ctx context.Context, channel, larkChatID, title string,
) (LarkChannelChat, error) {
	row, err := s.read().GetLarkChannelChat(ctx, store.GetLarkChannelChatParams{
		Channel: channel, LarkChatID: larkChatID,
	})
	if err != nil {
		return LarkChannelChat{}, notFound(err, "Lark chat", larkChatID)
	}

	ch, err := s.LarkChannel(ctx, channel)
	if err != nil {
		return LarkChannelChat{}, err
	}

	old, err := s.read().GetChat(ctx, row.ChatID)
	if err != nil {
		return LarkChannelChat{}, fmt.Errorf("reading the chat of Lark chat %s: %w", larkChatID, err)
	}

	chat, err := s.CreateChat(ctx, ch.Bot, title)
	if err != nil {
		return LarkChannelChat{}, err
	}

	group := old.LarkGroup != 0

	if err := s.write(ctx, func(q *store.Queries, _ int64) error {
		if group {
			if err := q.MarkLarkGroupChat(ctx, chat.ID); err != nil {
				return err
			}
		}

		_, err := q.ResetLarkChannelChat(ctx, store.ResetLarkChannelChatParams{
			Channel: channel, LarkChatID: larkChatID, ChatID: chat.ID,
		})

		return err
	}); err != nil {
		return LarkChannelChat{}, fmt.Errorf("starting Lark chat %s over: %w", larkChatID, err)
	}

	return LarkChannelChat{
		Channel: channel, LarkChatID: larkChatID, OpenID: row.OpenID, ChatID: chat.ID, Bot: chat.Bot,
		Group: group,
	}, nil
}

// LarkChannelChats returns the Lark chats a Channel answers in, oldest
// first.
func (s *Service) LarkChannelChats(ctx context.Context, channel string) ([]LarkChannelChat, error) {
	rows, err := s.read().ListLarkChannelChats(ctx, channel)
	if err != nil {
		return nil, fmt.Errorf("listing the chats of Lark channel %s: %w", channel, err)
	}

	out := make([]LarkChannelChat, 0, len(rows))

	for _, r := range rows {
		chat, err := s.larkChannelChat(ctx, r)
		if err != nil {
			return nil, err
		}

		out = append(out, chat)
	}

	return out, nil
}

// MarkLarkChannelSent records the last message of a chat that was sent to
// Lark. It never moves back.
func (s *Service) MarkLarkChannelSent(ctx context.Context, larkChatID string, seq int64) error {
	if err := s.write(ctx, func(q *store.Queries, _ int64) error {
		return q.SetLarkChannelChatSent(ctx, store.SetLarkChannelChatSentParams{LarkChatID: larkChatID, SentSeq: seq})
	}); err != nil {
		return fmt.Errorf("recording what was sent to Lark chat %s: %w", larkChatID, err)
	}

	return nil
}

// RenameLarkChannelChat records the Lark chat id of a person's direct chat
// that was kept under their open_id until Lark named the chat (issue #312: a
// press of the bot's menu names no chat, and the reply sent to the person
// does). ErrNotFound when no chat of the Channel has the old id; ErrConflict
// when one has the new id already.
func (s *Service) RenameLarkChannelChat(ctx context.Context, channel, oldID, newID string) error {
	if _, err := s.read().GetLarkChannelChat(ctx, store.GetLarkChannelChatParams{
		Channel: channel, LarkChatID: newID,
	}); err == nil {
		return fmt.Errorf("%w: Lark chat %s is a chat of channel %s already", ErrConflict, newID, channel)
	}

	var renamed int64

	if err := s.write(ctx, func(q *store.Queries, _ int64) error {
		var err error

		renamed, err = q.RenameLarkChannelChat(ctx, store.RenameLarkChannelChatParams{
			NewLarkChatID: newID, Channel: channel, LarkChatID: oldID,
		})

		return err
	}); err != nil {
		return fmt.Errorf("renaming Lark chat %s: %w", oldID, err)
	}

	if renamed == 0 {
		return fmt.Errorf("%w: channel %s has no Lark chat %s", ErrNotFound, channel, oldID)
	}

	return nil
}

// LarkChannelForRun returns the Channel a question of a Run goes out through:
// the Channel whose Lark chat is the chat that started the Run, as Hermes
// sends an approval to the conversation the work came from; for a Run that
// started elsewhere, the oldest Channel that is on. ErrNotFound when no
// Channel is on.
func (s *Service) LarkChannelForRun(ctx context.Context, runID string) (string, error) {
	q := s.read()

	channel, err := q.LarkChannelOfRun(ctx, runID)
	if err == nil {
		return channel, nil
	}

	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("reading the Lark channel of run %s: %w", runID, err)
	}

	channel, err = q.FirstEnabledLarkChannel(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%w: no Lark channel is on", ErrNotFound)
	}

	if err != nil {
		return "", fmt.Errorf("reading the first Lark channel: %w", err)
	}

	return channel, nil
}

// LarkQuestionCard is the card at Lark that shows one of the owner's
// questions.
type LarkQuestionCard struct {
	QuestionID string
	// Channel is the Channel the card was sent through.
	Channel string
	CardID  string
	// MessageID is the Lark message the card is; a reply to it answers the
	// question in the owner's own words.
	MessageID string
	// Seq is the card's last update.
	Seq int
}

// RecordLarkQuestionCard records the card a question was sent as.
func (s *Service) RecordLarkQuestionCard(ctx context.Context, c LarkQuestionCard) error {
	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		return q.InsertLarkChannelQuestion(ctx, store.InsertLarkChannelQuestionParams{
			QuestionID: c.QuestionID, Channel: c.Channel, CardID: c.CardID, MessageID: c.MessageID, Now: now,
		})
	}); err != nil {
		return fmt.Errorf("recording the Lark card of question %s: %w", c.QuestionID, err)
	}

	return nil
}

// LarkQuestionCardOf returns the card of a question; ErrNotFound when it was
// sent as none.
func (s *Service) LarkQuestionCardOf(ctx context.Context, questionID string) (LarkQuestionCard, error) {
	row, err := s.read().GetLarkChannelQuestion(ctx, questionID)
	if err != nil {
		return LarkQuestionCard{}, notFound(err, "Lark card of question", questionID)
	}

	return larkQuestionCardFromRow(row), nil
}

// LarkQuestionCardByMessage returns the card a Lark message of a Channel is,
// while its question can still be answered; ErrNotFound otherwise.
func (s *Service) LarkQuestionCardByMessage(ctx context.Context, channel, messageID string,
) (LarkQuestionCard, error) {
	row, err := s.read().GetLarkChannelQuestionByMessage(ctx, store.GetLarkChannelQuestionByMessageParams{
		Channel: channel, MessageID: messageID,
	})
	if err != nil {
		return LarkQuestionCard{}, notFound(err, "Lark question card", messageID)
	}

	if row.ClosedAt.Valid {
		return LarkQuestionCard{}, fmt.Errorf("%w: the question of Lark card %s is closed", ErrNotFound, messageID)
	}

	return larkQuestionCardFromRow(row), nil
}

// OpenLarkQuestionCards returns the cards of a Channel that still show a
// question as open, oldest first.
func (s *Service) OpenLarkQuestionCards(ctx context.Context, channel string) ([]LarkQuestionCard, error) {
	rows, err := s.read().ListOpenLarkChannelQuestions(ctx, channel)
	if err != nil {
		return nil, fmt.Errorf("listing the question cards of Lark channel %s: %w", channel, err)
	}

	out := make([]LarkQuestionCard, 0, len(rows))
	for _, r := range rows {
		out = append(out, larkQuestionCardFromRow(r))
	}

	return out, nil
}

// CloseLarkQuestionCard records that a card was changed to say how its
// question ended.
func (s *Service) CloseLarkQuestionCard(ctx context.Context, questionID string, seq int) error {
	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		return q.CloseLarkChannelQuestion(ctx, store.CloseLarkChannelQuestionParams{
			QuestionID: questionID, Seq: int64(seq), Now: sql.NullInt64{Int64: now, Valid: true},
		})
	}); err != nil {
		return fmt.Errorf("closing the Lark card of question %s: %w", questionID, err)
	}

	return nil
}

func larkQuestionCardFromRow(r store.LarkChannelQuestion) LarkQuestionCard {
	return LarkQuestionCard{
		QuestionID: r.QuestionID, Channel: r.Channel, CardID: r.CardID, MessageID: r.MessageID, Seq: int(r.Seq),
	}
}

// LarkChannelGroup is a Lark group a Channel's bot is in (issue #246). The
// roster follows MemoryOS's ChannelGroupRoster: a group is added when the bot
// is added to it and by Lark's whole list of the bot's groups, and a group
// the list leaves out is removed. A group is off until the owner switches it
// on, as OpenClaw's Feishu groupPolicy is an allowlist by default.
type LarkChannelGroup struct {
	Channel string
	// ChatID is the group's chat id at Lark.
	ChatID string
	// Name is the group's name at Lark; empty when Lark gave none.
	Name    string
	Enabled bool
	// AskedAt is when someone the bot answers last asked there; nil while
	// nobody has.
	AskedAt   *time.Time
	CreatedAt time.Time
}

func larkChannelGroupFromRow(r store.LarkChannelGroup) LarkChannelGroup {
	return LarkChannelGroup{
		Channel: r.Channel, ChatID: r.LarkChatID, Name: r.Name, Enabled: r.Enabled != 0,
		AskedAt: optionalTime(r.AskedAt), CreatedAt: millis(r.CreatedAt),
	}
}

// JoinLarkChannelGroup records a group the Channel's bot is in. A name Lark
// gives replaces the one kept; an empty one keeps it. A group already known
// keeps whether it is on.
func (s *Service) JoinLarkChannelGroup(ctx context.Context, channel, chatID, name string) error {
	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		return q.UpsertLarkChannelGroup(ctx, store.UpsertLarkChannelGroupParams{
			Channel: channel, LarkChatID: chatID, Name: strings.TrimSpace(name), Now: now,
		})
	}); err != nil {
		return fmt.Errorf("recording Lark group %s of channel %s: %w", chatID, channel, err)
	}

	return nil
}

// LeaveLarkChannelGroup forgets a group the Channel's bot left. Its chat
// with the Bot stays, on the web.
func (s *Service) LeaveLarkChannelGroup(ctx context.Context, channel, chatID string) error {
	if err := s.write(ctx, func(q *store.Queries, _ int64) error {
		_, err := q.DeleteLarkChannelGroup(ctx, store.DeleteLarkChannelGroupParams{Channel: channel, LarkChatID: chatID})

		return err
	}); err != nil {
		return fmt.Errorf("forgetting Lark group %s of channel %s: %w", chatID, channel, err)
	}

	return nil
}

// ListedLarkChannelGroups makes a Channel's groups Lark's whole list of the
// bot's groups, read from readAt: it records those listed and removes the
// others, which the bot has left. A group added or asked in since readAt
// stays, as the list may be older (MemoryOS, JdbcChannelGroupRepository
// .replace). It returns the groups removed.
func (s *Service) ListedLarkChannelGroups(ctx context.Context, channel string, listed []LarkChannelGroup,
	readAt time.Time,
) ([]string, error) {
	var left []string

	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		kept := make(map[string]bool, len(listed))

		for _, g := range listed {
			kept[g.ChatID] = true

			if err := q.UpsertLarkChannelGroup(ctx, store.UpsertLarkChannelGroupParams{
				Channel: channel, LarkChatID: g.ChatID, Name: strings.TrimSpace(g.Name), Now: now,
			}); err != nil {
				return err
			}
		}

		rows, err := q.ListLarkChannelGroups(ctx, channel)
		if err != nil {
			return err
		}

		from := readAt.UnixMilli()

		for _, r := range rows {
			if kept[r.LarkChatID] || r.CreatedAt >= from || (r.AskedAt.Valid && r.AskedAt.Int64 >= from) {
				continue
			}

			if _, err := q.DeleteLarkChannelGroup(ctx, store.DeleteLarkChannelGroupParams{
				Channel: channel, LarkChatID: r.LarkChatID,
			}); err != nil {
				return err
			}

			left = append(left, r.LarkChatID)
		}

		return nil
	}); err != nil {
		return nil, fmt.Errorf("recording the Lark groups of channel %s: %w", channel, err)
	}

	return left, nil
}

// LarkChannelGroups returns a Channel's groups, the most recently asked
// first and those nobody has asked in last.
func (s *Service) LarkChannelGroups(ctx context.Context, channel string) ([]LarkChannelGroup, error) {
	rows, err := s.read().ListLarkChannelGroups(ctx, channel)
	if err != nil {
		return nil, fmt.Errorf("listing the groups of Lark channel %s: %w", channel, err)
	}

	out := make([]LarkChannelGroup, 0, len(rows))
	for _, r := range rows {
		out = append(out, larkChannelGroupFromRow(r))
	}

	return out, nil
}

// LarkChannelGroup returns one group of a Channel; ErrNotFound when the
// Channel's bot is not known to be in it.
func (s *Service) LarkChannelGroup(ctx context.Context, channel, chatID string) (LarkChannelGroup, error) {
	row, err := s.read().GetLarkChannelGroup(ctx, store.GetLarkChannelGroupParams{Channel: channel, LarkChatID: chatID})
	if err != nil {
		return LarkChannelGroup{}, notFound(err, "group of Lark channel "+channel, chatID)
	}

	return larkChannelGroupFromRow(row), nil
}

// SetLarkChannelGroup switches a group of a Channel on or off.
func (s *Service) SetLarkChannelGroup(ctx context.Context, channel, chatID string, enabled bool,
) (LarkChannelGroup, error) {
	on := int64(0)
	if enabled {
		on = 1
	}

	var changed int64

	if err := s.write(ctx, func(q *store.Queries, _ int64) error {
		var err error

		changed, err = q.SetLarkChannelGroupEnabled(ctx, store.SetLarkChannelGroupEnabledParams{
			Enabled: on, Channel: channel, LarkChatID: chatID,
		})

		return err
	}); err != nil {
		return LarkChannelGroup{}, fmt.Errorf("switching Lark group %s of channel %s: %w", chatID, channel, err)
	}

	if changed == 0 {
		return LarkChannelGroup{}, fmt.Errorf("%w: the bot of Lark channel %s is in no group %q", ErrNotFound,
			channel, chatID)
	}

	return s.LarkChannelGroup(ctx, channel, chatID)
}

// AskedInLarkChannelGroup records that someone the bot answers asked in a
// group now.
func (s *Service) AskedInLarkChannelGroup(ctx context.Context, channel, chatID string) error {
	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		return q.SetLarkChannelGroupAsked(ctx, store.SetLarkChannelGroupAskedParams{
			Now: sql.NullInt64{Int64: now, Valid: true}, Channel: channel, LarkChatID: chatID,
		})
	}); err != nil {
		return fmt.Errorf("recording a question in Lark group %s: %w", chatID, err)
	}

	return nil
}

// IsLarkGroupChat says whether a chat is the conversation of a Lark group:
// the Bot's memory is then not in its prompt.
func (s *Service) IsLarkGroupChat(ctx context.Context, chatID string) (bool, error) {
	group, err := s.read().IsLarkGroupChat(ctx, chatID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("reading whether chat %s is a Lark group's: %w", chatID, err)
	}

	return group != 0, nil
}
