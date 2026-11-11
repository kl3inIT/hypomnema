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

// The Lark bot (issue #136): a Channel the owner gives work through. This
// package keeps what the bot is and who may talk to it; the bot itself, which
// talks to Lark, is internal/controlplane/larkbot. The app's secret is in the
// vault; only its name is here.

// LarkBotMode says who may talk to the bot, after OpenClaw's dmPolicy for
// Feishu (closed by default, an allowlist, open).
type LarkBotMode string

// The modes (owner, 2026-11-10).
const (
	// LarkBotOwner: only the owner.
	LarkBotOwner LarkBotMode = "owner"
	// LarkBotAllowlist: the owner and the people he added.
	LarkBotAllowlist LarkBotMode = "allowlist"
	// LarkBotEveryone: every member of the app's Lark organization.
	LarkBotEveryone LarkBotMode = "everyone"
)

// ValidLarkBotMode says whether m is a mode.
func ValidLarkBotMode(m LarkBotMode) bool {
	return m == LarkBotOwner || m == LarkBotAllowlist || m == LarkBotEveryone
}

// LarkBot is the bot as the owner set it up.
type LarkBot struct {
	// AppID is the Lark app's id; Secret names the vault secret with its
	// secret.
	AppID  string
	Secret string
	Mode   LarkBotMode
	// OwnerOpenID is the owner's id at Lark, in the app's own namespace.
	OwnerOpenID string
	// Enabled says the bot answers. A bot that is off takes no message.
	Enabled   bool
	UpdatedAt time.Time
}

// LarkBotPerson is someone the owner added, who may talk to the bot when its
// mode is the allowlist.
type LarkBotPerson struct {
	OpenID    string
	Email     string
	Name      string
	CreatedAt time.Time
}

// LarkBotChat joins a person's direct chat with the bot at Lark to a chat
// with the coordinator.
type LarkBotChat struct {
	LarkChatID string
	OpenID     string
	ChatID     string
	// SentSeq is the last message of the chat the bot sent to Lark.
	SentSeq int64
}

// SetLarkBot records the bot, or changes it.
func (s *Service) SetLarkBot(ctx context.Context, b LarkBot) (LarkBot, error) {
	switch {
	case b.AppID == "" || b.Secret == "":
		return LarkBot{}, fmt.Errorf("%w: a Lark bot needs its app's id and secret", ErrInvalid)
	case !ValidLarkBotMode(b.Mode):
		return LarkBot{}, fmt.Errorf("%w: the mode is %s, %s or %s", ErrInvalid, LarkBotOwner, LarkBotAllowlist,
			LarkBotEveryone)
	case !strings.HasPrefix(b.OwnerOpenID, "ou_"):
		return LarkBot{}, fmt.Errorf("%w: the owner is named by his Lark open_id, which starts with ou_", ErrInvalid)
	}

	enabled := int64(0)
	if b.Enabled {
		enabled = 1
	}

	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		return q.UpsertLarkBot(ctx, store.UpsertLarkBotParams{
			AppID: b.AppID, Secret: b.Secret, Mode: string(b.Mode), OwnerOpenID: b.OwnerOpenID, Enabled: enabled,
			Now: now,
		})
	}); err != nil {
		return LarkBot{}, fmt.Errorf("recording the Lark bot: %w", err)
	}

	return s.LarkBot(ctx)
}

// LarkBot returns the bot; ErrNotFound when the owner set none up.
func (s *Service) LarkBot(ctx context.Context) (LarkBot, error) {
	row, err := s.read().GetLarkBot(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return LarkBot{}, fmt.Errorf("%w: no Lark bot is set up", ErrNotFound)
	}

	if err != nil {
		return LarkBot{}, fmt.Errorf("reading the Lark bot: %w", err)
	}

	return LarkBot{
		AppID: row.AppID, Secret: row.Secret, Mode: LarkBotMode(row.Mode), OwnerOpenID: row.OwnerOpenID,
		Enabled: row.Enabled != 0, UpdatedAt: millis(row.UpdatedAt),
	}, nil
}

// RemoveLarkBot forgets the bot. Its people and its chats stay: a bot set up
// again with the same app finds them.
func (s *Service) RemoveLarkBot(ctx context.Context) error {
	if err := s.write(ctx, func(q *store.Queries, _ int64) error { return q.DeleteLarkBot(ctx) }); err != nil {
		return fmt.Errorf("forgetting the Lark bot: %w", err)
	}

	return nil
}

