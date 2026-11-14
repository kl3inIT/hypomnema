package memorystore_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/storeutil"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/memorystore"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

// newTestStore is the memories' store as the root's facade gives it, over a
// migrated database, which has the seeded profiles.
func newTestStore(t *testing.T) *memorystore.Store {
	t.Helper()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	n := 0
	ids := func(prefix string) string {
		n++

		return fmt.Sprintf("%s_%04d", prefix, n)
	}

	clock := &fakeClock{now: time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)}

	return orchestration.New(db, clock, ids).Memories()
}

// The offer reads the memories inside its own transaction: AgentMemoryTx and
// OwnerMemoryTx read with the caller's queries and open nothing of their own,
// so they see what that transaction wrote and do not wait for the one
// connection, which the transaction holds (issue #404).
func TestTheTxReadsUseTheCallersTransaction(t *testing.T) {
	t.Parallel()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	clock := &fakeClock{now: time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)}

	err = storeutil.Write(ctx, db, clock, func() {}, func(q *store.Queries, now int64) error {
		for _, m := range []store.SetMemoryParams{
			{Kind: "agent", Name: "reviewer", Memory: "Not yet committed.", Now: now},
			{Kind: "owner", Name: "", Memory: "Writes in Vietnamese.", Now: now},
		} {
			require.NoError(t, q.SetMemory(ctx, m))
		}

		agent, err := memorystore.AgentMemoryTx(ctx, q, "reviewer")
		require.NoError(t, err)
		assert.Equal(t, "Not yet committed.", agent.Text)

		owner, err := memorystore.OwnerMemoryTx(ctx, q)
		require.NoError(t, err)
		assert.Equal(t, "Writes in Vietnamese.", owner.Text)

		return nil
	})
	require.NoError(t, err, "a Tx read that opened its own would wait for the connection until the timeout")
}

// Workers of one agent writing at once each keep their entry: the memory is
// read again inside each write, as Hermes's tool re-reads its file.
func TestWorkersWritingAtOnceKeepEveryEntry(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)

	var wg sync.WaitGroup

	for i := range 8 {
		wg.Go(func() {
			_, err := s.ChangeAgentMemory(t.Context(), "reviewer",
				memorystore.Op{Kind: memorystore.Add, Text: fmt.Sprintf("fact %d", i)})
			assert.NoError(t, err)
		})
	}

	wg.Wait()

	m, err := s.AgentMemory(t.Context(), "reviewer")
	require.NoError(t, err)
	assert.Len(t, strings.Split(m.Text, "\n"), 8)
}
