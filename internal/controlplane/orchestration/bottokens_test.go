package orchestration

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
)

// Scenario: the supervisor starts pi for a chat (issue #260). Its token says
// the chat and the Bot and acts as the coordinator token it is made under;
// it ends when pi ends, when that coordinator token is revoked, and when the
// control plane starts again, since it is kept in memory only.
func TestABotTokenIsBoundToItsChatAndEndsWithItsPi(t *testing.T) {
	t.Parallel()

	s, db, clock := newTestService(t)
	ctx := t.Context()

	parent, _, err := s.AddToken(ctx, "pi-coordinator", bearertoken.Coordinator)
	require.NoError(t, err)

	chat, err := s.CreateChat(ctx, "", "")
	require.NoError(t, err)

	secret, err := s.AddBotToken(ctx, "pi-coordinator", chat.ID, DefaultBot)
	require.NoError(t, err)

	kind, err := bearertoken.Parse(secret)
	require.NoError(t, err)
	assert.Equal(t, bearertoken.Bot, kind)

	got, err := s.AuthenticateToken(ctx, secret)
	require.NoError(t, err)
	assert.Equal(t, Token{
		ID: parent.ID, Name: "pi-coordinator", Kind: bearertoken.Bot, CreatedAt: clock.Now(), Chat: chat.ID,
		Bot: DefaultBot,
	}, got)

	restarted := New(db, clock, s.newID)
	_, err = restarted.AuthenticateToken(ctx, secret)
	require.ErrorIs(t, err, ErrNotFound, "no pi outlives the control plane, nor its token")

	s.RevokeBotToken(secret)
	s.RevokeBotToken(secret)

	_, err = s.AuthenticateToken(ctx, secret)
	require.ErrorIs(t, err, ErrNotFound, "the token ends with its pi")

	again, err := s.AddBotToken(ctx, "pi-coordinator", chat.ID, DefaultBot)
	require.NoError(t, err)
	require.NoError(t, s.RevokeToken(ctx, parent.ID))

	_, err = s.AuthenticateToken(ctx, again)
	require.ErrorIs(t, err, ErrNotFound, "a revoked coordinator token ends the tokens made under it")
}

// A token is made only under a working coordinator token, and only for the
// Bot the chat talks with.
func TestABotTokenNeedsItsCoordinatorAndItsChatsBot(t *testing.T) {
	t.Parallel()

	s, _, _ := newTestService(t)
	ctx := t.Context()

	chat, err := s.CreateChat(ctx, "", "")
	require.NoError(t, err)

	_, err = s.AddBotToken(ctx, "pi-coordinator", chat.ID, DefaultBot)
	require.ErrorIs(t, err, ErrNotFound, "no such coordinator token")

	_, _, err = s.AddToken(ctx, "web", bearertoken.Owner)
	require.NoError(t, err)

	_, err = s.AddBotToken(ctx, "web", chat.ID, DefaultBot)
	require.ErrorIs(t, err, ErrInvalid, "an owner's token is not a coordinator's")

	_, _, err = s.AddToken(ctx, "pi-coordinator", bearertoken.Coordinator)
	require.NoError(t, err)

	_, err = s.AddBotToken(ctx, "pi-coordinator", chat.ID, "vmap")
	require.ErrorIs(t, err, ErrInvalid, "the chat talks with another Bot")

	_, err = s.AddBotToken(ctx, "pi-coordinator", "cht_missing", DefaultBot)
	require.ErrorIs(t, err, ErrNotFound)
}
