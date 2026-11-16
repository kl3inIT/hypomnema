// Package messaging is a messaging account the owner linked as himself (issue #612): his WhatsApp first, then his
// Zalo (#613), so that the main Bot reads and writes in his chats with his rights, as a Lark user Connection lets it
// act as him in Lark (issue #468).
//
// A Service is one messaging service: it links a new account and opens one that was linked. An Account is that
// account's live session, held by the control plane for as long as it runs. Each service implements both
// (messaging/whatsapp); what is shared (which Connection an account is, the sign-in that links it, the web page) is
// the Integration's (internal/controlplane/integration, device.go). The shape follows the Channel of
// internal/controlplane/channels and MemoryOS's WhatsApp bridge (memoryos-integration-whatsapp/bridge/bots.go: open,
// pair, handle). Steps 2 to 4 of #612 add the chats, the messages, sending and media.
package messaging

import (
	"context"
	"time"
)

// Service is one messaging service, as the control plane links and opens its accounts.
type Service interface {
	// Link starts linking a new account. The steps are the codes to show, each until it expires, then one last step
	// with the account linked or the error that ended the linking; the channel is closed after it. Canceling ctx
	// ends a linking that has not finished; an account linked lives on until it is closed.
	Link(ctx context.Context) (<-chan LinkStep, error)
	// Open opens the session of an account linked before, by the id its linking gave, and connects it. An account
	// the service has forgotten (logged out from the phone) opens in StatusLoggedOut.
	Open(ctx context.Context, id string) (Account, error)
}

// LinkStep is one step of a linking: a code to show, or its end.
type LinkStep struct {
	// Code is what the QR code shows, valid until Expires.
	Code    string
	Expires time.Time
	// Account is the account linked, on the last step of a linking that succeeded.
	Account Account
	// Err ended the linking, on the last step of one that did not.
	Err error
}

// Account is a linked account's live session.
type Account interface {
	// ID is what Open takes to open the account again: for WhatsApp the device's JID.
	ID() string
	// Name is the account as people know it: for WhatsApp the number, +84….
	Name() string
	// State is the session's state now.
	State() State
	// Logout ends the link at the service (the phone's linked devices drop it) and forgets the session.
	Logout(ctx context.Context) error
	// Close disconnects the session and keeps it, to be opened again.
	Close()
	// Chats lists the account's chats kept, the latest first (issue #631).
	Chats(ctx context.Context, limit int) ([]Chat, error)
	// Messages lists kept messages, the latest first: of one chat, or matching a search, or both.
	Messages(ctx context.Context, q Query) ([]Message, error)
}

// Chat is one chat of the account, as kept.
type Chat struct {
	// ID is the service's own id of the chat: for WhatsApp its JID.
	ID string
	// Kind is dm, group or broadcast.
	Kind string
	Name string
	// LastMessageAt is when its latest kept message was sent.
	LastMessageAt time.Time
}

// Message is one message as kept. Every chat's messages are kept as they arrive, with the history the service
// sends after a link: a personal account has no server to ask for past ones (owner, 2026-11-16, #631).
type Message struct {
	ID       string
	Chat     string
	ChatName string
	// Sender is the person's id, his number's JID when known; SenderName his name as last seen.
	Sender     string
	SenderName string
	At         time.Time
	FromMe     bool
	Text       string
	// QuotedID is the message it replies to.
	QuotedID string
	// MediaType is image, video, audio, document or sticker; Caption, FileName and MimeType describe it.
	MediaType string
	Caption   string
	FileName  string
	MimeType  string
	Edited    bool
	Revoked   bool
}

// Query is which kept messages to list.
type Query struct {
	// Chat keeps one chat's messages; empty is every chat.
	Chat string
	// Search keeps the messages whose text, caption, file name, chat or sender has these words.
	Search string
	// Before keeps the messages sent before it, to page back; zero is now.
	Before time.Time
	// Limit is how many at most; zero is the service's default.
	Limit int
}

// Status is where a session is.
type Status string

// The states of a session, as MemoryOS's bridge reports them (bots.go, handle) and its runbook explains them
// (docs/runbooks/whatsapp-bot.md, "When it stops").
const (
	// StatusConnecting: the session is being opened.
	StatusConnecting Status = "connecting"
	// StatusConnected: the session is open.
	StatusConnected Status = "connected"
	// StatusDisconnected: the connection dropped; it is opened again by itself.
	StatusDisconnected Status = "disconnected"
	// StatusLoggedOut: the link was ended, on the phone or by the service; the account must be linked again.
	StatusLoggedOut Status = "logged_out"
	// StatusBanned: the service banned the account for a while, until State.Until when it says.
	StatusBanned Status = "banned"
	// StatusReplaced: another client opened the same session.
	StatusReplaced Status = "replaced"
	// StatusOutdated: the service refuses this client's version; the library must be updated.
	StatusOutdated Status = "outdated"
)

// State is a session's state, why, and until when.
type State struct {
	Status Status
	// Reason is the service's own words for a ban or a logout, when it gave some.
	Reason string
	// Until is when a ban ends, when the service said.
	Until time.Time
}

// NeedsLink says the account cannot be used until it is linked again.
func (s State) NeedsLink() bool { return s.Status == StatusLoggedOut }
