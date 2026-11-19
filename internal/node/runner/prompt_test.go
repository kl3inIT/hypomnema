package runner

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kl3inIT/hypomnema/internal/node/statedb"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
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
func TestRoutedReadOnlyPromptsDoNotAskForPullRequests(t *testing.T) {
	t.Parallel()

	d := offered()
	d.Offer.Repo, d.Offer.Base = "kl3inIT/second-bite", "main"
	d.Offer.RoleRouted = true
	for _, role := range []string{"planner", "explorer", "reviewer"} {
		d.Offer.Profile = role
		prompt := Prompt(d)
		assert.Contains(t, prompt, "Do not edit project files")
		assert.NotContains(t, prompt, "gh pr create")
		assert.NotContains(t, prompt, "Do not end your turn on a plan")
		if role == "planner" {
			assert.Contains(t, prompt, "this CLI's subagents for independent read-only lookups")
			assert.NotContains(t, prompt, "Do not start subagents")
		} else {
			assert.Contains(t, prompt, "Do not start subagents")
		}
	}
}

func TestPromptCarriesTheGateAndTheRecoveryNote(t *testing.T) {
	t.Parallel()

	d := offered()
	d.Offer.Repo, d.Offer.Base = "kl3inIT/second-bite", "main"
	d.Offer.RepoHost, d.Offer.RepoKind = "github.com", "github"

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
	d.Offer.RepoHost, d.Offer.RepoKind = "github.com", "github"
	assert.Contains(t, p, "When it is false (the work is already there, the problem does not reproduce), "+
		"change nothing and call `finish` with that evidence", "a task's premise is checked first (issue #513)")
	assert.Contains(t, p, "read the code of the references, not only their documents, and cite the paths and lines",
		"research and design read the references' code (issue #719)")
	assert.Less(t, strings.Index(p, "read the code of the references"), strings.Index(p, "Task (implementer):"),
		"the rule comes before the task, for the prompt cache (issue #672)")
	assert.Contains(t, p, "outcome is unknown")
	assert.Contains(t, p, "The owner answered the question you asked at `finish`.\n"+
		"You asked: PR #7 is green. Merge it?\nThe answer: merge")
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

	assert.NotContains(t, Prompt(d), "Memory of your agent", "nothing kept, no tool: no section")
	assert.NotContains(t, Prompt(d), "`memory` tool", "nothing kept, no tool: no section")

	d.Offer.Instruction = "Report problems."
	d.Offer.Memory, d.Offer.OwnerMemory = "Run the tests first.\nLinh reviews Go.", "The owner writes in Vietnamese."
	p := Prompt(d)
	assert.Contains(t, p, "\nMemory of your agent:\n- Run the tests first.\n- Linh reviews Go.\n"+
		"\nAbout the owner:\n- The owner writes in Vietnamese.\n")
	assert.Greater(t, strings.Index(p, "Memory of your agent"), strings.Index(p, "Report problems."))
	assert.NotContains(t, p, "`memory` tool", "no agent API, no tool to write with")

	d.Offer.MemoryNotes = []string{"/memories/release.md (1.5K)"}
	assert.NotContains(t, Prompt(d), "release.md", "no tool to read a note with, no list of them")

	d.Offer.AgentKey = "the-dispatch's-key"
	p = Prompt(d)
	assert.Contains(t, p, "Keep in memory, with the `memory` tool")
	assert.Contains(t, p, "\nNotes of your agent's memory, read with the `memory` tool's view when their subject "+
		"comes up:\n- /memories/release.md (1.5K)\n", "the notes are listed, not given (issue #589)")
}

