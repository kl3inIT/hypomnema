package telegram

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram/updates"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/messaging"
)

func keptText(id int, text string) kept {
	return kept{chat: "user:7", id: id, sender: "user:7", at: time.Unix(1_700_000_000+int64(id), 0), text: text}
}

// A message is kept once however often Telegram sends it (live, then in a backfill), its edit replaces its text, and
// a deleted one keeps no text, as whatsapp.db keeps them.
func TestTheMirrorKeepsAMessageOnceWithItsEditAndDeletion(t *testing.T) {
	t.Parallel()

	a := testAccount(t)
	ctx := t.Context()
	dm := chatInfo{id: "user:7", kind: kindDirect, name: "Trung", listed: true}

	added, err := a.store.keepMessage(ctx, a.key, keptText(1, "hello"), dm)
	require.NoError(t, err)
	assert.True(t, added)

	added, err = a.store.keepMessage(ctx, a.key, keptText(1, "hello"), dm)
	require.NoError(t, err)
	assert.False(t, added, "a message kept twice is kept once")

	edit := keptText(1, "hello there")
	edit.edited = true
	_, err = a.store.keepMessage(ctx, a.key, edit, dm)
	require.NoError(t, err)

	msgs, err := a.Messages(ctx, messaging.Query{Chat: "user:7"})
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.Equal(t, "hello there", msgs[0].Text)
	assert.True(t, msgs[0].Edited)
	assert.Equal(t, "Trung", msgs[0].ChatName)

	require.NoError(t, a.store.revoke(ctx, a.key, "", []int{1}))

	msgs, err = a.Messages(ctx, messaging.Query{Chat: "user:7"})
	require.NoError(t, err)
	assert.True(t, msgs[0].Revoked)
	assert.Empty(t, msgs[0].Text)
}

// Telegram names a deleted message of a direct chat or a basic group by its id alone, unique in the account, and a
// channel's by the channel: the same id in a channel is another message.
func TestADeletionWithoutAChatLeavesTheChannelsAlone(t *testing.T) {
	t.Parallel()

	a := testAccount(t)
	ctx := t.Context()

	for _, chat := range []string{"user:7", "chat:8", "channel:9"} {
		k := keptText(5, "same id")
		k.chat = chat
		_, err := a.store.keepMessage(ctx, a.key, k, fallbackChat(chat))
		require.NoError(t, err)
	}

	require.NoError(t, a.store.revoke(ctx, a.key, "", []int{5}))

	msgs, err := a.Messages(ctx, messaging.Query{})
	require.NoError(t, err)

	revoked := map[string]bool{}
	for _, m := range msgs {
		revoked[m.Chat] = m.Revoked
	}

	assert.Equal(t, map[string]bool{"user:7": true, "chat:8": true, "channel:9": false}, revoked)
}

// The chats are listed the latest first, without those the account left or a deleted account's; a search reads the
// text, the chat's name and the sender's.
func TestTheMirrorListsChatsAndSearchesMessages(t *testing.T) {
	t.Parallel()

	a := testAccount(t)
	ctx := t.Context()

	keep := func(chat chatInfo, id int, sender, text string) {
		t.Helper()
		require.NoError(t, a.store.keepChat(ctx, a.key, chat))
		k := kept{chat: chat.id, id: id, sender: "user:7", senderName: sender, text: text,
			at: time.Unix(1_700_000_000+int64(id), 0)}
		_, err := a.store.keepMessage(ctx, a.key, k, chat)
		require.NoError(t, err)
	}

	keep(chatInfo{id: "user:7", kind: kindDirect, name: "Trung", listed: true}, 1, "Trung", "the FAQ tool sync")
	keep(chatInfo{id: "channel:9", kind: kindGroup, name: "VMAP AI", listed: true}, 2, "Laura", "standup at ten")
	keep(chatInfo{id: "chat:8", kind: kindGroup, name: "Old team", listed: true}, 3, "Huy", "bye")
	require.NoError(t, a.store.keepChat(ctx, a.key, chatInfo{id: "chat:8", kind: kindGroup, name: "Old team"}))

	chats, err := a.Chats(ctx, 0)
	require.NoError(t, err)
	require.Len(t, chats, 2, "the chat the account left is not listed")
	assert.Equal(t, "channel:9", chats[0].ID)
	assert.Equal(t, "VMAP AI", chats[0].Name)
	assert.Equal(t, kindGroup, chats[0].Kind)
	assert.Equal(t, time.Unix(1_700_000_002, 0).UTC(), chats[0].LastMessageAt)

	for search, want := range map[string]string{"faq": "user:7", "Laura": "channel:9", "VMAP": "channel:9"} {
		msgs, err := a.Messages(ctx, messaging.Query{Search: search})
		require.NoError(t, err)
		require.Len(t, msgs, 1, search)
		assert.Equal(t, want, msgs[0].Chat, search)
	}

	msgs, err := a.Messages(ctx, messaging.Query{ID: "2"})
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.Equal(t, "standup at ten", msgs[0].Text)
}

