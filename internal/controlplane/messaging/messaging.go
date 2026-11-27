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
	// Code is what the QR code shows, valid until Expires. A service that gives its QR code as a picture (Zalo,
	// issue #657) gives Image instead, a data URL.
	Code    string
	Image   string
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
	// Media is a kept message's image or file, downloaded first when it is not kept yet (issue #634).
	Media(ctx context.Context, chat, id string) (Media, error)
	// Backfill asks the service for up to count messages of a chat older than the oldest kept, keeps them, and
	// answers how many were added and whether the start of the chat was reached (issues #634, #651).
	Backfill(ctx context.Context, chat string, count int) (Backfilled, error)
	// Send sends a message, or a file with its caption, in a chat as the account's owner, and keeps it (issue
	// #645). Whether it may be sent is decided before.
	Send(ctx context.Context, out Outgoing) (Message, error)
}

// Backfilled is what asking for older messages of a chat brought: how many were added to those kept, and whether
// the service said the chat has none older (wacli's MessagesAdded and start_of_history_reached).
type Backfilled struct {
	Added    int
	Complete bool
}

// Outgoing is a message to send: a text, or a file with Text as its caption, replying to ReplyTo when it is set.
type Outgoing struct {
	Chat     string
	Text     string
	ReplyTo  string
	File     []byte
	FileName string
	// Mentions are the members it tags, by their ids as Messages gives a sender's; Text writes @ and each one's
	// MentionUser (issue #852). WhatsApp tags by them.
	Mentions []string
	// Tags are the members it tags in a group where Text writes @ and their names (issue #1200). Zalo tags by them.
	Tags []Tag
}

// MaxChats is the most chats Chats lists: every chat of an account, for the list of which ones the secretary reads
// (issue #1270). The API and the Bots' tools page at most MaxPage.
const MaxChats = 20000

// MaxPage is the most chats or messages one page of the API or of a Bot's tool gives.
const MaxPage = 500

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
	// MediaKept says its image or file was downloaded and is kept (issue #634).
	MediaKept bool
}

// Media is a kept message's image or file, downloaded (issue #634).
type Media struct {
	// Path is the file on the control plane's disk.
	Path     string
	MimeType string
	// FileName is the name its sender gave, for a document; empty otherwise.
	FileName string
}

// Query is which kept messages to list.
type Query struct {
	// Chat keeps one chat's messages; empty is every chat.
	Chat string
	// ID keeps the message of that id, the service's own (issue #1004: the owner's message given as evidence).
	ID string
	// Search keeps the messages whose text, caption, file name, chat or sender has these words.
	Search string
	// Before keeps the messages sent before it, to page back; zero is now.
	Before time.Time
	// Limit is how many at most; zero is the service's default.
	Limit int
	// Offset skips this many of those, in the same order: pages that never skip messages sent at the same time
	// (issue #1023: WhatsApp keeps seconds, so more than a page can share one Before).
	Offset int
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
	// PhoneSync is the last sync from the owner's phone, while the session lasts; nil when none was asked.
	PhoneSync *PhoneSync
}

// MemberLister is an account that reads who is in one of its chats now (issue #1163), so that the owner's answer
// in a group where the person who asked is a member counts as his answer.
type MemberLister interface {
	// Members are the people in a chat now: a group's as the service lists them, asked of it each time; a direct
	// chat's other party.
	Members(ctx context.Context, chat string) (Members, error)
}

// Members are the people in a chat, each by every id the service knows him by, as Messages gives a sender's
// (WhatsApp: his number's JID and his LID when known); Name is the chat's, Direct says it is a direct chat.
type Members struct {
	Name   string
	Direct bool
	IDs    []string
	// People are IDs person by person, when the service knows a person by more than one id (WhatsApp); nil when each
	// id is a person of its own (issue #1200).
	People [][]string
}

// PhoneSyncer is an account whose past comes from the owner's phone, as Zalo PC's "Sync messages" brings it
// (issue #697).
type PhoneSyncer interface {
	// SyncFromPhone asks the phone for its messages; the phone shows the owner a confirmation, and State's
	// PhoneSync tells what follows.
	SyncFromPhone(ctx context.Context) error
}

// SyncStage is where a sync from the phone is.
type SyncStage string

const (
	// SyncAsking: the phone shows the owner a confirmation, for a minute.
	SyncAsking SyncStage = "asking"
	// SyncSending: the owner confirmed; the phone packs and uploads its messages.
	SyncSending SyncStage = "sending"
	// SyncImporting: the backup is downloaded and its messages are being kept.
	SyncImporting SyncStage = "importing"
	// SyncDone: the messages are kept.
	SyncDone SyncStage = "done"
	// SyncFailed: refused on the phone, not answered, or the backup could not be read; Reason says.
	SyncFailed SyncStage = "failed"
)

// PhoneSync is a sync from the phone: its stage, why it failed, what it kept, and when it got there.
type PhoneSync struct {
	Stage  SyncStage
	Reason string
	// Messages is how many messages were added, in how many Chats; Skipped counts the chats it could not read.
	Messages, Chats, Skipped int
	At                       time.Time
}

// NeedsLink says the account cannot be used until it is linked again.
func (s State) NeedsLink() bool { return s.Status == StatusLoggedOut }
