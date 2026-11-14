package orchestration

import (
	"context"
	"errors"
	"fmt"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/channelstore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
)

// Lark Channels (issues #136 and #169) are kept by the domain store
// channelstore, reached through Channels() (issue #404). The names below stay
// so the Channels' callers keep theirs; what stays here is what also starts a
// chat, a write of the chats' domain.

// LarkChannelMode says who may talk through a Channel.
type LarkChannelMode = channelstore.LarkChannelMode

// The modes.
const (
	LarkChannelOwner     = channelstore.LarkChannelOwner
	LarkChannelAllowlist = channelstore.LarkChannelAllowlist
	LarkChannelEveryone  = channelstore.LarkChannelEveryone
)

// MaxLarkChannelName is the most characters of a Channel's name.
const MaxLarkChannelName = channelstore.MaxLarkChannelName

// ValidLarkChannelMode says whether m is a mode.
func ValidLarkChannelMode(m LarkChannelMode) bool { return channelstore.ValidLarkChannelMode(m) }

// ValidLarkChannelName says whether name can be a Channel's.
func ValidLarkChannelName(name string) bool { return channelstore.ValidLarkChannelName(name) }

// The Channels' records, as channelstore keeps them.
type (
	LarkChannel       = channelstore.LarkChannel
	LarkChannelPerson = channelstore.LarkChannelPerson
	LarkChannelChat   = channelstore.LarkChannelChat
	LarkChannelGroup  = channelstore.LarkChannelGroup
	LarkQuestionCard  = channelstore.LarkQuestionCard
)

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
	known, err := s.channels.LarkChannelChat(ctx, channel, larkChatID)
	if err == nil {
		return known, nil
	}

	if !errors.Is(err, ErrNotFound) {
		return LarkChannelChat{}, err
	}

	ch, err := s.channels.LarkChannel(ctx, channel)
	if err != nil {
		return LarkChannelChat{}, err
	}

	chat, err := s.CreateChat(ctx, ch.Bot, title)
	if err != nil {
		return LarkChannelChat{}, err
	}

	c := LarkChannelChat{
		Channel: channel, LarkChatID: larkChatID, OpenID: openID, ChatID: chat.ID, Bot: chat.Bot, Group: group,
	}

	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		return channelstore.RecordLarkChannelChatTx(ctx, q, c, now)
	}); err != nil {
		return LarkChannelChat{}, fmt.Errorf("recording Lark chat %s: %w", larkChatID, err)
	}

	return c, nil
}

// ResetLarkChannelChat gives a Lark chat a new chat with its Channel's Bot:
// the person's /new, or a group's conversation that was idle too long. The
// chat it had stays, on the web. ErrNotFound when the Lark chat has none yet.
func (s *Service) ResetLarkChannelChat(ctx context.Context, channel, larkChatID, title string,
) (LarkChannelChat, error) {
	old, err := s.channels.LarkChannelChat(ctx, channel, larkChatID)
	if err != nil {
		return LarkChannelChat{}, err
	}

	ch, err := s.channels.LarkChannel(ctx, channel)
	if err != nil {
		return LarkChannelChat{}, err
	}

	chat, err := s.CreateChat(ctx, ch.Bot, title)
	if err != nil {
		return LarkChannelChat{}, err
	}

	if err := s.write(ctx, func(q *store.Queries, _ int64) error {
		return channelstore.ResetLarkChannelChatTx(ctx, q, channel, larkChatID, chat.ID, old.Group)
	}); err != nil {
		return LarkChannelChat{}, fmt.Errorf("starting Lark chat %s over: %w", larkChatID, err)
	}

	return LarkChannelChat{
		Channel: channel, LarkChatID: larkChatID, OpenID: old.OpenID, ChatID: chat.ID, Bot: chat.Bot,
		Group: old.Group,
	}, nil
}
