package telegram

// Which chats are listed and of what kind (issue #1184, the design's row of chat kinds): the account flags gotd gives
// a user and the flags of a channel, read as mautrix/telegram reads them (pkg/connector/userinfo.go:294 User.Bot;
// chatinfo.go:162-166 Channel.Broadcast and Megagroup). The kinds are the ones messaging.Chat already has, dm, group
// and broadcast, with bot for a bot or an official account; inboxstore's ChatKind takes broadcast as a bot already.

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/gotd/td/tg"
)

// The kinds of chat, as messaging.Chat.Kind.
const (
	kindDirect    = "dm"
	kindGroup     = "group"
	kindBroadcast = "broadcast"
	kindBot       = "bot"
)

// The peer types of a chat id, user:<id>, chat:<id> or channel:<id>, as mautrix/telegram names a portal
// (pkg/connector/ids/ids.go:139-141,186-187): Telegram's ids are unique within a peer type only.
const (
	peerUser    = "user"
	peerChat    = "chat"
	peerChannel = "channel"
)

func userID(id int64) string    { return peerUser + ":" + strconv.FormatInt(id, 10) }
func chatID(id int64) string    { return peerChat + ":" + strconv.FormatInt(id, 10) }
func channelID(id int64) string { return peerChannel + ":" + strconv.FormatInt(id, 10) }

// peerID is a peer's chat id.
func peerID(p tg.PeerClass) string {
	switch p := p.(type) {
	case *tg.PeerUser:
		return userID(p.UserID)
	case *tg.PeerChat:
		return chatID(p.ChatID)
	case *tg.PeerChannel:
		return channelID(p.ChannelID)
	}

	return ""
}

// parseID reads a chat id back into its peer type and Telegram's id.
func parseID(id string) (string, int64, error) {
	kind, raw, ok := strings.Cut(id, ":")
	if ok && (kind == peerUser || kind == peerChat || kind == peerChannel) {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
			return kind, n, nil
		}
	}

	return "", 0, fmt.Errorf("telegram: %q is not a chat id (user:<id>, chat:<id> or channel:<id>)", id)
}

// chatInfo is what a chat is, read from its entity.
type chatInfo struct {
	id, kind, name string
	// listed is false for a deleted account and for a group or channel the account left or was removed from.
	listed bool
	// min says the entity is Telegram's partial copy, whose name does not replace a known one.
	min bool
}

// userChat is a direct chat with a person: a bot, or an account Telegram marks verified, support (its own service
// notifications, 777000), scam or fake, is of kind bot; a deleted account is not listed.
func userChat(u *tg.User) chatInfo {
	c := chatInfo{id: userID(u.ID), kind: kindDirect, name: userName(u), listed: !u.Deleted, min: u.Min}
	if u.Bot || u.Verified || u.Support || u.Scam || u.Fake {
		c.kind = kindBot
	}

	return c
}

// userName is a person as people know him: his name, else his username, else his number (mautrix's
// FormatDisplayname takes the same fields, userinfo.go:285).
func userName(u *tg.User) string {
	if name := strings.TrimSpace(u.FirstName + " " + u.LastName); name != "" {
		return name
	}

	if u.Username != "" {
		return "@" + u.Username
	}

	if u.Phone != "" {
		return "+" + u.Phone
	}

	return ""
}

// groupChat is a basic group, a supergroup or a channel: a broadcast channel, or a gigagroup (a supergroup only its
// admins write in), is of kind broadcast; a megagroup and a forum are groups, a forum one chat with its topics in it;
// one the account left, or a forbidden one, is not listed. ok is false for an empty chat, which is no chat.
func groupChat(c tg.ChatClass) (chatInfo, bool) {
	switch c := c.(type) {
	case *tg.Chat:
		return chatInfo{id: chatID(c.ID), kind: kindGroup, name: c.Title, listed: !c.Left}, true
	case *tg.ChatForbidden:
		return chatInfo{id: chatID(c.ID), kind: kindGroup, name: c.Title}, true
	case *tg.Channel:
		info := chatInfo{id: channelID(c.ID), kind: kindGroup, name: c.Title, listed: !c.Left, min: c.Min}
		if c.Broadcast || c.Gigagroup {
			info.kind = kindBroadcast
		}

		return info, true
	case *tg.ChannelForbidden:
		info := chatInfo{id: channelID(c.ID), kind: kindGroup, name: c.Title}
		if c.Broadcast {
			info.kind = kindBroadcast
		}

		return info, true
	}

	return chatInfo{}, false
}

// fallbackChat is what a chat is taken for until its entity is seen: Telegram's short updates name a sender by his id
// alone. Only a chat not kept yet takes it.
func fallbackChat(id string) chatInfo {
	kind := kindGroup
	if strings.HasPrefix(id, peerUser+":") {
		kind = kindDirect
	}

	return chatInfo{id: id, kind: kind, listed: true}
}
