// Package channelstore keeps the Channels (issues #136, #169 and #404), a
// domain store of the orchestration database reached through the root
// package's Channels(), as Omnara's storage facade reaches its stores (issue
// #404). It imports the generated store and never the root package; it only
// reads the tables of other domains (Bots, chats, Runs), through the shared
// queries. A write that spans another domain, such as starting a chat for a
// Lark chat or removing a Bot with its chats, stays in the root and calls the
// functions here that end in Tx with its own queries.
//
// A Channel is a door to one Bot: an account at a messaging service, of one
// kind (Lark today), and who may talk through it. Every kind keeps its rows in
// the same tables (channels, channel_people, channel_chats, ...). This
// package keeps what each Channel is and who may talk through it; what
// runs them is internal/controlplane/channels, and what talks to Lark
// channels/lark. An app's secret is in the
// vault; only its name is here.
package channelstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/storeutil"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/storeerr"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

// ChannelKind is the messaging service a Channel is an account at. Every
// kind keeps its Channels in the same tables, as MemoryOS keeps every
// provider in one set of channel tables with a provider column.
type ChannelKind string

// KindLark is a Lark app, the one kind there is (issue #404); a Channel set
// up without a kind is one, as the column's default says.
const KindLark ChannelKind = "lark"

// ChannelMode says who may talk through a Channel, after OpenClaw's
// dmPolicy for Feishu (closed by default, an allowlist, open).
type ChannelMode string

// The modes (owner, 2026-11-10).
const (
	// ChannelOwner: only the owner.
	ChannelOwner ChannelMode = "owner"
	// ChannelAllowlist: the owner and the people he added.
	ChannelAllowlist ChannelMode = "allowlist"
	// ChannelEveryone: every member of the app's Lark organization.
	ChannelEveryone ChannelMode = "everyone"
)

// ValidChannelMode says whether m is a mode.
func ValidChannelMode(m ChannelMode) bool {
	return m == ChannelOwner || m == ChannelAllowlist || m == ChannelEveryone
}

// MaxChannelName is the most characters of a Channel's name: the vault
// secret of its app is named after it, and a secret's name is 63 at most.
const MaxChannelName = 50

// ValidChannelName says whether name can be a Channel's: as a Bot's,
// lowercase letters, digits and dashes, starting with a letter.
func ValidChannelName(name string) bool {
	return len(name) <= MaxChannelName && storeutil.NamePattern.MatchString(name)
}

