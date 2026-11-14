package orchestration

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/memorystore"
)

// Scenario: the owner writes what the implementer keeps and what every agent
// knows about him. A worker of the implementer is offered both with its
// Task; it keeps a fact in its agent's memory, which the next worker is
// offered. A change over the limit is refused and changes nothing (issue #21).
func TestAWorkerIsOfferedItsAgentsMemoryAndKeepsAFact(t *testing.T) {
	t.Parallel()

	w := newSchedWorld(t)
	ctx := t.Context()

	_, err := w.s.Memories().SetAgentMemory(ctx, "implementer", "- Run the tests first.\n\nRun the tests first.\n")
	require.NoError(t, err)
	_, err = w.s.Memories().SetOwnerMemory(ctx, "The owner writes in Vietnamese.")
	require.NoError(t, err)

	w.tasks(NewTask{Ref: "a", Spec: "a"})
	first := w.dispatchOf(w.schedule().Created[0])

	offer := func(id string) Offer {
		t.Helper()

		pending, err := w.s.NodePending(ctx, w.node.ID)
		require.NoError(t, err)

		for _, o := range pending.Offers {
			if o.DispatchID == id {
				return o
			}
		}

		require.FailNow(t, "no offer of "+id)

		return Offer{}
	}

	o := offer(first.ID)
	assert.Equal(t, "Run the tests first.", o.Memory, "the owner's lines are the memory, once each")
	assert.Equal(t, "The owner writes in Vietnamese.", o.OwnerMemory)

	m, err := w.s.Memories().ChangeDispatchMemory(ctx, first.ID, false,
		memorystore.Op{Kind: memorystore.Add, Text: " Linh  reviews Go. "})
	require.NoError(t, err)
	assert.Equal(t, memorystore.Memory{Agent: "implementer", Text: "Run the tests first.\nLinh reviews Go.",
		Limit: MaxMemory, UpdatedAt: m.UpdatedAt}, m)

	_, err = w.s.Memories().ChangeDispatchMemory(ctx, first.ID, true,
		memorystore.Op{Kind: memorystore.Add, Text: strings.Repeat("x", 1400)})
	require.ErrorIs(t, err, ErrInvalid)
	require.ErrorContains(t, err, "over its limit of 1375")

	owner, err := w.s.Memories().OwnerMemory(ctx)
	require.NoError(t, err)
	assert.Equal(t, "The owner writes in Vietnamese.", owner.Text, "a refused change changes nothing")

	w.tasks(NewTask{Ref: "b", Spec: "b"})
	second := w.dispatchOf(w.schedule().Created[0])
	assert.Equal(t, "Run the tests first.\nLinh reviews Go.", offer(second.ID).Memory)

	_, err = w.s.Memories().AgentMemory(ctx, "nobody")
	require.ErrorIs(t, err, ErrNotFound)
	_, err = w.s.Memories().SetAgentMemory(ctx, "nobody", "x")
	require.ErrorIs(t, err, ErrNotFound)
}
