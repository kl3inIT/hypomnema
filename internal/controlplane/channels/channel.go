package channels

import (
	"context"
	"fmt"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/channelstore"
)

// Kind is the messaging service a Channel is an account at: the key a
// Channel's implementation is found by.
type Kind = channelstore.ChannelKind

// Channel is one kind of messaging service, as the runtime sees it
// (MemoryOS, ChannelAdapter; OpenClaw's ChannelPlugin): its key, what its
// chats can do, and how a Channel of that kind connects. The rules, the
// chats, the turns and the replies are the runtime's; the service's own
// transport, ids and shape of a message are the implementation's
// (channels/lark is LarkChannel).
type Channel interface {
	Kind() Kind
	Capabilities() Capabilities
	// Open makes a Channel's connection from what the owner set up, with
	// its account's secret, opened from the vault for this call only.
	Open(ctx context.Context, channel channelstore.Channel, secret string) (Conn, error)
}

// Capabilities are what a kind's chats can do, as its implementation states
// them, so that the runtime decides rather than the implementation hiding it
// (MemoryOS, ChannelFeatures; OpenClaw's capabilities value).
type Capabilities struct {
	// MaxText is the most characters one message shows: a longer reply goes
	// on in further messages (MemoryOS maxText, OpenClaw textChunkLimit).
	MaxText int
	// Platform is the service's name as its members know it, which the
	// notices and the label of a message name (MemoryOS,
	// ChannelProvider.platform).
	Platform string
}

// Conn is what a Channel does at its service as its account, once open.
type Conn interface {
	// Listen opens the connection and gives what comes over it to on. It
	// returns when the connection cannot be opened; an open connection may
	// stay for the life of the process.
	Listen(ctx context.Context, on Events) error
	// Self reads the account's own id, which a mention of it carries.
	Self(ctx context.Context) (string, error)
	// Groups lists the groups the account is in, the whole list or an error.
	Groups(ctx context.Context) ([]Group, error)
	// Notice sends a short line to a chat. Here and in SendFile and
	// OpenReply, a chat may be named by a person's id, which is their direct
	// chat with the account.
	Notice(ctx context.Context, chatID, text string) error
	// SendFile sends a file to a chat.
	SendFile(ctx context.Context, chatID string, a Attachment) error
	// OpenReply sends a reply that is still being written and returns it and
	// the id of the chat the service put it in. With replyTo it answers
	// that message, which the service shows quoted where it can.
	OpenReply(ctx context.Context, chatID, replyTo, text string) (reply Reply, sentTo string, err error)
	// SendQuestion sends a card to a person: a question with its choices,
	// or, with none, what the owner is told.
	SendQuestion(ctx context.Context, personID string, card Card) (QuestionRef, error)
	// ReplaceQuestion replaces a card SendQuestion sent and returns what
	// names it after.
	ReplaceQuestion(ctx context.Context, ref QuestionRef, card Card) (QuestionRef, error)
	// Mark puts a mark on a message and returns what takes it off.
	Mark(ctx context.Context, messageID string, mark Mark) (unmark func(context.Context) error, err error)
	// PersonByEmail finds a member of the account's organization by email.
	PersonByEmail(ctx context.Context, email string) (string, error)
	// NameOf reads a person's name.
	NameOf(ctx context.Context, personID string) (string, error)
}

// Reply is a reply being written. Its text is replaced as more is written,
// and set once more when it ends; how a change is counted is the service's.
type Reply interface {
	Update(ctx context.Context, text string) error
	Close(ctx context.Context, text string) error
}

// QuestionRef names a card SendQuestion sent. The runtime keeps it and hands
// it back; what Seq counts is the service's, and a card just sent has none.
type QuestionRef struct {
	// ID is the card's id; MessageID the message it is, which a reply to it
	// names.
	ID        string
	MessageID string
	Seq       int
}

// Mark is what a mark on a person's message says, as OpenClaw and Hermes
// mark one: the Bot works on it, or it failed. Nothing marks success: the
// reply does. How a mark looks is the service's.
type Mark string

// The marks.
const (
	MarkWorking Mark = "working"
	MarkFailed  Mark = "failed"
)