// Channel is a Channel as the owner set it up.
type Channel struct {
	Name string
	Kind ChannelKind
	// AppID is the Lark app's id; Secret names the vault secret with its
	// secret. One app is one Channel.
	AppID  string
	Secret string
	// Bot is the Bot a chat opened through the Channel talks with. Several
	// Channels may talk to one Bot (OpenClaw's bindings).
	Bot  string
	Mode ChannelMode
	// OwnerID is the owner's id at the service: at Lark his open_id, in the
	// app's own namespace.
	OwnerID string
	// Enabled says the Channel answers. One that is off takes no message.
	Enabled bool
	// WebLink says a reply in a direct chat ends with a link to the chat in
	// the web client (issue #305); a group's reply never has one.
	WebLink   bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ChannelPerson is someone the owner added, who may talk through a
// Channel when its mode is the allowlist. Lark gives a person another open_id
// in each app, so a person is added to each Channel apart.
type ChannelPerson struct {
	// PersonID is the person's id at the service: Lark's open_id.
	PersonID  string
	Email     string
	Name      string
	CreatedAt time.Time
}

// ChannelChat joins a person's direct chat with a Channel's app at Lark
// to a chat with a Bot.
type ChannelChat struct {
	Channel string
	// ExternalChatID is the chat's id at the service: Lark's chat_id.
	ExternalChatID string
	// PersonID is the person's id at the service, Lark's open_id.
	PersonID string
	ChatID   string
	// Bot is the Bot of the chat: the Channel's when the chat was opened.
	Bot string
	// SentSeq is the last message of the chat that was sent to Lark.
	SentSeq int64
	// Group says the Lark chat is a group, whose one chat with the Bot is the
	// whole group's (OpenClaw's Feishu groupSessionScope "group"); PersonID is
	// then empty.
	Group bool
}

// Store keeps the Channels.
type Store struct {
	db    *sql.DB
	clock platform.Clock
	// signal wakes the orchestration's event readers after each commit.
	signal func()
}

// New returns the Store over the orchestration database. signal is the
// root's, called after each commit as for every other write.
func New(db *sql.DB, clock platform.Clock, signal func()) *Store {
	return &Store{db: db, clock: clock, signal: signal}
}

func (s *Store) write(ctx context.Context, fn func(q *store.Queries, now int64) error) error {
	return storeutil.Write(ctx, s.db, s.clock, s.signal, fn)
}

func (s *Store) read() *store.Queries {
	return store.New(s.db)
}

func channelFromRow(r store.Channel) Channel {
	return Channel{
		Name: r.Name, Kind: ChannelKind(r.Kind), AppID: r.AppID, Secret: r.Secret, Bot: r.Bot, Mode: ChannelMode(r.Mode),
		OwnerID: r.OwnerID, Enabled: r.Enabled != 0, WebLink: r.WebLink != 0,
		CreatedAt: storeutil.Millis(r.CreatedAt), UpdatedAt: storeutil.Millis(r.UpdatedAt),
	}
}

// SetChannel records a Channel, or changes it. An app that is another
// Channel's is refused, as Hermes refuses two profiles on one app.
func (s *Store) SetChannel(ctx context.Context, c Channel) (Channel, error) {
	if c.Kind == "" {
		c.Kind = KindLark
	}

	switch {
	case c.Kind != KindLark:
		return Channel{}, fmt.Errorf("%w: a channel's kind is %s", storeerr.ErrInvalid, KindLark)
	case !ValidChannelName(c.Name):
		return Channel{}, fmt.Errorf("%w: a channel's name is lowercase letters, digits and dashes, starting "+
			"with a letter, at most %d characters", storeerr.ErrInvalid, MaxChannelName)
	case c.AppID == "" || c.Secret == "":
		return Channel{}, fmt.Errorf("%w: a Lark channel needs its app's id and secret", storeerr.ErrInvalid)
	case c.Bot == "":
		return Channel{}, fmt.Errorf("%w: a Lark channel names the bot it talks to", storeerr.ErrInvalid)
	case !ValidChannelMode(c.Mode):
		return Channel{}, fmt.Errorf("%w: the mode is %s, %s or %s", storeerr.ErrInvalid, ChannelOwner,
			ChannelAllowlist, ChannelEveryone)
	case !strings.HasPrefix(c.OwnerID, "ou_"):
		return Channel{}, fmt.Errorf("%w: the owner is named by his Lark open_id, which starts with ou_",
			storeerr.ErrInvalid)
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
				return fmt.Errorf("%w: no bot is named %q", storeerr.ErrInvalid, c.Bot)
			}

			return err
		}

		other, err := q.GetChannelByApp(ctx, store.GetChannelByAppParams{Kind: string(c.Kind), AppID: c.AppID})
		if err == nil && other.Name != c.Name {
			return fmt.Errorf("%w: the Lark app %s is the app of channel %s; one app is one channel",
				storeerr.ErrConflict, c.AppID, other.Name)
		}

		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}

		return q.UpsertChannel(ctx, store.UpsertChannelParams{
			Name: c.Name, Kind: string(c.Kind), AppID: c.AppID, Secret: c.Secret, Bot: c.Bot, Mode: string(c.Mode),
			OwnerID: c.OwnerID, Enabled: enabled, WebLink: webLink, Now: now,
		})
	}); err != nil {
		return Channel{}, fmt.Errorf("recording Lark channel %s: %w", c.Name, err)
	}

	return s.Channel(ctx, c.Name)
}

// Channel returns one Channel; ErrNotFound when the owner set none up
// under that name.
func (s *Store) Channel(ctx context.Context, name string) (Channel, error) {
	row, err := s.read().GetChannel(ctx, name)
	if err != nil {
		return Channel{}, storeutil.NotFound(err, "Lark channel", name)
	}

	return channelFromRow(row), nil
}

