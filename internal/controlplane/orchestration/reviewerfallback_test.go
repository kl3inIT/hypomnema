package orchestration

import (
	"os"
	"strings"
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

// Scenario: the Claude reviewer reviews as the Codex reviewer does (issue #756). Its instruction is the Codex
// reviewer's word for word, with one paragraph more: a review of a Claude author's change says it was not
// independent.
func TestTheClaudeReviewerHasTheCodexReviewersInstruction(t *testing.T) {
	t.Parallel()

	var codex, claude profileSource
	require.NoError(t, decodeStrict(deployProfile(t, "reviewer-codex.yaml"), &codex))
	require.NoError(t, decodeStrict(deployProfile(t, "reviewer-claude.yaml"), &claude))

	assert.True(t, strings.HasPrefix(claude.Instruction, codex.Instruction), "the Codex reviewer's text first")
	assert.Contains(t, strings.TrimPrefix(claude.Instruction, codex.Instruction), "the review was not independent")
	assert.Equal(t, "claude", claude.Runtime)
	assert.Empty(t, claude.Fallbacks, "a backup's own backups are not followed")
}

// Scenario (issue #756, split of PR #786): the gateway now ends a Codex worker's turn with a usage limit when every
// Codex account cools, and the node reports it as rate_limited with no reset. The scheduler cannot see the
// gateway's accounts, so the review, applied from deploy/profiles as the owner applies it, is not started on Codex
// again at once: it waits RateLimitCooldown, then runs again.
func TestACodexReviewTheGatewayRefusesWaitsAndDoesNotLoop(t *testing.T) {
	t.Parallel()

	w := newSchedWorld(t)
	ctx := t.Context()

	w.gatewayAccount("claude")
	w.gatewayAccount("codex")

	both := w.addNode("node_both", 4, "claude", "codex")
	require.NoError(t, w.s.DrainNode(ctx, w.node.ID))

	_, _, err := w.s.ApplyProfile(ctx, "", deployProfile(t, "reviewer-claude.yaml"))
	require.NoError(t, err)

	codex := strings.NewReplacer("<gateway provider>", "codex-subscription", "<gateway model>", "gpt-6.1-sol").
		Replace(deployProfile(t, "reviewer-codex.yaml"))
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