// Scenario: the coordinator asked more of a Task whose worker had finished
// (issue #666). The next turn goes on in the same Conversation and tells the
// worker the note, to continue on its branch and update its pull request,
// before a Gate answered since; a Dispatch with no follow-up says nothing of
// one.
func TestPromptCarriesTheFollowUp(t *testing.T) {
	t.Parallel()

	d := offered()
	assert.NotContains(t, Prompt(d), "more is asked of it", "no follow-up, no section")

	d.Offer.Repo, d.Offer.Base = "kl3inIT/second-bite", "main"
	d.Offer.RepoHost, d.Offer.RepoKind = "github.com", "github"
	d.Accepted = &nodeproto.Accepted{Epoch: 1, FollowUp: "  The review found: the button has no test.\n"}

	p := Prompt(d)
	assert.Contains(t, p, "You called `finish` on this task before, and more is asked of it now (this replaces "+
		"the answer to any question you asked at `finish`):\n"+
		"The review found: the button has no test.\n"+
		"Continue in this worktree and on this branch; update the same pull request, then call `finish` again.\n")
	assert.Contains(t, p, "Task (implementer):\nadd the Google login button", "the spec is still given")

	d.Accepted.Gate = &nodeproto.Question{Text: "Add an e2e test too?", Answer: &nodeproto.Answer{Text: "yes"}}
	p = Prompt(d)
	assert.Greater(t, strings.Index(p, "The owner answered"), strings.Index(p, "more is asked of it now"),
		"the answer to the newest question comes last")

	d.Offer.Repo, d.Accepted.Gate = "", nil
	assert.Contains(t, Prompt(d), "Continue in this directory, then call `finish` again.\n",
		"without a repository there is no pull request to update")
}

// Scenario: a worker ends with a report the coordinator can act on (issue
// #666): the answer first, what was done, found and left, the checks run and
// skipped, then the evidence.
func TestPromptAsksForTheReportShape(t *testing.T) {
	t.Parallel()

	p := Prompt(offered())
	assert.Contains(t, p, "write it for someone who has seen none of your work: the answer first, and when the "+
		"work is not done, say so in its first line; then what you did, what you found and what is left; which "+
		"checks you ran and which you skipped; then the evidence", "a failure is said first (issue #672)")
	assert.Contains(t, p, "Do not end your turn on a plan, an offer to continue or a list of next steps",
		"the worker finishes the whole task (issue #672)")
	assert.Contains(t, p, "no progress messages are needed")
}

// Scenario: the order of the first turn (issue #672, after Orca's preamble,
// whose rules come first and the task last). The rules, the profile's
// instruction and the memories come before where to work; the spec follows;
// a recovery note and a follow-up come after the spec.
func TestPromptPutsTheTaskAfterTheRules(t *testing.T) {
	t.Parallel()

	d := offered()
	d.Offer.Repo, d.Offer.Base = "kl3inIT/second-bite", "main"
	d.Offer.RepoHost, d.Offer.RepoKind = "github.com", "github"
	d.Offer.Instruction, d.Offer.Memory = "Run the checks.", "Linh reviews Go."
	d.Accepted = &nodeproto.Accepted{Epoch: 1, Recovery: true, FollowUp: "Add a test."}

	p := Prompt(d)

	// The decision boundary the owner set (issue #672): what the worker decides itself, then what it asks first.
	assert.Contains(t, p, "Decide yourself, within what the task and your instructions allow")
	assert.Contains(t, p, "Ask first, with the `ask` tool, and wait for the answer: before changing the task's scope, "+
		"deploying or changing a server", "merging is not asked: auto-merge is turned on (issue #714)")
	assert.Contains(t, p, "turning on its auto-merge as said below. Never merge it directly.")
	assert.NotContains(t, p, "When a decision is not yours to make")

	order := []string{
		"Rules:", "For this kind of task:", "Memory of your agent", "Work only in this directory",
		"Task (implementer):\nadd the Google login button\n", "outcome is unknown", "more is asked of it now",
	}
	for i := 1; i < len(order); i++ {
		assert.Greater(t, strings.Index(p, order[i]), strings.Index(p, order[i-1]),
			"%q comes after %q", order[i], order[i-1])
	}
}