// ListChannels returns the Channels, oldest first.
func (s *Store) ListChannels(ctx context.Context) ([]Channel, error) {
	rows, err := s.read().ListChannels(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing the Lark channels: %w", err)
	}

	out := make([]Channel, 0, len(rows))
	for _, r := range rows {
		out = append(out, channelFromRow(r))
	}

	return out, nil
}

// RemoveChannel forgets a Channel with its people, the Lark chats it
// answered in and its question cards. The chats with the Bot stay, on the
// web.
func (s *Store) RemoveChannel(ctx context.Context, name string) error {
	var removed int64

	if err := s.write(ctx, func(q *store.Queries, _ int64) error {
		var err error

		removed, err = q.DeleteChannel(ctx, name)

		return err
	}); err != nil {
		return fmt.Errorf("forgetting Lark channel %s: %w", name, err)
	}

	if removed == 0 {
		return fmt.Errorf("%w: Lark channel %q", storeerr.ErrNotFound, name)
	}

	return nil
}

// CountChannelsOfBotTx counts the Channels that talk to a Bot, with the
// caller's queries: the root refuses to remove a Bot that one talks to.
func CountChannelsOfBotTx(ctx context.Context, q *store.Queries, bot string) (int64, error) {
	return q.CountChannelsOfBot(ctx, bot)
}

// AddChannelPerson adds someone who may talk through a Channel, or
// changes the email and name kept for them.
func (s *Store) AddChannelPerson(ctx context.Context, channel string, p ChannelPerson,
) (ChannelPerson, error) {
	if !strings.HasPrefix(p.PersonID, "ou_") {
		return ChannelPerson{}, fmt.Errorf("%w: a person is named by a Lark open_id, which starts with ou_",
			storeerr.ErrInvalid)
	}

	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		if _, err := q.GetChannel(ctx, channel); err != nil {
			return storeutil.NotFound(err, "Lark channel", channel)
		}

		return q.UpsertChannelPerson(ctx, store.UpsertChannelPersonParams{
			Channel: channel, PersonID: p.PersonID, Email: p.Email, Name: p.Name, Now: now,
		})
	}); err != nil {
		return ChannelPerson{}, fmt.Errorf("adding %s to Lark channel %s: %w", p.PersonID, channel, err)
	}

	return s.ChannelPerson(ctx, channel, p.PersonID)
}

// ChannelPerson returns one person added to a Channel; ErrNotFound when
// they were not added to it.
func (s *Store) ChannelPerson(ctx context.Context, channel, personID string) (ChannelPerson, error) {
	row, err := s.read().GetChannelPerson(ctx, store.GetChannelPersonParams{Channel: channel, PersonID: personID})
	if err != nil {
		return ChannelPerson{}, storeutil.NotFound(err, "person of Lark channel "+channel, personID)
	}

	return channelPersonFromRow(row), nil
}

// ChannelPeople returns the people the owner added to a Channel, oldest
// first.
func (s *Store) ChannelPeople(ctx context.Context, channel string) ([]ChannelPerson, error) {
	rows, err := s.read().ListChannelPeople(ctx, channel)
	if err != nil {
		return nil, fmt.Errorf("listing the people of Lark channel %s: %w", channel, err)
	}

	out := make([]ChannelPerson, 0, len(rows))
	for _, r := range rows {
		out = append(out, channelPersonFromRow(r))
	}

	return out, nil
}

// RemoveChannelPerson takes a person off a Channel's list.
func (s *Store) RemoveChannelPerson(ctx context.Context, channel, personID string) error {
	var removed int64

	if err := s.write(ctx, func(q *store.Queries, _ int64) error {
		var err error

		removed, err = q.DeleteChannelPerson(ctx, store.DeleteChannelPersonParams{
			Channel: channel, PersonID: personID,
		})

		return err
	}); err != nil {
		return fmt.Errorf("removing %s from Lark channel %s: %w", personID, channel, err)
	}

	if removed == 0 {
		return fmt.Errorf("%w: person %q of Lark channel %s", storeerr.ErrNotFound, personID, channel)
	}

	return nil
}

func channelPersonFromRow(r store.ChannelPerson) ChannelPerson {
	return ChannelPerson{PersonID: r.PersonID, Email: r.Email, Name: r.Name, CreatedAt: storeutil.Millis(r.CreatedAt)}
}

