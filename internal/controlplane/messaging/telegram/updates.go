package telegram

// Every chat kept as it arrives (issue #1184): gotd's updates manager hands each update to the dispatcher below,
// after it recovered any gap from Telegram's pts/qts/seq (telegram/updates/doc.go); what it cannot recover it reports,
// and the chats are fetched again here (history.go). The handlers are those gotd's examples/userbot registers
// (dispatcher.OnNewMessage, main.go:192), for the channels' messages, the edits and the deletions too.

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/gotd/td/tg"
)

// updateHandler keeps what each update's entities say, then hands the update to the dispatcher, as gotd's
// examples/userbot fills its peer storage before its dispatcher (storage.UpdateHook, main.go:139): a chat the
// account left or lost comes as an entity of any update, and a lost one (forbidden) is left out of tg.Entities.
type updateHandler struct {
	a        *Account
	dispatch tg.UpdateDispatcher
}

func (h updateHandler) Handle(ctx context.Context, u tg.UpdatesClass) error {
	var err error

	switch u := u.(type) {
	case *tg.Updates:
		_, err = h.a.keepRaw(ctx, u.Users, u.Chats)
	case *tg.UpdatesCombined:
		_, err = h.a.keepRaw(ctx, u.Users, u.Chats)
	}

	return errors.Join(err, h.dispatch.Handle(ctx, u))
}

// handler is the handler of the updates the account keeps.
func (a *Account) handler() updateHandler {
	d := tg.NewUpdateDispatcher()

	d.OnNewMessage(func(ctx context.Context, e tg.Entities, u *tg.UpdateNewMessage) error {
		return a.keepUpdate(ctx, e, u.Message, false)
	})
	d.OnNewChannelMessage(func(ctx context.Context, e tg.Entities, u *tg.UpdateNewChannelMessage) error {
		return a.keepUpdate(ctx, e, u.Message, false)
	})
	d.OnEditMessage(func(ctx context.Context, e tg.Entities, u *tg.UpdateEditMessage) error {
		return a.keepUpdate(ctx, e, u.Message, true)
	})
	d.OnEditChannelMessage(func(ctx context.Context, e tg.Entities, u *tg.UpdateEditChannelMessage) error {
		return a.keepUpdate(ctx, e, u.Message, true)
	})
	d.OnDeleteMessages(func(ctx context.Context, _ tg.Entities, u *tg.UpdateDeleteMessages) error {
		return a.store.revoke(ctx, a.key, "", u.Messages)
	})
	d.OnDeleteChannelMessages(func(ctx context.Context, _ tg.Entities, u *tg.UpdateDeleteChannelMessages) error {
		return a.store.revoke(ctx, a.key, channelID(u.ChannelID), u.Messages)
	})

	return updateHandler{a: a, dispatch: d}
}

// keepUpdate keeps a message an update brought, with what its entities say of its chat and of the people in it. An
// edit Telegram hides (a reaction's) changes nothing kept.
func (a *Account) keepUpdate(ctx context.Context, e tg.Entities, mc tg.MessageClass, edit bool) error {
	if svc, ok := mc.(*tg.MessageService); ok {
		return a.keepService(ctx, e, svc)
	}

	k, ok := convert(a.userID, mc)
	if !ok || (edit && !k.edited) {
		return nil
	}

	_, err := a.keep(ctx, e, k)

	return err
}

// keepService keeps what a service message says of its chat, as mautrix reads the same actions
// (pkg/connector/handletelegram.go:495-540): a group created, or a channel, is a chat from then on, listed at once
// rather than at its first message (MessageActionChatCreate and MessageActionChannelCreate create the portal); the
// account removed from a basic group leaves it (MessageActionChatDeleteUser of itself, selfLeaveChat). A service
// message is not kept as a message: the mirror keeps what people wrote.
func (a *Account) keepService(ctx context.Context, e tg.Entities, svc *tg.MessageService) error {
	chat := peerID(svc.PeerID)
	if chat == "" {
		return nil
	}

	switch action := svc.Action.(type) {
	case *tg.MessageActionChatCreate, *tg.MessageActionChannelCreate:
		info, ok := entityChat(e, chat)
		if !ok {
			info = fallbackChat(chat)
		}

		return a.store.keepChatAt(ctx, a.key, info, time.Unix(int64(svc.Date), 0))
	case *tg.MessageActionChatDeleteUser:
		if action.UserID == a.userID {
			return a.store.unlist(ctx, a.key, chat)
		}
	}

	return nil
}

