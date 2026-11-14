package orchestration

import (
	"context"
	"errors"
	"fmt"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/channelstore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
)

// Channels (issues #136, #169 and #404) are kept by the domain store
// channelstore, reached through Channels(). What stays here is what also
// starts a chat, a write of the chats' domain.

// ChannelChatFor returns the chat that a person's direct chat with a
// Channel's account is, starting one with the Channel's Bot the first time.
func (s *Service) ChannelChatFor(ctx context.Context, channel, externalChatID, personID, title string,
) (channelstore.ChannelChat, error) {
	return s.channelChatFor(ctx, channel, externalChatID, personID, title, false)
}

// ChannelGroupChatFor returns the chat that a group is, the whole group's
// one, starting one with the Channel's Bot the first time.
func (s *Service) ChannelGroupChatFor(ctx context.Context, channel, externalChatID, title string,
) (channelstore.ChannelChat, error) {
	return s.channelChatFor(ctx, channel, externalChatID, "", title, true)
}

func (s *Service) channelChatFor(ctx context.Context, channel, externalChatID, personID, title string, group bool,
) (channelstore.ChannelChat, error) {
	known, err := s.channels.ChannelChat(ctx, channel, externalChatID)
	if err == nil {
		return known, nil
	}

	if !errors.Is(err, ErrNotFound) {
		return channelstore.ChannelChat{}, err
	}

	ch, err := s.channels.Channel(ctx, channel)
	if err != nil {
		return channelstore.ChannelChat{}, err
	}

	chat, err := s.CreateChat(ctx, ch.Bot, title)
	if err != nil {
		return channelstore.ChannelChat{}, err
	}

	c := channelstore.ChannelChat{
		Channel: channel, ExternalChatID: externalChatID, PersonID: personID, ChatID: chat.ID, Bot: chat.Bot,
		Group: group,
	}

	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		return channelstore.RecordChannelChatTx(ctx, q, c, now)
	}); err != nil {
		return channelstore.ChannelChat{}, fmt.Errorf("recording Lark chat %s: %w", externalChatID, err)
	}

	return c, nil
}

// ResetChannelChat gives a chat at a Channel a new chat with its Channel's
// Bot: the person's /new, or a group's conversation that was idle too long.
// The chat it had stays, on the web. ErrNotFound when the chat at the
// Channel has none yet.
func (s *Service) ResetChannelChat(ctx context.Context, channel, externalChatID, title string,
) (channelstore.ChannelChat, error) {
	old, err := s.channels.ChannelChat(ctx, channel, externalChatID)
	if err != nil {
		return channelstore.ChannelChat{}, err
	}

	ch, err := s.channels.Channel(ctx, channel)
	if err != nil {
		return channelstore.ChannelChat{}, err
	}

	chat, err := s.CreateChat(ctx, ch.Bot, title)
	if err != nil {
		return channelstore.ChannelChat{}, err
	}

	if err := s.write(ctx, func(q *store.Queries, _ int64) error {
		return channelstore.ResetChannelChatTx(ctx, q, channel, externalChatID, chat.ID, old.Group)
	}); err != nil {
		return channelstore.ChannelChat{}, fmt.Errorf("starting Lark chat %s over: %w", externalChatID, err)
	}

	return channelstore.ChannelChat{
		Channel: channel, ExternalChatID: externalChatID, PersonID: old.PersonID, ChatID: chat.ID, Bot: chat.Bot,
		Group: old.Group,
	}, nil
}