// TakeChannelMessage records that a Channel took a Lark message and says
// whether it is the first time. Lark delivers an event again when it was not
// acknowledged within three seconds, so a second delivery is dropped here.
func (s *Store) TakeChannelMessage(ctx context.Context, channel, messageID string) (first bool, err error) {
	var inserted int64

	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		var err error

		inserted, err = q.InsertChannelMessage(ctx, store.InsertChannelMessageParams{
			MessageID: messageID, Channel: channel, Now: now,
		})

		return err
	}); err != nil {
		return false, fmt.Errorf("taking Lark message %s: %w", messageID, err)
	}

	return inserted == 1, nil
}

// ForgetChannelMessagesBefore drops the record of messages taken before a time:
// Lark's last delivery of an event comes about seven hours after the first.
func (s *Store) ForgetChannelMessagesBefore(ctx context.Context, before time.Time) error {
	if err := s.write(ctx, func(q *store.Queries, _ int64) error {
		return q.DeleteChannelMessagesBefore(ctx, before.UnixMilli())
	}); err != nil {
		return fmt.Errorf("forgetting old Lark messages: %w", err)
	}

	return nil
}

// channelChat is a Lark chat's row with the Bot of its chat.
func (s *Store) channelChat(ctx context.Context, r store.ChannelChat) (ChannelChat, error) {
	chat, err := s.read().GetChat(ctx, r.ChatID)
	if err != nil {
		return ChannelChat{}, fmt.Errorf("reading the chat of Lark chat %s: %w", r.ExternalChatID, err)
	}

	return ChannelChat{
		Channel: r.Channel, ExternalChatID: r.ExternalChatID, PersonID: r.PersonID, ChatID: r.ChatID, Bot: chat.Bot,
		SentSeq: r.SentSeq, Group: chat.ChannelGroup != 0,
	}, nil
}

// ChannelChat returns the chat a Lark chat of a Channel is; ErrNotFound
// when the Lark chat has none yet.
func (s *Store) ChannelChat(ctx context.Context, channel, externalChatID string) (ChannelChat, error) {
	row, err := s.read().GetChannelChat(ctx, store.GetChannelChatParams{
		Channel: channel, ExternalChatID: externalChatID,
	})
	if err != nil {
		return ChannelChat{}, storeutil.NotFound(err, "Lark chat", externalChatID)
	}

	return s.channelChat(ctx, row)
}

// RecordChannelChatTx records, with the caller's queries, that a Lark
// chat is the chat c.ChatID, which the caller started in the same or an
// earlier transaction. A group's chat is marked on the chat itself, so it
// stays one once the group has started another.
func RecordChannelChatTx(ctx context.Context, q *store.Queries, c ChannelChat, now int64) error {
	if c.Group {
		if err := q.MarkChannelGroupChat(ctx, c.ChatID); err != nil {
			return err
		}
	}

	return q.InsertChannelChat(ctx, store.InsertChannelChatParams{
		ExternalChatID: c.ExternalChatID, Channel: c.Channel, PersonID: c.PersonID, ChatID: c.ChatID, Now: now,
	})
}

// ResetChannelChatTx points a Lark chat at the new chat chatID, with the
// caller's queries; group marks the new chat as a group's, as the old one
// was.
func ResetChannelChatTx(ctx context.Context, q *store.Queries, channel, externalChatID, chatID string, group bool,
) error {
	if group {
		if err := q.MarkChannelGroupChat(ctx, chatID); err != nil {
			return err
		}
	}

	_, err := q.ResetChannelChat(ctx, store.ResetChannelChatParams{
		Channel: channel, ExternalChatID: externalChatID, ChatID: chatID,
	})

	return err
}

// DeleteChannelChatOfChatTx forgets the Lark chat a chat answered, with
// the caller's queries, as the root deletes the chat (issue #484), on its
// own or with its Bot.
func DeleteChannelChatOfChatTx(ctx context.Context, q *store.Queries, chatID string) error {
	return q.DeleteChannelChatOfChat(ctx, chatID)
}

