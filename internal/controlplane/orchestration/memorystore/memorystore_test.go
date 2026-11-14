package memorystore_test

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
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
