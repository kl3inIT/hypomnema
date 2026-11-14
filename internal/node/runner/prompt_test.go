package runner

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kl3inIT/hypomnema/internal/node/statedb"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

func offered() statedb.Dispatch {
	return statedb.Dispatch{
		ID: "dsp_1", State: statedb.StateAccepted, Epoch: 1,
		Offer: nodeproto.Offer{
			DispatchID: "dsp_1", TaskID: "tsk_1", RunID: "run_1", Profile: "implementer",
			Spec: "add the Google login button", Runtime: "fake",
		},
		Accepted: &nodeproto.Accepted{Epoch: 1},
	}
}

// Scenario: the first turn after a Gate and after an interruption. The
// prompt carries the decision and the recovery note.
func TestPromptCarriesTheGateAndTheRecoveryNote(t *testing.T) {
	t.Parallel()

	d := offered()
	d.Offer.Repo, d.Offer.Base = "kl3inIT/second-bite", "main"

	first := 0
	d.Accepted = &nodeproto.Accepted{Epoch: 1, Recovery: true, Gate: &nodeproto.Question{
		Text: "PR #7 is green. Merge it?", Options: []nodeproto.Option{{Label: "merge"}, {Label: "change first"}},
		Answer: &nodeproto.Answer{Option: &first},
	}}

	p := Prompt(d)
	assert.Contains(t, p, "branch hyp/tsk_1")
	assert.Contains(t, p, "gh pr create --base main --head hyp/tsk_1",
		"gh does not find the branch in a worktree of the mirror without --head")
	assert.Contains(t, p, "the pull request as its URL", "a URL names the host; owner/repo#N does not")

	d.Offer.RepoHost, d.Offer.RepoKind = "gitlab.company.example", "gitlab"
	assert.Contains(t, Prompt(d), "glab mr create --target-branch main --source-branch hyp/tsk_1 --yes",
		"a worker on GitLab opens its request with GitLab's own tool")
	d.Offer.RepoHost, d.Offer.RepoKind = "", ""
	assert.Contains(t, p, "outcome is unknown")
	assert.Contains(t, p, "DECISION GATE RESOLVED. You asked: PR #7 is green. Merge it?\nThe answer: merge")
	assert.NotContains(t, p, "For this kind of task", "no instruction, no section")

	d.Offer.Profile, d.Offer.Instruction = "reviewer", "Report problems; change nothing.\n"
	p = Prompt(d)
	assert.Contains(t, p, "Task (reviewer):")
	assert.Contains(t, p, "Rules:")
	assert.Greater(t, strings.Index(p, "For this kind of task:\nReport problems; change nothing."),
		strings.Index(p, "Rules:"), "the profile's instruction follows the rules")
}

// Scenario: the offer carries the agent's memory and the one about the owner
// (issue #21). The first message gives both after the agent's instruction,
// and how to keep a fact only to a worker with the agent API, whose tool
// writes it; a worker with no memory gets no section.
func TestPromptCarriesTheMemories(t *testing.T) {
	t.Parallel()

	d := offered()

	assert.NotContains(t, Prompt(d), "memory", "nothing kept, no tool: no section")

	d.Offer.Instruction = "Report problems."
	d.Offer.Memory, d.Offer.OwnerMemory = "Run the tests first.\nLinh reviews Go.", "The owner writes in Vietnamese."
	p := Prompt(d)
	assert.Contains(t, p, "\nMemory of your agent:\n- Run the tests first.\n- Linh reviews Go.\n"+
		"\nAbout the owner:\n- The owner writes in Vietnamese.\n")
	assert.Greater(t, strings.Index(p, "Memory of your agent"), strings.Index(p, "Report problems."))
	assert.NotContains(t, p, "`memory` tool", "no agent API, no tool to write with")

	d.Offer.AgentKey = "the-dispatch's-key"
	assert.Contains(t, Prompt(d), "Keep in memory, with the `memory` tool")
}