// ChannelChats returns the Lark chats a Channel answers in, oldest
// first.
func (s *Store) ChannelChats(ctx context.Context, channel string) ([]ChannelChat, error) {
	rows, err := s.read().ListChannelChats(ctx, channel)
	if err != nil {
		return nil, fmt.Errorf("listing the chats of Lark channel %s: %w", channel, err)
	}

	out := make([]ChannelChat, 0, len(rows))

	for _, r := range rows {
		chat, err := s.channelChat(ctx, r)
		if err != nil {
			return nil, err
		}

		out = append(out, chat)
	}

	return out, nil
}

// MarkChannelSent records the last message of a chat that was sent to
// Lark. It never moves back.
func (s *Store) MarkChannelSent(ctx context.Context, externalChatID string, seq int64) error {
	if err := s.write(ctx, func(q *store.Queries, _ int64) error {
		return q.SetChannelChatSent(ctx, store.SetChannelChatSentParams{ExternalChatID: externalChatID, SentSeq: seq})
	}); err != nil {
		return fmt.Errorf("recording what was sent to Lark chat %s: %w", externalChatID, err)
	}

	return nil
}

// RenameChannelChat records the Lark chat id of a person's direct chat
// that was kept under their open_id until Lark named the chat (issue #312: a
// press of the bot's menu names no chat, and the reply sent to the person
// does). ErrNotFound when no chat of the Channel has the old id; ErrConflict
// when one has the new id already.
func (s *Store) RenameChannelChat(ctx context.Context, channel, oldID, newID string) error {
	if _, err := s.read().GetChannelChat(ctx, store.GetChannelChatParams{
		Channel: channel, ExternalChatID: newID,
	}); err == nil {
		return fmt.Errorf("%w: Lark chat %s is a chat of channel %s already", storeerr.ErrConflict, newID, channel)
	}

	var renamed int64

	if err := s.write(ctx, func(q *store.Queries, _ int64) error {
		var err error

		renamed, err = q.RenameChannelChat(ctx, store.RenameChannelChatParams{
			NewExternalChatID: newID, Channel: channel, ExternalChatID: oldID,
		})

		return err
	}); err != nil {
		return fmt.Errorf("renaming Lark chat %s: %w", oldID, err)
	}

	if renamed == 0 {
		return fmt.Errorf("%w: channel %s has no Lark chat %s", storeerr.ErrNotFound, channel, oldID)
	}

	return nil
}

// ChannelForRun returns the Channel a question of a Run goes out through:
// the Channel whose Lark chat is the chat that started the Run, as Hermes
// sends an approval to the conversation the work came from; for a Run that
// started elsewhere, the oldest Channel that is on. ErrNotFound when no
// Channel is on.
func (s *Store) ChannelForRun(ctx context.Context, runID string) (string, error) {
	q := s.read()

	channel, err := q.ChannelOfRun(ctx, runID)
	if err == nil {
		return channel, nil
	}

	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("reading the Lark channel of run %s: %w", runID, err)
	}

	channel, err = q.FirstEnabledChannel(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%w: no Lark channel is on", storeerr.ErrNotFound)
	}

	if err != nil {
		return "", fmt.Errorf("reading the first Lark channel: %w", err)
	}

	return channel, nil
}

// QuestionCard is the card at Lark that shows one of the owner's
// questions.
type QuestionCard struct {
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

// RecordQuestionCard records the card a question was sent as.
func (s *Store) RecordQuestionCard(ctx context.Context, c QuestionCard) error {
	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		return q.InsertChannelQuestion(ctx, store.InsertChannelQuestionParams{
			QuestionID: c.QuestionID, Channel: c.Channel, CardID: c.CardID, MessageID: c.MessageID, Now: now,
		})
	}); err != nil {
		return fmt.Errorf("recording the Lark card of question %s: %w", c.QuestionID, err)
	}

	return nil
}

// QuestionCardOf returns the card of a question; ErrNotFound when it was
// sent as none.
func (s *Store) QuestionCardOf(ctx context.Context, questionID string) (QuestionCard, error) {
	row, err := s.read().GetChannelQuestion(ctx, questionID)
	if err != nil {
		return QuestionCard{}, storeutil.NotFound(err, "Lark card of question", questionID)
	}

	return questionCardFromRow(row), nil
}

