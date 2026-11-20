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