// Message is a message a person sent, as a Channel needs it.
type Message struct {
	// ID is the message's id at the service; ChatID the chat it is in.
	ID     string
	ChatID string
	// Direct says the chat is the person's direct chat with the account.
	Direct bool
	// Text is the text of a text message; IsText says it is one.
	Text   string
	IsText bool
	// Sender is the sender's id; FromPerson says a person sent it, not an
	// app.
	Sender     string
	FromPerson bool
	// SenderOrg and Org are the organization of the sender and of the
	// account (MemoryOS, ChannelFeatures).
	SenderOrg string
	Org       string
	// ReplyTo is the message this one replies to, when it is a reply.
	ReplyTo string
	// Mentions are the people and accounts the message mentions; Text holds
	// each as its Key.
	Mentions []Mention
	// Menu says the message is a press of the account's menu, for which the
	// service holds no message to mark or to reply to.
	Menu bool
}

// Mention is one mention in a message: the placeholder the service writes
// for it in the text, and who it names. A mention of everyone names nobody.
type Mention struct {
	Key      string
	PersonID string
	Name     string
}

// Group is a group the Channel's account is in.
type Group struct {
	ChatID string
	Name   string
}

// Events are what a Channel is given from its connection: each message, each
// press of a card's button, whose answer is shown to who pressed, each press
// of the account's menu, and the account being added to a group or leaving
// one.
type Events struct {
	Message func(Message)
	Press   func(Press) string
	Menu    func(MenuPress)
	Joined  func(Group)
	Left    func(chatID string)
}

// MenuPress is a person pressing an item of the account's menu, in their
// direct chat with it.
type MenuPress struct {
	// Operator is the id of who pressed; Key the item's key, as the owner
	// set it at the service.
	Operator string
	Key      string
	// At is when it was pressed, which tells a press from the service's
	// repeated delivery of it; Org is the account's organization.
	At  string
	Org string
}

// Press is a person pressing a button of a card.
type Press struct {
	// Operator is the id of who pressed.
	Operator string
	// Value is what the button carries.
	Value map[string]string
}

// Card is a card that is not being written: a question with its choices, how
// it ended, or what the owner is told.
type Card struct {
	Title string
	Tone  Tone
	// Text is Markdown.
	Text string
	// Buttons are the choices; none on a card that can no longer be answered.
	Buttons []Button
	// Note is small print under the buttons.
	Note string
}

// Tone is what a card's look says; how it looks is the service's.
type Tone string

// The tones.
const (
	// ToneAsking is a question that waits.
	ToneAsking Tone = "asking"
	// ToneAnswered is a question that was answered.
	ToneAnswered Tone = "answered"
	// ToneAlert is a question that was canceled, or something gone wrong.
	ToneAlert Tone = "alert"
	// ToneClosed is a question that ended otherwise.
	ToneClosed Tone = "closed"
)

// Button is one choice of a card.
type Button struct {
	Label string
	Value map[string]string
}

// Attachment is a file sent into a chat: its name as the chat shows it, and
// its bytes.
type Attachment struct {
	Name string
	Data []byte
}

// Registry finds a kind's Channel. It is made once at startup, which fails
// on a kind given twice (docs/conventions.md, Strategy behind a registry;
// MemoryOS ChannelAdapters; OpenClaw's registrar refuses a channel
// registered twice).
type Registry struct {
	kinds map[Kind]Channel
}

// NewRegistry returns the Registry of these Channels, or an error when one
// has no kind or two have the same.
func NewRegistry(channels ...Channel) (Registry, error) {
	r := Registry{kinds: make(map[Kind]Channel, len(channels))}

	for _, c := range channels {
		if c == nil || c.Kind() == "" {
			return Registry{}, fmt.Errorf("a channel has no kind")
		}

		if _, taken := r.kinds[c.Kind()]; taken {
			return Registry{}, fmt.Errorf("channel kind %s is registered twice", c.Kind())
		}

		if c.Capabilities().MaxText < 1 {
			return Registry{}, fmt.Errorf("channel kind %s shows no text: its MaxText is %d", c.Kind(),
				c.Capabilities().MaxText)
		}

		r.kinds[c.Kind()] = c
	}

	return r, nil
}

// Lookup returns the Channel of a kind, or an error when none is registered.
func (r Registry) Lookup(kind Kind) (Channel, error) {
	c, ok := r.kinds[kind]
	if !ok {
		return nil, fmt.Errorf("no channel of kind %q is registered", kind)
	}

	return c, nil
}
