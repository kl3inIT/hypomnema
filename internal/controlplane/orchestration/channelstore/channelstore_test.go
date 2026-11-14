package channelstore_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/channelstore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/storeutil"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

// The root starts the chat of a Lark chat and removes a Bot inside its own
// transaction: the Tx functions write and read with the caller's queries and
// open nothing of their own, so they see what that transaction wrote and do
// not wait for the one connection, which the transaction holds (issue #404).
func TestTheTxFunctionsUseTheCallersTransaction(t *testing.T) {
	t.Parallel()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	clock := &fakeClock{now: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)}
	bot := orchestration.DefaultBot

	err = storeutil.Write(ctx, db, clock, func() {}, func(q *store.Queries, now int64) error {
		require.NoError(t, q.UpsertLarkChannel(ctx, store.UpsertLarkChannelParams{
			Name: "main", AppID: "cli_main", Secret: "lark-channel-main", Bot: bot, Mode: "owner",
			OwnerOpenID: "ou_owner", Enabled: 1, Now: now,
		}))

		channels, err := channelstore.CountLarkChannelsOfBotTx(ctx, q, bot)
		require.NoError(t, err)
		assert.Equal(t, int64(1), channels, "the Channel not yet committed")

		for _, id := range []string{"cht_first", "cht_second"} {
			require.NoError(t, q.InsertChat(ctx, store.InsertChatParams{ID: id, Title: "Group", Bot: bot, Now: now}))
		}

		require.NoError(t, channelstore.RecordLarkChannelChatTx(ctx, q, channelstore.LarkChannelChat{
			Channel: "main", LarkChatID: "oc_group", ChatID: "cht_first", Group: true,
		}, now))
		require.NoError(t, channelstore.ResetLarkChannelChatTx(ctx, q, "main", "oc_group", "cht_second", true))

		row, err := q.GetLarkChannelChat(ctx, store.GetLarkChannelChatParams{Channel: "main", LarkChatID: "oc_group"})
		require.NoError(t, err)
		assert.Equal(t, "cht_second", row.ChatID)

		group, err := q.IsLarkGroupChat(ctx, "cht_second")
		require.NoError(t, err)
		assert.Equal(t, int64(1), group, "the new chat is the group's too")

		require.NoError(t, channelstore.DeleteLarkChannelChatsOfBotTx(ctx, q, bot))

		_, err = q.GetLarkChannelChat(ctx, store.GetLarkChannelChatParams{Channel: "main", LarkChatID: "oc_group"})
		require.ErrorIs(t, err, sql.ErrNoRows)

		return nil
	})
	require.NoError(t, err, "a Tx function that opened its own would wait for the connection until the timeout")
}
