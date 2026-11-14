package node

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/node/runner"
	"github.com/kl3inIT/hypomnema/internal/node/statedb"
)

type noRuntime struct{}

func (noRuntime) Prepare(context.Context, statedb.Dispatch) (runner.Launch, error) {
	return runner.Launch{}, nil
}

func (noRuntime) Adopted([]byte) runner.Turn { return nil }

// Scenario: the node starts. Every runtime it looks for has one Runtime; a
// key named twice or a runtime with none stops it at startup, not at the
// first offer.
func TestTheRegistryRefusesAMissingOrDuplicateRuntime(t *testing.T) {
	t.Parallel()

	var all []entry
	for _, key := range knownRuntimes {
		all = append(all, entry{key, noRuntime{}})
	}

	byRuntime, err := registry(all...)
	require.NoError(t, err)
	assert.Len(t, byRuntime, len(knownRuntimes))

	_, err = registry(append(all, entry{"codex", noRuntime{}})...)
	require.ErrorContains(t, err, "two runtimes are named codex")

	_, err = registry(all[1:]...)
	require.ErrorContains(t, err, "no runtime is named "+knownRuntimes[0])
}