// keep keeps a converted message in its chat, and says whether it was new.
func (a *Account) keep(ctx context.Context, e tg.Entities, k kept) (bool, error) {
	k.senderName = a.nameOf(ctx, e, k.sender)

	fallback := fallbackChat(k.chat)
	if info, ok := entityChat(e, k.chat); ok {
		if err := a.store.keepChat(ctx, a.key, info); err != nil {
			return false, err
		}

		fallback = info
	}

	return a.store.keepMessage(ctx, a.key, k, fallback)
}

// keepEntities keeps what an update's or an answer's entities say: each person's name for the messages he sends, the
// chats already kept as they are now (renamed, left), and the access hashes later requests need. A min entity's
// access hash is not one a request may use (core.telegram.org/api/min), so it is not kept.
func (a *Account) keepEntities(ctx context.Context, e tg.Entities) error {
	var errs []error

	for _, u := range e.Users {
		info := userChat(u)
		errs = append(errs, a.store.nameUser(ctx, a.key, info), a.store.refreshChat(ctx, a.key, info))

		if hash, ok := u.GetAccessHash(); ok && !u.Min {
			errs = append(errs, a.states.SetUserAccessHash(ctx, a.userID, u.ID, hash))
		}
	}

	for _, c := range e.Chats {
		info, _ := groupChat(c)
		errs = append(errs, a.store.refreshChat(ctx, a.key, info))
	}

	for _, c := range e.Channels {
		info, _ := groupChat(c)
		errs = append(errs, a.store.refreshChat(ctx, a.key, info))

		if hash, ok := c.GetAccessHash(); ok && !c.Min {
			errs = append(errs, a.states.SetChannelAccessHash(ctx, a.userID, c.ID, hash))
		}
	}

	return errors.Join(errs...)
}

// keepRaw keeps the entities of an answer (a page of history, of dialogs, a channel's gap), with the chats the account
// lost (forbidden ones, which tg.Entities leaves out) taken off the list, and gives them as tg.Entities.
func (a *Account) keepRaw(ctx context.Context, users []tg.UserClass, chats []tg.ChatClass) (tg.Entities, error) {
	e := tg.EntitiesFrom(users, chats)
	errs := []error{a.keepEntities(ctx, e)}

	for _, c := range chats {
		switch c.(type) {
		case *tg.ChatForbidden, *tg.ChannelForbidden:
			info, _ := groupChat(c)
			errs = append(errs, a.store.refreshChat(ctx, a.key, info))
		}
	}

	return e, errors.Join(errs...)
}

// entityChat is a chat as the entities say it is, when they name it.
func entityChat(e tg.Entities, chat string) (chatInfo, bool) {
	kind, id, err := parseID(chat)
	if err != nil {
		return chatInfo{}, false
	}

	switch kind {
	case peerUser:
		if u, ok := e.Users[id]; ok {
			return userChat(u), true
		}
	case peerChat:
		if c, ok := e.Chats[id]; ok {
			return groupChat(c)
		}
	case peerChannel:
		if c, ok := e.Channels[id]; ok {
			return groupChat(c)
		}
	}

	return chatInfo{}, false
}

// nameOf is a sender's name: as the entities give it, else as kept.
func (a *Account) nameOf(ctx context.Context, e tg.Entities, sender string) string {
	if info, ok := entityChat(e, sender); ok && info.name != "" {
		return info.name
	}

	return a.store.userName(ctx, a.key, sender)
}

// spawned runs a fetch the updates manager asked for, outside its goroutine, and logs how it ended.
func (a *Account) spawned(ctx context.Context, what string, err error) {
	if err != nil && !errors.Is(err, context.Canceled) {
		a.log.WarnContext(ctx, "telegram: "+what+" failed", slog.String("account", a.key), slog.String("error", err.Error()))
	}
}