// AddLarkBotPerson adds someone who may talk to the bot, or changes the email
// and name kept for them.
func (s *Service) AddLarkBotPerson(ctx context.Context, p LarkBotPerson) (LarkBotPerson, error) {
	if !strings.HasPrefix(p.OpenID, "ou_") {
		return LarkBotPerson{}, fmt.Errorf("%w: a person is named by a Lark open_id, which starts with ou_", ErrInvalid)
	}

	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		return q.UpsertLarkBotPerson(ctx, store.UpsertLarkBotPersonParams{
			OpenID: p.OpenID, Email: p.Email, Name: p.Name, Now: now,
		})
	}); err != nil {
		return LarkBotPerson{}, fmt.Errorf("adding %s to the Lark bot: %w", p.OpenID, err)
	}

	return s.LarkBotPerson(ctx, p.OpenID)
}

// LarkBotPerson returns one added person; ErrNotFound when they were not
// added.
func (s *Service) LarkBotPerson(ctx context.Context, openID string) (LarkBotPerson, error) {
	row, err := s.read().GetLarkBotPerson(ctx, openID)
	if err != nil {
		return LarkBotPerson{}, notFound(err, "Lark bot person", openID)
	}

	return larkBotPersonFromRow(row), nil
}

// LarkBotPeople returns the people the owner added, oldest first.
func (s *Service) LarkBotPeople(ctx context.Context) ([]LarkBotPerson, error) {
	rows, err := s.read().ListLarkBotPeople(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing the Lark bot's people: %w", err)
	}

	out := make([]LarkBotPerson, 0, len(rows))
	for _, r := range rows {
		out = append(out, larkBotPersonFromRow(r))
	}

	return out, nil
}

// RemoveLarkBotPerson takes a person off the list.
func (s *Service) RemoveLarkBotPerson(ctx context.Context, openID string) error {
	var removed int64

	if err := s.write(ctx, func(q *store.Queries, _ int64) error {
		var err error

		removed, err = q.DeleteLarkBotPerson(ctx, openID)

		return err
	}); err != nil {
		return fmt.Errorf("removing %s from the Lark bot: %w", openID, err)
	}

	if removed == 0 {
		return fmt.Errorf("%w: Lark bot person %q", ErrNotFound, openID)
	}

	return nil
}

func larkBotPersonFromRow(r store.LarkBotPerson) LarkBotPerson {
	return LarkBotPerson{OpenID: r.OpenID, Email: r.Email, Name: r.Name, CreatedAt: millis(r.CreatedAt)}
}

// TakeLarkMessage records that the bot took a Lark message and says whether
// it is the first time. Lark delivers an event again when it was not
// acknowledged within three seconds, so a second delivery is dropped here.
func (s *Service) TakeLarkMessage(ctx context.Context, messageID string) (first bool, err error) {
	var inserted int64

	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		var err error

		inserted, err = q.InsertLarkBotMessage(ctx, store.InsertLarkBotMessageParams{MessageID: messageID, Now: now})

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
		return q.DeleteLarkBotMessagesBefore(ctx, before.UnixMilli())
	}); err != nil {
		return fmt.Errorf("forgetting old Lark messages: %w", err)
	}

	return nil
}

// LarkBotChatFor returns the chat with the coordinator that a person's direct
// chat with the bot is, starting one the first time.
func (s *Service) LarkBotChatFor(ctx context.Context, larkChatID, openID, title string) (LarkBotChat, error) {
	row, err := s.read().GetLarkBotChat(ctx, larkChatID)
	if err == nil {
		return larkBotChatFromRow(row), nil
	}

	if !errors.Is(err, sql.ErrNoRows) {
		return LarkBotChat{}, fmt.Errorf("reading Lark chat %s: %w", larkChatID, err)
	}

	chat, err := s.CreateChat(ctx, title)
	if err != nil {
		return LarkBotChat{}, err
	}

	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		return q.InsertLarkBotChat(ctx, store.InsertLarkBotChatParams{
			LarkChatID: larkChatID, OpenID: openID, ChatID: chat.ID, Now: now,
		})
	}); err != nil {
		return LarkBotChat{}, fmt.Errorf("recording Lark chat %s: %w", larkChatID, err)
	}

	return LarkBotChat{LarkChatID: larkChatID, OpenID: openID, ChatID: chat.ID}, nil
}