// Scenario: the worker's pull request merges by itself once green (issue
// #714). On GitHub it checks, read-only, that the repository allows
// auto-merge and that the base branch requires a check, by ruleset or branch
// protection; a draft stays open; a conflict is resolved by merging the base;
// auto-merge is pinned to the pushed commit; it waits for the required checks
// and no longer. A repository lacking a setting gets the pull request left
// open, with what it lacks said first, and no gate. A task that says a review
// comes first or to ask before merging skips it. GitLab has its own commands
// and waits for a pipeline; a Task without a repository has none of this.
func TestPromptTurnsOnTheHostsAutoMerge(t *testing.T) {
	t.Parallel()

	d := offered()
	assert.NotContains(t, Prompt(d), "auto-merge on", "no repository, no pull request")

	d.Offer.Repo, d.Offer.Base = "kl3inIT/second-bite", "main"
	d.Offer.RepoHost, d.Offer.RepoKind = "github.com", "github"
	p := Prompt(d)

	for _, want := range []string{
		"never merge it directly",
		"When the task says a review comes first, that the owner merges, or to ask before merging, skip this",
		"`gh api --hostname github.com repos/kl3inIT/second-bite --jq .allow_auto_merge`: prints true",
		"`gh api --hostname github.com repos/kl3inIT/second-bite/rules/branches/main` and " +
			"`gh api --hostname github.com repos/kl3inIT/second-bite/branches/main/protection/required_status_checks`: " +
			"the first lists a rule of type required_status_checks",
		"if one does not hold, do not turn auto-merge on and do not merge: leave the pull request open and say " +
			"in the summary's first line what the repository lacks (\"auto-merge is off in the repository's " +
			"settings\" or \"no required check on main\")",
		"do not open a gate only to ask whether to merge",
		"`gh pr view <url> --json isDraft,mergeStateStatus`. A draft (isDraft is true) is left open",
		"On a conflict (mergeStateStatus is DIRTY), merge main into your branch, resolve it keeping the intent " +
			"of both sides, run the repository's checks again and push",
		"`gh pr merge <url> --auto --merge --match-head-commit <sha>`, where <sha> is the commit you pushed",
		"`gh pr checks <url> --required --watch`; fix a red one at its cause and push",
		"Once they are green, call `finish` without waiting longer",
	} {
		assert.Contains(t, p, want)
	}

	assert.Less(t, strings.Index(p, "gh pr create"), strings.Index(p, "gh pr merge"),
		"auto-merge follows the pull request it is for")
	assert.Less(t, strings.Index(p, "gh pr merge"), strings.Index(p, "Task (implementer):"),
		"the task's own word on merging, after the rules, wins")

	// The offer names the repository by its path and the host apart (orchestration's Offer, connect.go), as
	// the control plane sends it; a GitLab subgroup has more than two names.
	d.Offer.Repo, d.Offer.RepoHost, d.Offer.RepoKind = "team/platform/api", "gitlab.company.example", "gitlab"
	p = Prompt(d)

	for _, want := range []string{
		"`glab api projects/team%2Fplatform%2Fapi`: has only_allow_merge_if_pipeline_succeeds: true",
		"\"\"Pipelines must succeed\" is off in the project's settings\"",
		"Before step 3, check that its pipeline is not null",
		"`glab mr merge <iid> --auto-merge --yes --sha <sha>`",
		"`glab ci status --live --branch hyp/tsk_1`",
	} {
		assert.Contains(t, p, want)
	}

	assert.NotContains(t, p, "gh pr merge")

	d.Offer.Repo, d.Offer.RepoHost, d.Offer.RepoKind = "kl3inIT/second-bite", "github.example.com", "github"
	assert.Contains(t, Prompt(d),
		"`gh api --hostname github.example.com repos/kl3inIT/second-bite --jq .allow_auto_merge`",
		"a GitHub Enterprise host is named apart too")
}

// Scenario (issue #723, decisions 2.2 to 2.4): the offer names the tools the
// worker gets, and its rules end with the "Tools" block built from them, the
// same block a Bot with those tools gets (agenttool.ToolGuide), before the
// profile's instruction and the task. An offer of a control plane before
// #723, with no tools, gets no block.
func TestPromptCarriesTheToolsBlockBeforeTheTask(t *testing.T) {
	t.Parallel()

	d := offered()
	d.Offer.Instruction = "Run the checks."
	d.Offer.Tools = []string{agenttool.ToolMemory, agenttool.ToolWebSearch, agenttool.ToolWebFetch,
		agenttool.ToolGenerateImage}

	p := Prompt(d)
	block := agenttool.ToolGuide(d.Offer.Tools)

	assert.Contains(t, p, "the paths and lines you read.\n\n"+block+"\nFor this kind of task:",
		"after the rules, before the instruction")
	assert.Less(t, strings.Index(p, block), strings.Index(p, "Task (implementer):"))

	d.Offer.Tools = nil
	assert.NotContains(t, Prompt(d), "## Tools")
}