// QuestionCardByMessage returns the card a Lark message of a Channel is,
// while its question can still be answered; ErrNotFound otherwise.
func (s *Store) QuestionCardByMessage(ctx context.Context, channel, messageID string,
) (QuestionCard, error) {
	row, err := s.read().GetChannelQuestionByMessage(ctx, store.GetChannelQuestionByMessageParams{
		Channel: channel, MessageID: messageID,
	})
	if err != nil {
		return QuestionCard{}, storeutil.NotFound(err, "Lark question card", messageID)
	}

	if row.ClosedAt.Valid {
		return QuestionCard{}, fmt.Errorf("%w: the question of Lark card %s is closed", storeerr.ErrNotFound,
			messageID)
	}

	return questionCardFromRow(row), nil
}

// OpenQuestionCards returns the cards of a Channel that still show a
// question as open, oldest first.
func (s *Store) OpenQuestionCards(ctx context.Context, channel string) ([]QuestionCard, error) {
	rows, err := s.read().ListOpenChannelQuestions(ctx, channel)
	if err != nil {
		return nil, fmt.Errorf("listing the question cards of Lark channel %s: %w", channel, err)
	}

	out := make([]QuestionCard, 0, len(rows))
	for _, r := range rows {
		out = append(out, questionCardFromRow(r))
	}

	return out, nil
}

// CloseQuestionCard records that a card was changed to say how its
// question ended.
func (s *Store) CloseQuestionCard(ctx context.Context, questionID string, seq int) error {
	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		return q.CloseChannelQuestion(ctx, store.CloseChannelQuestionParams{
			QuestionID: questionID, Seq: int64(seq), Now: sql.NullInt64{Int64: now, Valid: true},
		})
	}); err != nil {
		return fmt.Errorf("closing the Lark card of question %s: %w", questionID, err)
	}

	return nil
}

func questionCardFromRow(r store.ChannelQuestion) QuestionCard {
	return QuestionCard{
		QuestionID: r.QuestionID, Channel: r.Channel, CardID: r.CardID, MessageID: r.MessageID, Seq: int(r.Seq),
	}
}

// ChannelGroup is a Lark group a Channel's bot is in (issue #246). The
// roster follows MemoryOS's ChannelGroupRoster: a group is added when the bot
// is added to it and by Lark's whole list of the bot's groups, and a group
// the list leaves out is removed. A group is off until the owner switches it
// on, as OpenClaw's Feishu groupPolicy is an allowlist by default.
type ChannelGroup struct {
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

func channelGroupFromRow(r store.ChannelGroup) ChannelGroup {
	return ChannelGroup{
		Channel: r.Channel, ChatID: r.ExternalChatID, Name: r.Name, Enabled: r.Enabled != 0,
		AskedAt: storeutil.OptionalMillis(r.AskedAt), CreatedAt: storeutil.Millis(r.CreatedAt),
	}
}

// JoinChannelGroup records a group the Channel's bot is in. A name Lark
// gives replaces the one kept; an empty one keeps it. A group already known
// keeps whether it is on.
func (s *Store) JoinChannelGroup(ctx context.Context, channel, chatID, name string) error {
	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		return q.UpsertChannelGroup(ctx, store.UpsertChannelGroupParams{
			Channel: channel, ExternalChatID: chatID, Name: strings.TrimSpace(name), Now: now,
		})
	}); err != nil {
		return fmt.Errorf("recording Lark group %s of channel %s: %w", chatID, channel, err)
	}

	return nil
}

// LeaveChannelGroup forgets a group the Channel's bot left. Its chat
// with the Bot stays, on the web.
func (s *Store) LeaveChannelGroup(ctx context.Context, channel, chatID string) error {
	if err := s.write(ctx, func(q *store.Queries, _ int64) error {
		_, err := q.DeleteChannelGroup(ctx, store.DeleteChannelGroupParams{Channel: channel, ExternalChatID: chatID})

		return err
	}); err != nil {
		return fmt.Errorf("forgetting Lark group %s of channel %s: %w", chatID, channel, err)
	}

	return nil
}

