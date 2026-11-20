package controlplane

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

// Scenario (issue #996): the supervisor's chat key, made as the control plane wires it, names the coordinator token
// pi runs under; the owner revokes that token and the key is refused. Its pi's end revokes a key with its reason.
func TestAChatKeyEndsWithTheCoordinatorTokenItsPiRunsUnder(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	db, err := sqlite.Open(ctx, sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	svc := orchestration.New(db, platform.SystemClock{}, platform.NewID,
		orchestration.WithGatewayKeys(modelgateway.Revocations{}))
	gateway := modelgateway.New(db, platform.SystemClock{}, platform.NewID, nil, nil)
	keys := chatKeys{gateway: gateway, chats: svc, coordinator: "pi"}

	coordinator, _, err := svc.AddToken(ctx, "pi", bearertoken.Coordinator)
	require.NoError(t, err)
	chat, err := svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	ended, _, err := keys.AddChatKey(ctx, chat.ID, "")
	require.NoError(t, err)
	require.NoError(t, keys.RevokeKey(ctx, ended))

	all, err := gateway.ListKeys(ctx)
	require.NoError(t, err)
	require.Len(t, all, 1)
	assert.Equal(t, ended, all[0].ID)
	assert.Equal(t, bearertoken.RevokedProcessEnded, all[0].RevokeReason)

	_, secret, err := keys.AddChatKey(ctx, chat.ID, "")
	require.NoError(t, err)
	_, err = gateway.Authenticate(ctx, secret)
	require.NoError(t, err)

	require.NoError(t, svc.RevokeToken(ctx, coordinator.ID))

	_, err = gateway.Authenticate(ctx, secret)
	require.ErrorIs(t, err, modelgateway.ErrUnauthorized)
}

// Scenario (issue #996, review of #999): a pi runs under a coordinator token, and the owner revokes it. A model
// change asks for a new key: it is refused, not made without a parent. A key whose parent was read before the
// revocation and made after it is refused too, in the gateway's own transaction. A key made for the chat without
// naming a parent keeps the one its pi has, so the revocation ends it.
func TestARunningPiGetsNoKeyOnceItsCoordinatorTokenIsRevoked(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	db, err := sqlite.Open(ctx, sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	svc := orchestration.New(db, platform.SystemClock{}, platform.NewID,
		orchestration.WithGatewayKeys(modelgateway.Revocations{}))
	gateway := modelgateway.New(db, platform.SystemClock{}, platform.NewID, nil, nil)
	keys := chatKeys{gateway: gateway, chats: svc, coordinator: "pi"}

	coordinator, _, err := svc.AddToken(ctx, "pi", bearertoken.Coordinator)
	require.NoError(t, err)
	chat, err := svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	_, _, err = keys.AddChatKey(ctx, chat.ID, "")
	require.NoError(t, err)

	_, kept, err := gateway.AddChatKey(ctx, chat.ID, chat.Bot, "gpt-sol")
	require.NoError(t, err, "a key that names no parent")

	parent, err := svc.CoordinatorTokenID(ctx, "pi")
	require.NoError(t, err, "read before the revocation")

	require.NoError(t, svc.RevokeToken(ctx, coordinator.ID))

	_, err = gateway.Authenticate(ctx, kept)
	require.ErrorIs(t, err, modelgateway.ErrUnauthorized, "it kept the pi's parent")

	_, _, err = keys.AddChatKey(ctx, chat.ID, "gpt-sol")
	require.ErrorIs(t, err, modelgateway.ErrConflict, "pi's model change gets no key")

	_, _, err = gateway.AddChatKeyUnder(ctx, parent, chat.ID, chat.Bot, "gpt-sol")
	require.ErrorIs(t, err, modelgateway.ErrConflict, "nor one whose parent was read before")

	all, err := gateway.ListKeys(ctx)
	require.NoError(t, err)

	for _, k := range all {
		assert.NotNil(t, k.RevokedAt, "no key of the chat works: %s", k.ID)
	}
}

// Scenario (issue #996, review of #999): a pi's key is made from a read of its chat taken before the chat was
// deleted, or before its Bot was removed with its chats; the gateway refuses it in the transaction that would make
// it. A key that names no parent, made after the pi's coordinator token was revoked, is refused too.
func TestNoChatKeyIsMadeForAChatGoneOrUnderARevokedToken(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	db, err := sqlite.Open(ctx, sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	svc := orchestration.New(db, platform.SystemClock{}, platform.NewID,
		orchestration.WithGatewayKeys(modelgateway.Revocations{}))
	gateway := modelgateway.New(db, platform.SystemClock{}, platform.NewID, nil, nil)
	keys := chatKeys{gateway: gateway, chats: svc, coordinator: "pi"}

	coordinator, _, err := svc.AddToken(ctx, "pi", bearertoken.Coordinator)
	require.NoError(t, err)

	gone, err := svc.CreateChat(ctx, "", "")
	require.NoError(t, err)
	stale, err := svc.GetChat(ctx, gone.ID)
	require.NoError(t, err, "read before the deletion")
	_, err = svc.DeleteChat(ctx, gone.ID)
	require.NoError(t, err)

	_, _, err = gateway.AddChatKeyUnder(ctx, coordinator.ID, stale.ID, stale.Bot, "")
	require.ErrorIs(t, err, modelgateway.ErrConflict, "a deleted chat gets no key")

	helper, err := svc.PutBot(ctx, orchestration.Bot{Name: "helper"})
	require.NoError(t, err)
	helperChat, err := svc.CreateChat(ctx, helper.ID, "")
	require.NoError(t, err)
	_, err = svc.ForceDeleteBot(ctx, helper.ID)
	require.NoError(t, err)

	_, _, err = gateway.AddChatKeyUnder(ctx, coordinator.ID, helperChat.ID, helper.ID, "")
	require.ErrorIs(t, err, modelgateway.ErrConflict, "a removed Bot's chat gets no key")

	chat, err := svc.CreateChat(ctx, "", "")
	require.NoError(t, err)
	_, _, err = keys.AddChatKey(ctx, chat.ID, "")
	require.NoError(t, err)
	require.NoError(t, svc.RevokeToken(ctx, coordinator.ID))

	_, _, err = gateway.AddChatKey(ctx, chat.ID, chat.Bot, "")
	require.ErrorIs(t, err, modelgateway.ErrConflict, "a key naming no parent keeps the chat's, which is revoked")
}
