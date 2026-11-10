package daemon

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// Scenario: the control plane names the tools a node should have; the node
// installs them and says what it has. Before the first try it says nothing; a
// tool that could not be installed is reported with the reason and tried
// again without being asked; an empty list from the control plane changes
// nothing.
func TestTheNodeSaysWhichToolsItHas(t *testing.T) {
	t.Parallel()

	var (
		mu       sync.Mutex
		offline  = true
		installs = map[string]int{}
	)

	w := newToolWatch(func(_ context.Context, tool nodeproto.Tool) error {
		mu.Lock()
		defer mu.Unlock()

		installs[tool.Name]++

		if tool.Name == "lark-cli" && offline {
			return errors.New("fetching tool lark-cli 1.0.0: no route to host")
		}

		return nil
	})
	w.every = 20 * time.Millisecond

	assert.Nil(t, w.said(), "nothing was asked: the heartbeat says nothing about tools")

	go w.run(t.Context())

	w.ask([]nodeproto.Tool{{Name: "lark-cli", Version: "1.0.0"}, {Name: "gws", Version: "0.22.5"}})

	said := func() []nodeproto.ToolState {
		if s := w.said(); s != nil {
			return *s
		}

		return nil
	}

	require.Eventually(t, func() bool { return said() != nil }, time.Second, 5*time.Millisecond)
	assert.Equal(t, []nodeproto.ToolState{
		{Name: "gws", Version: "0.22.5"},
		{Name: "lark-cli", Error: "fetching tool lark-cli 1.0.0: no route to host"},
	}, said())

	w.ask(nil)
	assert.Len(t, said(), 2, "an empty list changes nothing")

	mu.Lock()
	offline = false
	mu.Unlock()

	require.Eventually(t, func() bool { return said()[1].Version == "1.0.0" }, time.Second, 5*time.Millisecond,
		"a failed install is tried again without being asked")
	assert.Empty(t, said()[1].Error)

	// A new pin is installed when it is named.
	w.ask([]nodeproto.Tool{{Name: "gws", Version: "0.23.0"}})
	require.Eventually(t, func() bool { return len(said()) == 1 && said()[0].Version == "0.23.0" }, time.Second,
		5*time.Millisecond)

	// A daemon that installs no tools has no watcher; asking and reading it are no-ops.
	var none *toolWatch

	none.ask([]nodeproto.Tool{{Name: "gws", Version: "0.22.5"}})
	assert.Nil(t, none.said())
}