// LarkBotChats returns the Lark chats the bot answers in, oldest first.
func (s *Service) LarkBotChats(ctx context.Context) ([]LarkBotChat, error) {
	rows, err := s.read().ListLarkBotChats(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing the Lark bot's chats: %w", err)
	}

	out := make([]LarkBotChat, 0, len(rows))
	for _, r := range rows {
		out = append(out, larkBotChatFromRow(r))
	}

	return out, nil
}

// MarkLarkBotSent records the last message of a chat the bot sent to Lark. It
// never moves back.
func (s *Service) MarkLarkBotSent(ctx context.Context, larkChatID string, seq int64) error {
	if err := s.write(ctx, func(q *store.Queries, _ int64) error {
		return q.SetLarkBotChatSent(ctx, store.SetLarkBotChatSentParams{LarkChatID: larkChatID, SentSeq: seq})
	}); err != nil {
		return fmt.Errorf("recording what was sent to Lark chat %s: %w", larkChatID, err)
	}

	return nil
}

func larkBotChatFromRow(r store.LarkBotChat) LarkBotChat {
	return LarkBotChat{LarkChatID: r.LarkChatID, OpenID: r.OpenID, ChatID: r.ChatID, SentSeq: r.SentSeq}
}

// LarkQuestionCard is the card at Lark that shows one of the owner's
// questions.
type LarkQuestionCard struct {
	QuestionID string
	CardID     string
	// MessageID is the Lark message the card is; a reply to it answers the
	// question in the owner's own words.
	MessageID string
	// Seq is the card's last update.
	Seq int
}

// RecordLarkQuestionCard records the card a question was sent as.
func (s *Service) RecordLarkQuestionCard(ctx context.Context, c LarkQuestionCard) error {
	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		return q.InsertLarkBotQuestion(ctx, store.InsertLarkBotQuestionParams{
			QuestionID: c.QuestionID, CardID: c.CardID, MessageID: c.MessageID, Now: now,
		})
	}); err != nil {
		return fmt.Errorf("recording the Lark card of question %s: %w", c.QuestionID, err)
	}

	return nil
}

// LarkQuestionCardOf returns the card of a question; ErrNotFound when it was
// sent as none.
func (s *Service) LarkQuestionCardOf(ctx context.Context, questionID string) (LarkQuestionCard, error) {
	row, err := s.read().GetLarkBotQuestion(ctx, questionID)
	if err != nil {
		return LarkQuestionCard{}, notFound(err, "Lark card of question", questionID)
	}

	return larkQuestionCardFromRow(row), nil
}

// LarkQuestionCardByMessage returns the card a Lark message is, while its
// question can still be answered; ErrNotFound otherwise.
func (s *Service) LarkQuestionCardByMessage(ctx context.Context, messageID string) (LarkQuestionCard, error) {
	row, err := s.read().GetLarkBotQuestionByMessage(ctx, messageID)
	if err != nil {
		return LarkQuestionCard{}, notFound(err, "Lark question card", messageID)
	}

	if row.ClosedAt.Valid {
		return LarkQuestionCard{}, fmt.Errorf("%w: the question of Lark card %s is closed", ErrNotFound, messageID)
	}

	return larkQuestionCardFromRow(row), nil
}

// OpenLarkQuestionCards returns the cards that still show a question as
// open, oldest first.
func (s *Service) OpenLarkQuestionCards(ctx context.Context) ([]LarkQuestionCard, error) {
	rows, err := s.read().ListOpenLarkBotQuestions(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing the Lark question cards: %w", err)
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
		return q.CloseLarkBotQuestion(ctx, store.CloseLarkBotQuestionParams{
			QuestionID: questionID, Seq: int64(seq), Now: sql.NullInt64{Int64: now, Valid: true},
		})
	}); err != nil {
		return fmt.Errorf("closing the Lark card of question %s: %w", questionID, err)
	}

	return nil
}

func larkQuestionCardFromRow(r store.LarkBotQuestion) LarkQuestionCard {
	return LarkQuestionCard{QuestionID: r.QuestionID, CardID: r.CardID, MessageID: r.MessageID, Seq: int(r.Seq)}
}
