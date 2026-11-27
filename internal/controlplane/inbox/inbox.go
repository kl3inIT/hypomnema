// Package inbox reads and answers the owner's own chats for the Inbox: the message he or a person sent, given as
// evidence (Sent), and his reply sent from an item (Sender). The Inbox's state is orchestration/inboxstore, which
// calls Sent through inboxstore.Sent; the client API calls Sender for the owner's Send.
package inbox

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/kl3inIT/hypomnema/internal/controlplane/messaging"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/inboxstore"
)

// Config is what the Inbox reads and sends with: the owner's messaging accounts, by Integration and Connection
// label, his Lark and Google sign-ins and their APIs.
type Config struct {
	// Send sends to a Zalo or WhatsApp chat of an account; Messages and Members read what the server keeps of one
	// and who is in it.
	Send     func(ctx context.Context, integration, label string, out messaging.Outgoing) (messaging.Message, error)
	Messages func(ctx context.Context, integration, label string, q messaging.Query) ([]messaging.Message, error)
	Members  func(ctx context.Context, integration, label, chat string) (messaging.Members, error)
	// Chats lists the chats kept of a Zalo or WhatsApp account, the latest first; ChatReads the owner's choices of
	// which the secretary reads (issue #1270).
	Chats     func(ctx context.Context, integration, label string, limit int) ([]messaging.Chat, error)
	ChatReads func(ctx context.Context, integration, label string) ([]inboxstore.ChatRead, error)
	// LarkToken and GoogleToken are the access tokens of the owner's Lark and Google Connections, by label.
	LarkToken   func(ctx context.Context, label string) (string, error)
	GoogleToken func(ctx context.Context, label string) (string, error)
	// LarkURL is Lark's API, https://open.larksuite.com; GmailURL Gmail's, https://gmail.googleapis.com.
	LarkURL, GmailURL string
	Client            *http.Client
	Log               *slog.Logger
}

// NewSent reads the owner's chats with c's Messages, Members, LarkToken, LarkURL and Client.
func NewSent(c Config) Sent {
	return Sent{messages: c.Messages, members: c.Members, larkToken: c.LarkToken, larkURL: c.LarkURL, client: c.Client}
}

// NewSender sends the owner's replies with all of c.
func NewSender(c Config) Sender {
	return Sender{
		send: c.Send, messages: c.Messages, members: c.Members, larkToken: c.LarkToken, googleToken: c.GoogleToken,
		larkURL: c.LarkURL, gmailURL: c.GmailURL, client: c.Client, log: c.Log,
	}
}
