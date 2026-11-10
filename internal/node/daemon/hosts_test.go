package daemon

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Scenario: the control plane names the git hosts; the node tries them and
// says which it reaches. Before the first try it says nothing; a host that
// stops answering leaves what the node says at its next try; an empty list
// from the control plane changes nothing.
func TestTheNodeSaysWhichGitHostsItReaches(t *testing.T) {
	t.Parallel()

	var (
		mu   sync.Mutex
		up   = map[string]bool{"github.com": true, "gitlab.company.example": true}
		seen = map[string]int{}
	)

	w := newHostWatch(func(_ context.Context, host string) bool {
		mu.Lock()
		defer mu.Unlock()

		seen[host]++

		return up[host]
	})
	w.every = 20 * time.Millisecond

	assert.Nil(t, w.said(), "nothing was asked: the heartbeat says nothing about hosts")

	go w.run(t.Context())

	w.ask([]string{"gitlab.company.example", "github.com", "gitlab.com"})

	said := func() []string {
		if s := w.said(); s != nil {
			return *s
		}

		return nil
	}

	require.Eventually(t, func() bool { return said() != nil }, time.Second, 5*time.Millisecond)
	assert.Equal(t, []string{"github.com", "gitlab.company.example"}, said())

	w.ask(nil)
	assert.Equal(t, []string{"github.com", "gitlab.company.example"}, said(), "an empty list changes nothing")

	mu.Lock()
	up["gitlab.company.example"] = false
	mu.Unlock()

	require.Eventually(t, func() bool { return len(said()) == 1 }, time.Second, 5*time.Millisecond,
		"the hosts are tried again without being asked")
	assert.Equal(t, []string{"github.com"}, said())

	w.ask([]string{"gitlab.com"})
	require.Eventually(t, func() bool { return len(said()) == 0 }, time.Second, 5*time.Millisecond)
	assert.NotNil(t, w.said(), "reaching none is said, not left out")

	// A daemon that does not try hosts has no watcher; asking and reading it are no-ops.
	var none *hostWatch

	none.ask([]string{"github.com"})
	assert.Nil(t, none.said())
}
