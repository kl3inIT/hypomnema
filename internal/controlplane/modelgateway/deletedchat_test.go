package modelgateway_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

// laterClock is an hour after fixedClock: what orchestration does is told
// apart from what the gateway did.
type laterClock struct{}

func (laterClock) Now() time.Time { return fixedClock{}.Now().Add(time.Hour) }

// failingRevocations are the gateway's revocations, with a step after a chat's that may fail.
type failingRevocations struct {
	modelgateway.Revocations

	fail func() error
}

func (r failingRevocations) RevokeDeletedChatKeysTx(ctx context.Context, tx *sql.Tx, chatID string, now int64) error {
	if err := r.Revocations.RevokeDeletedChatKeysTx(ctx, tx, chatID, now); err != nil {
		return err
	}

	return r.fail()
}

// Scenario (issue #405): orchestration deletes a chat whose pi had keys, and
// the gateway's own function revokes them in that transaction. A delete that
// fails after the revocation leaves the chat and its live key as they were;
// one that commits leaves no live key: the key revoked earlier keeps its time,
// and both are kept and lose the chat.
func TestADeletedChatEndsItsGatewayKeysInItsTransaction(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	db, err := sqlite.Open(ctx, sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	errLater := errors.New("a step after the revocation failed")
	fail := true
	n := 0
	ids := func(prefix string) string {
		n++

		return fmt.Sprintf("%s_%04d", prefix, n)
	}

	orch := orchestration.New(db, laterClock{}, ids, orchestration.WithGatewayKeys(failingRevocations{
		fail: func() error {
			if fail {
				return errLater
			}

			return nil
		},
	}))
	g := modelgateway.New(db, fixedClock{}, ids, nil, nil)

	chat, err := orch.CreateChat(ctx, "", "")
	require.NoError(t, err)
	other, err := orch.CreateChat(ctx, "", "")
	require.NoError(t, err)

	earlier, _, err := g.AddChatKey(ctx, chat.ID, "", "")
	require.NoError(t, err)
	_, live, err := g.AddChatKey(ctx, chat.ID, "", "")
	require.NoError(t, err, "the second key revokes the first")
	_, kept, err := g.AddChatKey(ctx, other.ID, "", "")
	require.NoError(t, err)

	_, err = orch.DeleteChat(ctx, chat.ID)
	require.ErrorIs(t, err, errLater)

	_, err = orch.GetChat(ctx, chat.ID)
	require.NoError(t, err, "the chat stays")

	k, err := g.Authenticate(ctx, live)
	require.NoError(t, err, "the revocation went back with the delete")
	assert.Equal(t, chat.ID, k.ChatID)

	fail = false
	_, err = orch.DeleteChat(ctx, chat.ID)
	require.NoError(t, err)

	_, err = g.Authenticate(ctx, live)
	require.ErrorIs(t, err, modelgateway.ErrUnauthorized, "a deleted chat has no live key")

	_, err = g.Authenticate(ctx, kept)
	require.NoError(t, err, "another chat's key still works")

	keys, err := g.ListKeys(ctx)
	require.NoError(t, err)

	revoked := map[string]time.Time{}

	for _, k := range keys {
		if k.ChatID == other.ID {
			continue
		}

		assert.Empty(t, k.ChatID, "a deleted chat's keys lose the chat")
		require.NotNil(t, k.RevokedAt, "and are kept, revoked")

		revoked[k.ID] = *k.RevokedAt
	}

	require.Len(t, revoked, 2)
	assert.Equal(t, fixedClock{}.Now(), revoked[earlier.ID], "a key revoked earlier keeps its time")

	for id, at := range revoked {
		if id != earlier.ID {
			assert.Equal(t, laterClock{}.Now(), at, "the live key ends as the chat is deleted")
		}
	}
}
