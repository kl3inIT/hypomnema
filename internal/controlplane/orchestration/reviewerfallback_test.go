package orchestration

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func deployProfile(t *testing.T, name string) string {
	t.Helper()

	raw, err := os.ReadFile("../../../deploy/profiles/" + name) //nolint:gosec // a file of the repository's samples
	require.NoError(t, err)

	return string(raw)
}

func TestDeploymentProfilesDoNotPinModels(t *testing.T) {
	t.Parallel()

	for _, role := range roles {
		var sample profileSource
		require.NoError(t, decodeStrict(deployProfile(t, role+".yaml"), &sample))
		assert.Equal(t, role, sample.Name)
		assert.Empty(t, sample.Runtime)
		assert.Nil(t, sample.Model)
		assert.Empty(t, sample.Fallbacks)
		assert.NotEmpty(t, sample.Instruction)
	}
}

// Scenario (issue #952): the reviewer's sample blocks only on what it proved at the head, holds the change to
// the spec alone and names its evidence level; its sources stay in comments, out of the instruction.
func TestTheReviewerSampleBlocksOnlyOnProof(t *testing.T) {
	t.Parallel()

	var sample profileSource
	require.NoError(t, decodeStrict(deployProfile(t, "reviewer.yaml"), &sample))

	for _, want := range []string{
		"The spec is the whole bar",
		"A P0 or P1, and so an incorrect verdict, needs proof at the head commit",
		"A problem the base already has does not block unless the change copies it or newly reaches it.",
		"blocked (you could not run it; never a pass)",
		"Verify only that its cited code exists",
	} {
		assert.Contains(t, sample.Instruction, want)
	}

	assert.NotContains(t, sample.Instruction, "pstack", "sources are comments")
}

// Scenario (issue #756, split of PR #786): the gateway now ends a Codex worker's turn with a usage limit when every
// Codex account cools, and the node reports it as rate_limited with no reset. A legacy reviewer profile is not
// started on Codex again at once: it waits RateLimitCooldown, then runs again.
func TestACodexReviewTheGatewayRefusesWaitsAndDoesNotLoop(t *testing.T) {
	t.Parallel()

	w := newSchedWorld(t)
	ctx := t.Context()

	w.gatewayAccount("claude")
	w.gatewayAccount("codex")

	both := w.addNode("node_both", 4, "claude", "codex")
	require.NoError(t, w.s.DrainNode(ctx, w.node.ID))

	_, _, err := w.s.ApplyProfile(ctx, "", "name: reviewer-claude\ndescription: legacy reviewer\nruntime: claude\n"+
		"model: {provider: claude-subscription, name: claude-opus-5-5}\n")
	require.NoError(t, err)

	codex := "name: reviewer\ndescription: legacy reviewer\nruntime: codex\n" +
		"model: {provider: codex-subscription, name: gpt-6.1-sol}\n"
	_, _, err = w.s.ApplyProfile(ctx, "", codex)
	require.NoError(t, err)

	review := w.tasks(NewTask{Ref: "r", Profile: "reviewer", Spec: "review PR #786"})[0]

	created := w.schedule().Created
	require.Len(t, created, 1)
	assert.Equal(t, "codex", w.conversation(w.dispatchOf(created[0]).ConversationID).Runtime)

	// The CLI's message on the gateway's usage_limit_reached answer (codex-cli 0.169.0).
	require.NoError(t, w.finish(w.begin(created[0]), Outcome{
		Class:  FailureRateLimited,
		Result: Result{Summary: "You’ve hit your usage limit. Try again at 1:12 PM."},
	}))

	require.NotNil(t, w.task(review.ID).NotBefore, "held")
	assert.Equal(t, t0.Add(RateLimitCooldown), *w.task(review.ID).NotBefore)

	for range 3 {
		w.advance(10 * time.Second)
		w.alive(both)
		assert.Empty(t, w.schedule().Created, "not started again before the wait")
	}

	w.advance(RateLimitCooldown)
	w.alive(both)
	assert.Len(t, w.schedule().Created, 1, "then it runs again")
}
