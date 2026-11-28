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
	messages map[string][]tg.MessageClass
	users    []tg.UserClass
	chats    []tg.ChatClass
	dialogs  []tg.MessagesDialogsClass
	// dialogsFail fails the dialogs request of that attempt, counted from 1, instead of answering a page, and
	// historyFail the history request of that attempt, counted over every chat.
	dialogsFail map[int]error
	historyFail map[int]error
	// dialogsBy, when set, answers each dialogs request in place of the pages of dialogs, as a server reads its
	// offsets.
	dialogsBy func(req *tg.MessagesGetDialogsRequest) tg.MessagesDialogsClass
	served    int
	// historyAsked counts the history requests of every chat.
	historyAsked int
	// asked are the history requests, by chat; dialogsAsked the dialogs requests.
	asked        map[string][]tg.MessagesGetHistoryRequest
	dialogsAsked []tg.MessagesGetDialogsRequest
	loggedOut    bool
	// tracked are the channels the account asked the updates manager to track, as TrackChannel's arguments.
	tracked [][3]int64
}

// conn is a connection to this fake, with it as the updates manager too.
func (f *fakeTelegram) conn() conn { return conn{api: f, tracker: f} }

func (f *fakeTelegram) TrackChannel(_ context.Context, channelID, accessHash int64, pts int) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.tracked = append(f.tracked, [3]int64{channelID, accessHash, int64(pts)})

	return nil
}

func newFakeTelegram() *fakeTelegram {
	return &fakeTelegram{messages: map[string][]tg.MessageClass{}, asked: map[string][]tg.MessagesGetHistoryRequest{}}
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

	f.historyAsked++
	if err := f.historyFail[f.historyAsked]; err != nil {
		return nil, err
	}

	all := slices.Clone(f.messages[chat])
	slices.SortFunc(all, func(a, b tg.MessageClass) int { return b.GetID() - a.GetID() })

	var page []tg.MessageClass

	for _, m := range all {
		if id := m.GetID(); (req.OffsetID == 0 || id < req.OffsetID) && id > req.MinID && len(page) < req.Limit {
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
	if err := f.dialogsFail[len(f.dialogsAsked)]; err != nil {
		return nil, err
	}

	if f.dialogsBy != nil {
		return f.dialogsBy(req), nil
	}

	if f.served >= len(f.dialogs) {
		return &tg.MessagesDialogs{}, nil
	}

	f.served++

	return f.dialogs[f.served-1], nil
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