// A min entity, Telegram's partial copy of a person or a channel, never replaces the name kept with its own.
func TestAMinEntityKeepsTheKnownName(t *testing.T) {
	t.Parallel()

	a := testAccount(t)
	ctx := t.Context()

	require.NoError(t, a.store.keepChat(ctx, a.key, chatInfo{id: "user:7", kind: kindDirect, name: "Trung", listed: true}))
	require.NoError(t, a.store.refreshChat(ctx, a.key,
		chatInfo{id: "user:7", kind: kindDirect, name: "T.", listed: true, min: true}))

	chats, err := a.Chats(ctx, 0)
	require.NoError(t, err)
	require.Len(t, chats, 1)
	assert.Equal(t, "Trung", chats[0].Name)
}

// gotd's session is kept in telegram.db and read again after the control plane restarts; an account never linked
// has none, which gotd takes as a login to make.
func TestTheSessionOutlivesARestart(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "telegram.db")
	svc := openService(t, path, fakeSecrets{})

	storage := sessionStorage{store: svc.store, account: "42"}
	_, err := storage.LoadSession(t.Context())
	require.ErrorIs(t, err, session.ErrNotFound)

	require.NoError(t, storage.StoreSession(t.Context(), []byte(`{"Version":1}`)))
	require.NoError(t, storage.StoreSession(t.Context(), []byte(`{"Version":1,"Data":{}}`)))
	require.NoError(t, svc.Close())

	again := openService(t, path, fakeSecrets{})
	data, err := sessionStorage{store: again.store, account: "42"}.LoadSession(t.Context())
	require.NoError(t, err)
	assert.JSONEq(t, `{"Version":1,"Data":{}}`, string(data))
}

// The update state follows gotd's storages (updates/storage.go, storage_file.go): a part of a state not kept is
// refused, a new state forgets the channels' pts, and the access hashes are kept by kind.
func TestTheUpdateStateFollowsGotdsStorages(t *testing.T) {
	t.Parallel()

	st := openService(t, "", fakeSecrets{}).states
	ctx := t.Context()

	require.ErrorIs(t, st.SetPts(ctx, self, 10), errNoState)

	_, found, err := st.GetState(ctx, self)
	require.NoError(t, err)
	assert.False(t, found)

	require.NoError(t, st.SetState(ctx, self, updates.State{Pts: 1, Qts: 2, Date: 3, Seq: 4}))
	require.NoError(t, st.SetPts(ctx, self, 11))
	require.NoError(t, st.SetQts(ctx, self, 12))
	require.NoError(t, st.SetDateSeq(ctx, self, 13, 14))

	got, found, err := st.GetState(ctx, self)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, updates.State{Pts: 11, Qts: 12, Date: 13, Seq: 14}, got)

	require.NoError(t, st.SetChannelPts(ctx, self, 100, 5))
	require.NoError(t, st.SetChannelPts(ctx, self, 101, 6))
	require.NoError(t, st.SetChannelPts(ctx, self, 100, 7))

	seen := map[int64]int{}
	require.NoError(t, st.ForEachChannels(ctx, self, func(_ context.Context, id int64, pts int) error {
		seen[id] = pts
		// gotd may write while it reads the channels.
		return st.SetChannelPts(ctx, self, id, pts+1)
	}))
	assert.Equal(t, map[int64]int{100: 7, 101: 6}, seen)

	stop := errors.New("stop")
	require.ErrorIs(t, st.ForEachChannels(ctx, self, func(context.Context, int64, int) error { return stop }), stop)

	require.NoError(t, st.SetState(ctx, self, updates.State{Pts: 20}))

	_, found, err = st.GetChannelPts(ctx, self, 100)
	require.NoError(t, err)
	assert.False(t, found, "a new state forgets the channels' pts")

	require.NoError(t, st.SetChannelAccessHash(ctx, self, 100, 555))
	require.NoError(t, st.SetUserAccessHash(ctx, self, 100, 666))

	hash, found, err := st.GetChannelAccessHash(ctx, self, 100)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, int64(555), hash)

	hash, found, err = st.GetUserAccessHash(ctx, self, 100)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, int64(666), hash)

	_, found, err = st.GetUserAccessHash(ctx, self+1, 100)
	require.NoError(t, err)
	assert.False(t, found, "another account's hashes are its own")
}
