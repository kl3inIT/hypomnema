package telegram

import (
	"context"
	"log/slog"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/messaging"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
)

// self is the test account's Telegram user id.
const self = 42

// fakeSecrets is the vault's secrets, by name; a missing one is not found, as the vault answers.
type fakeSecrets map[string]string

func (f fakeSecrets) Open(_ context.Context, name string) (vault.Opened, error) {
	value, ok := f[name]
	if !ok {
		return vault.Opened{}, vault.ErrNotFound
	}

	return vault.Opened{Payload: vault.Payload{vault.KeyValue: value}}, nil
}

func openService(t *testing.T, path string, secrets Secrets) *Service {
	t.Helper()

	if path == "" {
		path = filepath.Join(t.TempDir(), "telegram.db")
	}

	svc, err := Open(t.Context(), path, secrets, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.Close() })

	return svc
}

// testAccount is an account of a fresh telegram.db that never connects, whose fetches never pause.
func testAccount(t *testing.T) *Account {
	t.Helper()

	a := openService(t, "", fakeSecrets{}).newAccount(self)
	a.pause = func(context.Context, int) error { return nil }

	return a
}

// fakeTelegram is Telegram's servers as the account asks them: every chat's messages, answered as
// messages.getHistory pages them (newest first, below offset_id, above min_id), and pages of dialogs.
type fakeTelegram struct {
	mu       sync.Mutex
	messages map[string][]*tg.Message
	users    []tg.UserClass
	chats    []tg.ChatClass
	dialogs  []tg.MessagesDialogsClass
	// asked are the history requests, by chat; dialogsAsked the dialogs requests.
	asked        map[string][]tg.MessagesGetHistoryRequest
	dialogsAsked []tg.MessagesGetDialogsRequest
	loggedOut    bool
}

func newFakeTelegram() *fakeTelegram {
	return &fakeTelegram{messages: map[string][]*tg.Message{}, asked: map[string][]tg.MessagesGetHistoryRequest{}}
}

// add puts messages with these ids in a chat, each from its peer.
func (f *fakeTelegram) add(peer tg.PeerClass, ids ...int) {
	for _, id := range ids {
		f.messages[peerID(peer)] = append(f.messages[peerID(peer)], message(peer, id, "message"))
	}
}

func inputChat(p tg.InputPeerClass) string {
	switch p := p.(type) {
	case *tg.InputPeerUser:
		return userID(p.UserID)
	case *tg.InputPeerChat:
		return chatID(p.ChatID)
	case *tg.InputPeerChannel:
		return channelID(p.ChannelID)
	case *tg.InputPeerSelf:
		return userID(self)
	}

	return ""
}

func (f *fakeTelegram) MessagesGetHistory(_ context.Context, req *tg.MessagesGetHistoryRequest,
) (tg.MessagesMessagesClass, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	chat := inputChat(req.Peer)
	f.asked[chat] = append(f.asked[chat], *req)

	all := slices.Clone(f.messages[chat])
	slices.SortFunc(all, func(a, b *tg.Message) int { return b.ID - a.ID })

	var page []tg.MessageClass

	for _, m := range all {
		if (req.OffsetID == 0 || m.ID < req.OffsetID) && m.ID > req.MinID && len(page) < req.Limit {
			page = append(page, m)
		}
	}

	return &tg.MessagesMessagesSlice{Messages: page, Count: len(all), Users: f.users, Chats: f.chats}, nil
}

func (f *fakeTelegram) MessagesGetDialogs(_ context.Context, req *tg.MessagesGetDialogsRequest,
) (tg.MessagesDialogsClass, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.dialogsAsked = append(f.dialogsAsked, *req)
	if len(f.dialogsAsked) > len(f.dialogs) {
		return &tg.MessagesDialogs{}, nil
	}

	return f.dialogs[len(f.dialogsAsked)-1], nil
}

func (f *fakeTelegram) AuthLogOut(context.Context) (*tg.AuthLoggedOut, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.loggedOut = true

	return &tg.AuthLoggedOut{}, nil
}

// message is a text message of a chat, sent at a second of its id.
func message(peer tg.PeerClass, id int, text string) *tg.Message {
	return &tg.Message{ID: id, PeerID: peer, Date: 1_700_000_000 + id, Message: text}
}

func user(id int64, first string) *tg.User {
	u := &tg.User{ID: id, FirstName: first}
	u.SetAccessHash(id * 1000)

	return u
}

func channel(id int64, title string) *tg.Channel {
	c := &tg.Channel{ID: id, Title: title, Megagroup: true}
	c.SetAccessHash(id * 1000)

	return c
}

// keptIDs are the ids of a chat's messages kept, the oldest first.
func keptIDs(t *testing.T, a *Account, chat string) []string {
	t.Helper()

	msgs, err := a.Messages(t.Context(), messaging.Query{Chat: chat, Limit: messaging.MaxPage})
	require.NoError(t, err)

	ids := make([]string, 0, len(msgs))
	for _, m := range slices.Backward(msgs) {
		ids = append(ids, m.ID)
	}

	return ids
}
