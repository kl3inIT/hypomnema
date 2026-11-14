package channels_test

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/channels"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/channelstore"
)

// A kind is one Channel: the registry made at startup refuses a kind given
// twice (docs/conventions.md, Strategy behind a registry), and finds the one
// given once.
func TestTheRegistryRefusesAKindGivenTwice(t *testing.T) {
	t.Parallel()

	_, err := channels.NewRegistry(kind{}, kind{})
	require.ErrorContains(t, err, "lark is registered twice")

	registry, err := channels.NewRegistry(kind{})
	require.NoError(t, err)

	found, err := registry.Lookup(channelstore.KindLark)
	require.NoError(t, err)
	assert.Equal(t, channelstore.KindLark, found.Kind())

	_, err = registry.Lookup("zalo")
	require.Error(t, err)
}

// Scenario: a Channel's kind has no Channel in the registry. It does not
// connect, and says why; nothing else stops.
func TestAChannelOfAnUnknownKindSaysWhyItDoesNotConnect(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.setUp(channelstore.ChannelOwner)

	empty, err := channels.NewRegistry()
	require.NoError(t, err)

	runtime := channels.New(w.asks, w.pi, empty, w.vault, slog.New(slog.DiscardHandler))

	go func() { _ = runtime.Run(t.Context()) }()

	require.Eventually(t, func() bool { return runtime.State(main).LastError != "" }, eventual, tick)
	assert.Contains(t, runtime.State(main).LastError, `no channel of kind "lark" is registered`)
	assert.False(t, runtime.State(main).Connected)
}
