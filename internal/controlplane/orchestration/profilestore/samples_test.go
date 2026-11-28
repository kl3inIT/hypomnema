package profilestore

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func deployProfile(t *testing.T, name string) string {
	t.Helper()

	raw, err := os.ReadFile("../../../../deploy/profiles/" + name) //nolint:gosec // a file of the repository's samples
	require.NoError(t, err)

	return string(raw)
}

func TestDeploymentProfilesDoNotPinModels(t *testing.T) {
	t.Parallel()

	for _, role := range Roles {
		var sample Source
		require.NoError(t, decodeStrict(deployProfile(t, role+".yaml"), &sample))
		assert.Equal(t, role, sample.Name)
		assert.Empty(t, sample.Runtime)
		assert.Nil(t, sample.Model)
		assert.Empty(t, sample.Fallbacks)
		assert.NotEmpty(t, sample.Instructions)
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

	var sample Source
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
		assert.Equal(t, 1, strings.Count(sample.Instructions, rule), rule)
		assert.Contains(t, raw, from, rule)
	}

	assert.NotContains(t, sample.Instructions, "pstack", "sources are comments")
	assert.Equal(t, strings.Count(sample.Instructions, "\n"), strings.Count(raw, "\n# Line "),
		"each line of the instruction has its source comment")
}

// Scenario (PR #1056 review round 1): a plan review shares the reviewer profile but has no pull request, so the
// fields only a pull request or merge request has (its merge state, mergeability, required checks, and the
// reviewed pull request in the evidence) are asked for only when the review is of one. The verdict, head and
// evidence level stay asked of every review.
func TestTheReviewerSampleAsksPullRequestFieldsOnlyOfAPullRequestReview(t *testing.T) {
	t.Parallel()

	var sample Source
	require.NoError(t, decodeStrict(deployProfile(t, "reviewer.yaml"), &sample))

	const scope = "For a pull request or merge request review"

	pullRequestOnly := []string{"`Merge state:", "`Mergeable:", "`Checks:", "gh pr checks", "the reviewed pull request"}
	for _, field := range pullRequestOnly {
		at := strings.Index(sample.Instructions, field)
		require.NotEqual(t, -1, at, field)

		start := strings.LastIndex(sample.Instructions[:at], "\n") + 1
		sentence := strings.LastIndex(sample.Instructions[start:at], ". ")
		if sentence >= 0 {
			start += sentence
		}

		assert.Contains(t, sample.Instructions[start:at], scope, "%s is asked of a plan review too", field)
	}

	for _, every := range []string{"`Verdict: correct`", "`Head: <sha>`", "`Evidence: <level>`"} {
		at := strings.Index(sample.Instructions, every)
		require.NotEqual(t, -1, at, every)
		line := sample.Instructions[strings.LastIndex(sample.Instructions[:at], "\n")+1 : at]
		assert.NotContains(t, line, scope, "%s is asked of every review", every)
	}
}
