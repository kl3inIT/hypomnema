package telegram

import (
	"testing"

	"github.com/gotd/td/tg"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The design's row of chat kinds (issue #1184): a person is a direct chat; a bot, an account Telegram marks verified,
// support, scam or fake is a bot; a deleted account is not listed.
func TestAPersonsKindFollowsTheAccountFlags(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		user   tg.User
		kind   string
		listed bool
	}{
		"a person":                 {tg.User{ID: 1, FirstName: "Trung"}, kindDirect, true},
		"a bot":                    {tg.User{ID: 2, Bot: true}, kindBot, true},
		"a verified account":       {tg.User{ID: 3, Verified: true}, kindBot, true},
		"Telegram's notifications": {tg.User{ID: 777000, Support: true}, kindBot, true},
		"a scam account":           {tg.User{ID: 4, Scam: true}, kindBot, true},
		"a fake account":           {tg.User{ID: 5, Fake: true}, kindBot, true},
		"a deleted account":        {tg.User{ID: 6, Deleted: true}, kindDirect, false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			c := userChat(&tc.user)
			assert.Equal(t, userID(tc.user.ID), c.id)
			assert.Equal(t, tc.kind, c.kind)
			assert.Equal(t, tc.listed, c.listed)
		})
	}
}

// Basic groups, megagroups and forums are groups, a forum one chat; broadcast channels and gigagroups are broadcasts,
// which the Inbox takes as bots; a chat the account left or lost is not listed.
func TestAGroupsKindFollowsTheChannelFlags(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		chat   tg.ChatClass
		id     string
		kind   string
		listed bool
	}{
		"a basic group":        {&tg.Chat{ID: 1, Title: "Team"}, "chat:1", kindGroup, true},
		"a basic group left":   {&tg.Chat{ID: 1, Left: true}, "chat:1", kindGroup, false},
		"a forbidden group":    {&tg.ChatForbidden{ID: 1}, "chat:1", kindGroup, false},
		"a megagroup":          {&tg.Channel{ID: 2, Megagroup: true}, "channel:2", kindGroup, true},
		"a forum":              {&tg.Channel{ID: 3, Megagroup: true, Forum: true}, "channel:3", kindGroup, true},
		"a broadcast channel":  {&tg.Channel{ID: 4, Broadcast: true}, "channel:4", kindBroadcast, true},
		"a gigagroup":          {&tg.Channel{ID: 5, Megagroup: true, Gigagroup: true}, "channel:5", kindBroadcast, true},
		"a megagroup left":     {&tg.Channel{ID: 6, Megagroup: true, Left: true}, "channel:6", kindGroup, false},
		"a forbidden channel":  {&tg.ChannelForbidden{ID: 7, Broadcast: true}, "channel:7", kindBroadcast, false},
		"a forbidden supergrp": {&tg.ChannelForbidden{ID: 8, Megagroup: true}, "channel:8", kindGroup, false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			c, ok := groupChat(tc.chat)
			require.True(t, ok)
			assert.Equal(t, tc.id, c.id)
			assert.Equal(t, tc.kind, c.kind)
			assert.Equal(t, tc.listed, c.listed)
		})
	}

	_, ok := groupChat(&tg.ChatEmpty{ID: 9})
	assert.False(t, ok, "an empty chat is no chat")
}

// A chat id is its peer type and Telegram's id, which is unique within a type only (mautrix ids.go:186).
func TestAChatIDNamesItsPeerType(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "user:7", peerID(&tg.PeerUser{UserID: 7}))
	assert.Equal(t, "chat:7", peerID(&tg.PeerChat{ChatID: 7}))
	assert.Equal(t, "channel:7", peerID(&tg.PeerChannel{ChannelID: 7}))

	kind, id, err := parseID("channel:1001")
	require.NoError(t, err)
	assert.Equal(t, peerChannel, kind)
	assert.Equal(t, int64(1001), id)

	for _, bad := range []string{"7", "group:7", "user:", "user:x"} {
		_, _, err := parseID(bad)
		assert.Error(t, err, bad)
	}
}