// ListedChannelGroups makes a Channel's groups Lark's whole list of the
// bot's groups, read from readAt: it records those listed and removes the
// others, which the bot has left. A group added or asked in since readAt
// stays, as the list may be older (MemoryOS, JdbcChannelGroupRepository
// .replace). It returns the groups removed.
func (s *Store) ListedChannelGroups(ctx context.Context, channel string, listed []ChannelGroup,
	readAt time.Time,
) ([]string, error) {
	var left []string

	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		kept := make(map[string]bool, len(listed))

		for _, g := range listed {
			kept[g.ChatID] = true

			if err := q.UpsertChannelGroup(ctx, store.UpsertChannelGroupParams{
				Channel: channel, ExternalChatID: g.ChatID, Name: strings.TrimSpace(g.Name), Now: now,
			}); err != nil {
				return err
			}
		}

		rows, err := q.ListChannelGroups(ctx, channel)
		if err != nil {
			return err
		}

		from := readAt.UnixMilli()

		for _, r := range rows {
			if kept[r.ExternalChatID] || r.CreatedAt >= from || (r.AskedAt.Valid && r.AskedAt.Int64 >= from) {
				continue
			}

			if _, err := q.DeleteChannelGroup(ctx, store.DeleteChannelGroupParams{
				Channel: channel, ExternalChatID: r.ExternalChatID,
			}); err != nil {
				return err
			}

			left = append(left, r.ExternalChatID)
		}

		return nil
	}); err != nil {
		return nil, fmt.Errorf("recording the Lark groups of channel %s: %w", channel, err)
	}

	return left, nil
}

// ChannelGroups returns a Channel's groups, the most recently asked
// first and those nobody has asked in last.
func (s *Store) ChannelGroups(ctx context.Context, channel string) ([]ChannelGroup, error) {
	rows, err := s.read().ListChannelGroups(ctx, channel)
	if err != nil {
		return nil, fmt.Errorf("listing the groups of Lark channel %s: %w", channel, err)
	}

	out := make([]ChannelGroup, 0, len(rows))
	for _, r := range rows {
		out = append(out, channelGroupFromRow(r))
	}

	return out, nil
}

// ChannelGroup returns one group of a Channel; ErrNotFound when the
// Channel's bot is not known to be in it.
func (s *Store) ChannelGroup(ctx context.Context, channel, chatID string) (ChannelGroup, error) {
	row, err := s.read().GetChannelGroup(ctx, store.GetChannelGroupParams{Channel: channel, ExternalChatID: chatID})
	if err != nil {
		return ChannelGroup{}, storeutil.NotFound(err, "group of Lark channel "+channel, chatID)
	}

	return channelGroupFromRow(row), nil
}

// SetChannelGroup switches a group of a Channel on or off.
func (s *Store) SetChannelGroup(ctx context.Context, channel, chatID string, enabled bool,
) (ChannelGroup, error) {
	on := int64(0)
	if enabled {
		on = 1
	}

	var changed int64

	if err := s.write(ctx, func(q *store.Queries, _ int64) error {
		var err error

		changed, err = q.SetChannelGroupEnabled(ctx, store.SetChannelGroupEnabledParams{
			Enabled: on, Channel: channel, ExternalChatID: chatID,
		})

		return err
	}); err != nil {
		return ChannelGroup{}, fmt.Errorf("switching Lark group %s of channel %s: %w", chatID, channel, err)
	}

	if changed == 0 {
		return ChannelGroup{}, fmt.Errorf("%w: the bot of Lark channel %s is in no group %q",
			storeerr.ErrNotFound, channel, chatID)
	}

	return s.ChannelGroup(ctx, channel, chatID)
}

// AskedInChannelGroup records that someone the bot answers asked in a
// group now.
func (s *Store) AskedInChannelGroup(ctx context.Context, channel, chatID string) error {
	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		return q.SetChannelGroupAsked(ctx, store.SetChannelGroupAskedParams{
			Now: sql.NullInt64{Int64: now, Valid: true}, Channel: channel, ExternalChatID: chatID,
		})
	}); err != nil {
		return fmt.Errorf("recording a question in Lark group %s: %w", chatID, err)
	}

	return nil
}

// IsChannelGroupChat says whether a chat is the conversation of a Lark group:
// the Bot's memory is then not in its prompt.
func (s *Store) IsChannelGroupChat(ctx context.Context, chatID string) (bool, error) {
	group, err := s.read().IsChannelGroupChat(ctx, chatID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("reading whether chat %s is a Lark group's: %w", chatID, err)
	}

	return group != 0, nil
}
