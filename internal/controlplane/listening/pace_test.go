package listening_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/listening"
)

// Design row "Pacing": one call at a time goes to a host, the next waits for the first to end and then for the
// site's gap after it; another host does not wait.
func TestOneCallAtATimePerHostAndAGapBetween(t *testing.T) {
	t.Parallel()

	c := newClock()

	var (
		mu    sync.Mutex
		slept []time.Duration
	)

	p := listening.NewPacer(c.Now, func(_ context.Context, d time.Duration) error {
		mu.Lock()
		defer mu.Unlock()

		slept = append(slept, d)
		c.Advance(d)

		return nil
	})

	done, err := p.Wait(t.Context(), "www.reddit.com", time.Second)
	require.NoError(t, err)

	second := make(chan func(), 1)

	go func() {
		next, err := p.Wait(context.Background(), "www.reddit.com", time.Second)
		assert.NoError(t, err)

		second <- next
	}()

	other, err := p.Wait(t.Context(), "arctic-shift.photon-reddit.com", time.Second)
	require.NoError(t, err, "another host does not wait")
	other()

	select {
	case <-second:
		require.Fail(t, "a second call to the host ran while the first was out")
	case <-time.After(50 * time.Millisecond):
	}

	c.Advance(300 * time.Millisecond)
	done()

	next := <-second
	next()

	mu.Lock()
	defer mu.Unlock()

	assert.Equal(t, []time.Duration{time.Second}, slept, "the second waits the gap after the first ended")

	ctx, cancel := context.WithCancel(t.Context())
	hold, err := p.Wait(t.Context(), "slow.example", 0)
	require.NoError(t, err)

	cancel()

	_, err = p.Wait(ctx, "slow.example", 0)
	require.ErrorIs(t, err, context.Canceled, "a caller that leaves stops waiting")
	hold()
}
