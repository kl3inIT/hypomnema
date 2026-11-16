package whatsapp

// Keeping every chat as WhatsApp sends it (issue #631): each message as it arrives, the owner's own from his other
// devices included, and the history sent after a link, as wacli's sync does (docs/sync.md, internal/app
// sync_events.go and bootstrap.go). A person's LID is kept as his number when whatsmeow knows it, as wacli's
// lid_migration.go and MemoryOS's bridge (bots.go, convert) do, so that his chats and messages are found under one
// id.

import (
	"context"
	"log/slog"
	"time"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/kl3inIT/hypomnema/internal/controlplane/messaging"
)

// keepTimeout bounds one write of the mirror from an event.
const keepTimeout = 30 * time.Second

// mirrorKey is the account the mirror keeps under: the number, which a link again keeps.
func (a *account) mirrorKey() string {
	if id := a.client.Store.ID; id != nil {
		return id.User
	}

	return ""
}

func (a *account) Chats(ctx context.Context, limit int) ([]messaging.Chat, error) {
	return a.mirror.chats(ctx, a.mirrorKey(), limit)
}

func (a *account) Messages(ctx context.Context, q messaging.Query) ([]messaging.Message, error) {
	return a.mirror.messages(ctx, a.mirrorKey(), q)
}

func (a *account) forgetMirror(ctx context.Context) {
	if key := a.mirrorKey(); key != "" {
		if err := a.mirror.forget(ctx, key); err != nil {
			a.log.WarnContext(ctx, "whatsapp: what was kept of the account could not be removed",
				slog.String("error", err.Error()))
		}
	}
}

func (a *account) keepLive(e *events.Message) {
	ctx, cancel := context.WithTimeout(context.Background(), keepTimeout)
	defer cancel()

	a.keep(ctx, parseLive(e))
}

func (a *account) keepHistory(e *events.HistorySync) {
	ctx, cancel := context.WithTimeout(context.Background(), keepTimeout*10)
	defer cancel()

	for _, conv := range e.Data.GetConversations() {
		chat, err := types.ParseJID(conv.GetID())
		if err != nil {
			continue
		}

		if chat.Server == types.GroupServer {
			a.nameGroup(chat, conv.GetName())
		}

		for _, m := range conv.GetMessages() {
			if web := m.GetMessage(); web != nil {
				a.keep(ctx, parseHistory(chat, web))
			}
		}
	}
}

// keep stores one parsed message under the account, its chat and sender resolved to numbers and named.
func (a *account) keep(ctx context.Context, p parsed) {
	key := a.mirrorKey()
	if key == "" || p.id == "" || !p.hasContent() {
		return
	}

	p.chat = a.toNumber(ctx, p.chat)
	if sender, err := types.ParseJID(p.sender); err == nil && !sender.IsEmpty() {
		p.sender = a.toNumber(ctx, sender).String()
	}

	if p.fromMe {
		p.sender = a.toNumber(ctx, *a.client.Store.ID).String()
	}

	k := kept{parsed: p, account: key, chatKind: kindOf(p.chat), chatName: a.chatName(ctx, p)}
	if err := a.mirror.keep(ctx, k); err != nil {
		a.log.WarnContext(ctx, "whatsapp: a message could not be kept", slog.String("error", err.Error()))
	}
}

// toNumber is a person's id as his number when whatsmeow knows it, without the device.
func (a *account) toNumber(ctx context.Context, jid types.JID) types.JID {
	jid = jid.ToNonAD()
	if jid.Server != types.HiddenUserServer {
		return jid
	}

	if pn, err := a.client.Store.LIDs.GetPNForLID(ctx, jid); err == nil && !pn.IsEmpty() {
		return pn.ToNonAD()
	}

	return jid
}

func kindOf(chat types.JID) string {
	switch chat.Server {
	case types.GroupServer:
		return "group"
	case types.BroadcastServer:
		return "broadcast"
	}

	return "dm"
}

// chatName is a chat's name: a group's as kept, a person's as the owner's contacts or he himself name him.
func (a *account) chatName(ctx context.Context, p parsed) string {
	switch kindOf(p.chat) {
	case "group":
		return a.mirror.groupName(ctx, a.mirrorKey(), p.chat.String())
	case "broadcast":
		return ""
	}

	if c, err := a.client.Store.Contacts.GetContact(ctx, p.chat); err == nil && c.Found {
		for _, name := range []string{c.FullName, c.FirstName, c.PushName, c.BusinessName} {
			if name != "" {
				return name
			}
		}
	}

	if !p.fromMe && p.pushName != "" {
		return p.pushName
	}

	return phoneOf(p.chat)
}

func (a *account) nameGroup(jid types.JID, name string) {
	ctx, cancel := context.WithTimeout(context.Background(), keepTimeout)
	defer cancel()

	if err := a.mirror.nameGroup(ctx, a.mirrorKey(), jid.String(), name); err != nil {
		a.log.WarnContext(ctx, "whatsapp: a group's name could not be kept", slog.String("error", err.Error()))
	}
}

// nameGroups keeps the name of every group the account is in, as WhatsApp lists them.
func (a *account) nameGroups() {
	ctx, cancel := context.WithTimeout(context.Background(), keepTimeout)
	defer cancel()

	groups, err := a.client.GetJoinedGroups(ctx)
	if err != nil {
		a.log.WarnContext(ctx, "whatsapp: the groups could not be listed", slog.String("error", err.Error()))

		return
	}

	for _, g := range groups {
		a.nameGroup(g.JID, g.Name)
	}
}
