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

// Scenario (issues #952, #1051): the reviewer's sample is the one home of the review rules. It blocks only on a P0 or
// P1 it proved at the head, that supported use with the default configuration reaches and that breaks the spec's
// intent; it holds the change to the spec alone, newly blocks from a second round only on the diff since the
// reviewed head and names its evidence level. Each rule is stated once, and its source comment names the reference
// it is learned from, out of the instruction.
func TestTheReviewerSampleBlocksOnlyOnBugsSupportedUseReaches(t *testing.T) {
	t.Parallel()

	raw := deployProfile(t, "reviewer.yaml")

	var sample profileSource
	require.NoError(t, decodeStrict(raw, &sample))

	for rule, from := range map[string]string{
		"The spec is the whole bar":                                     "pstack skills/poteto-mode/worker.md:4",
		"An owner's condition in the spec is that condition as written": "pstack skills/poteto-mode/worker.md:4",
		"P0 blocks release or operation for every input":                "Codex codex-rs/prompts/templates/review/rubric.md:56",
		"P2 and P3 never block.":                                        "omp packages/coding-agent/src/prompts/agents/reviewer.md:88-93",
		"A P0 or P1 blocks, and so makes the verdict incorrect, only when all three hold: it has proof at the head commit": "pstack skills/poteto-mode/playbooks/autopilot-full.md:16",
		"documented, supported use with the default configuration reaches its trigger":                                     "Codex codex-rs/prompts/templates/review/rubric.md:14,17,18",
		"and it breaks the spec's intent":                                                                "skills/interrogate/SKILL.md:19-23",
		"is P2 at most: list it as a follow-up that names its precondition":                              "rubric.md:14,17,18 (criteria 3, 6, 7) and :27",
		"A problem the base already has does not block unless the change copies it or newly reaches it.": "pstack skills/poteto-mode/playbooks/autopilot-full.md:16",
		"a new blocking finding must be in the diff since that head":                                     "omp packages/coding-agent/src/prompts/agents/reviewer.md:73",
		"Run the checks and tests that touch the diff, and each finding's repro":                         "pstack skills/interrogate/SKILL.md:19-23",
		"blocked: you could not run it, never a pass":                                                    "orchestrate skills/orchestrate/prompts/verifier.md:38-61",
		"Verify only that its cited code exists":                                                         "oh-my-opencode packages/senpi-task/src/agents/builtin/plan-reviewer.ts:120-151",
	} {
		assert.Equal(t, 1, strings.Count(sample.Instruction, rule), rule)
		assert.Contains(t, raw, from, rule)
	}

	assert.NotContains(t, sample.Instruction, "pstack", "sources are comments")
	assert.Equal(t, strings.Count(sample.Instruction, "\n"), strings.Count(raw, "\n# Line "),
		"each line of the instruction has its source comment")
}

// Scenario (PR #1056 review round 1): a plan review shares the reviewer profile but has no pull request, so the
// fields only a pull request or merge request has (its merge state, mergeability, required checks, and the
// reviewed pull request in the evidence) are asked for only when the review is of one. The verdict, head and
// evidence level stay asked of every review.
func TestTheReviewerSampleAsksPullRequestFieldsOnlyOfAPullRequestReview(t *testing.T) {
	t.Parallel()

	var sample profileSource
	require.NoError(t, decodeStrict(deployProfile(t, "reviewer.yaml"), &sample))

	const scope = "For a pull request or merge request review"

	for _, field := range []string{"`Merge state:", "`Mergeable:", "`Checks:", "gh pr checks", "the reviewed pull request"} {
		at := strings.Index(sample.Instruction, field)
		require.NotEqual(t, -1, at, field)

		start := strings.LastIndex(sample.Instruction[:at], "\n") + 1
		sentence := strings.LastIndex(sample.Instruction[start:at], ". ")
		if sentence >= 0 {
			start += sentence
		}

		assert.Contains(t, sample.Instruction[start:at], scope, "%s is asked of a plan review too", field)
	}

	for _, every := range []string{"`Verdict: correct`", "`Head: <sha>`", "`Evidence: <level>`"} {
		at := strings.Index(sample.Instruction, every)
		require.NotEqual(t, -1, at, every)
		line := sample.Instruction[strings.LastIndex(sample.Instruction[:at], "\n")+1 : at]
		assert.NotContains(t, line, scope, "%s is asked of every review", every)
	}
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
