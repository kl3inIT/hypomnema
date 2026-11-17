package runner

import (
	"fmt"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/node/statedb"
	"github.com/kl3inIT/hypomnema/internal/node/worktree"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
	"github.com/kl3inIT/hypomnema/internal/shared/githost"
)

// recoveryNote is the orchestration core's RecoveryNote: the previous
// attempt's outcome is unknown.
const recoveryNote = "Your previous attempt at this task was interrupted and its outcome is unknown. " +
	"Check the worktree, the branch and any pull request before redoing work."

// rules are the worker's instructions for its tools and its work, the same
// for every worker (Orca's preamble forbids AskUserQuestion and subagents and
// requires worker_done exactly once).
const rules = "\nRules:\n" +
	// Issue #672: one decision boundary, stated once, in place of "when a decision is not yours to make": what a
	// worker decides itself and what needs `ask` (GPT-5.6 Sol's prompt guidance, "Require confirmation for external
	// writes, destructive actions ... or a material expansion of scope"; OpenAI's GPT-6 guide, "replacing blanket
	// 'always ask' rules with clear boundaries"; OpenClaw's template "Do freely / Ask first",
	// docs/reference/templates/AGENTS.md L19-23). The list is the owner's (2026-11-17). "Within what the task and
	// your instructions allow" keeps an explorer's or a reviewer's own instruction, which changes nothing.
	"- Decide yourself, within what the task and your instructions allow: how to build it and where the code " +
	"goes, which tests to add or change, running the repository's checks, committing and pushing your branch, " +
	"opening or updating this task's pull request. Ask first, with the `ask` tool, and wait for the answer: " +
	"before changing the task's scope, merging, deploying or changing a server, deleting what your change did " +
	"not make, or acting outside this directory and this task's pull request. " +
	"Never use AskUserQuestion; no one reads it.\n" +
	// Issue #672: Orca's worker "needs no progress updates unless the task asks"
	// (src/main/runtime/orchestration/workers/launch/worker-dispatch-preamble.ts:71-73); omp's subagent, "No TODO
	// tracking, no progress updates" (packages/coding-agent/src/prompts/system/subagent-system-prompt.md:90).
	"- No one reads what you write before `finish`, so no progress messages are needed.\n" +
	// Issue #672: finish the whole task, after Anthropic's Opus 5.5 page (its text for unattended runs) and Fable
	// 5.2 page ("Finishing work"), omp's subagent ("While work remains ... continue with another tool call", and
	// not to end "on a plan, a promise of further work, or a question", subagent-system-prompt.md:92,98) and Hermes's
	// TASK_COMPLETION_GUIDANCE (agent/prompt_builder.py:383-392).
	"- Finish the whole task. Errors, timeouts and failing tests are ordinary obstacles: find their cause and " +
	"work through them. Do not end your turn on a plan, an offer to continue or a list of next steps; stop early " +
	"only when nothing can move without the owner, and then use `ask` or a `gate`.\n" +
	"- When the work is done, call the `finish` tool exactly once, then end your turn. A task ended without " +
	"`finish` counts as failed.\n" +
	// Issue #666: the shape of the report, after Orca's worker_done body, "what you did, what you found, what's
	// left" (src/main/runtime/orchestration/workers/launch/worker-dispatch-preamble.ts:96-103), and OpenAI's GPT-6
	// guide's short handoff, "what changed, what was checked, and what still needs attention". Issue #672: a
	// failure said first, after Orca's "Use --outcome failed if the work is not done; never hide failure in prose"
	// (same file, :98-100); written for a reader who saw none of the work, after Anthropic's Fable 5.2 page ("The
	// person reading has not seen your tool calls, your reasoning or the files you opened").
	"- Keep the summary short, and write it for someone who has seen none of your work: the answer first, and " +
	"when the work is not done, say so in its first line; then what you did, what you found and what is left; " +
	"which checks you ran and which you skipped; then the evidence (paths with lines, commands with their " +
	"output, the pull request as its URL).\n" +
	"- If the owner must decide before the work goes on (such as whether to merge), " +
	"pass that question as `gate` to `finish` instead of waiting.\n" +
	"- Do not start subagents, and do not work outside this directory.\n" +
	// Issue #513, after OpenClaw's and Hermes's AGENTS.md (verify the claim against source and observed
	// behavior; close as implemented_on_main or cannot_reproduce) and Coder's ("state uncertainty instead of
	// guessing").
	"- The task, an issue, a document or a memory is a claim. Before you build on it, check it in the code of " +
	"the base branch, or on the system the task names, and cite what you saw (a path and line, a command and " +
	"its output). When it is false (the work is already there, the problem does not reproduce), change nothing " +
	"and call `finish` with that evidence. When you are unsure, say so instead of guessing.\n"

// Prompt is the worker's first turn, after Orca's dispatch preamble
// (src/main/runtime/orchestration/workers/launch/worker-dispatch-preamble.ts):
// who it is and the rules, the same for every worker; the profile's
// instruction, the accounts and phone of the Run and the memories; where to
// work; the task; and last the recovery note, a follow-up and a resolved
// Gate, the newest instruction.
//
// The order is issue #672's. Orca puts its rules first and `=== TASK ===` with
// the spec last (:118-121); omp gives its subagent the rules in the system
// prompt and only the assignment in the user message
// (packages/coding-agent/src/prompts/system/subagent-user-prompt.md:1-3);
// Anthropic's prompting guide puts long context first and the query at the
// end, and OpenAI's caching guide stable instructions first and what varies
// last. That the recovery note, a follow-up and a Gate come after the spec
// follows Anthropic's Fable 5.2 page, which puts what changed in the newest
// turn; the exact order is inferred.
func Prompt(d statedb.Dispatch) string {
	o := d.Offer

	var b strings.Builder

	b.WriteString("You are a worker dispatched by Hypomnema, a software factory. ")
	b.WriteString("Carry out the task at the end of this message on your own.\n")
	b.WriteString(rules)

	// The profile's instruction comes after the rules it may not override.
	if s := strings.TrimSpace(o.Instruction); s != "" {
		b.WriteString("\nFor this kind of task:\n" + s + "\n")
	}

	// The accounts the Run was given, and the command each is used with (issue #103); the phone (issue #417).
	b.WriteString(agenttool.Brief(o.Uses, o.Device))
	b.WriteString(memoryBrief(o.Memory, o.OwnerMemory, o.AgentKey != ""))

	if o.Repo != "" {
		fmt.Fprintf(&b, "\nWork only in this directory, a git worktree of %s on branch %s, started from %s. ",
			o.Repo, worktree.Branch(o.TaskID), o.Base)
		// The command is the host's (internal/shared/githost): gh on GitHub, glab on GitLab.
		fmt.Fprintf(&b, "Commit your work, push the branch, and open a pull request with `%s`.\n",
			openCommand(o.RepoKind, o.Base, worktree.Branch(o.TaskID)))
	} else {
		b.WriteString("\nWork only in this directory.\n")
	}

	fmt.Fprintf(&b, "\nTask (%s):\n%s\n", o.Profile, o.Spec)

	if d.Accepted != nil && d.Accepted.Recovery {
		b.WriteString("\n" + recoveryNote + "\n")
	}

	if f := followUp(d); f != "" {
		b.WriteString("\n" + f)
	}

	if g := gate(d); g != "" {
		b.WriteString("\n" + g)
	}

	return b.String()
}

// openCommand is the host's command to open a pull request. An offer with a
// repository of a kind this node does not know never starts a worker
// (worktree.SourceOf); GitHub's command only keeps this total.
func openCommand(kind, base, branch string) string {
	adapter, ok := githost.For(githost.Kind(kind))
	if !ok {
		adapter, _ = githost.For(githost.GitHub)
	}

	return adapter.OpenCommand(base, branch)
}

// followUp tells the worker of a Task that had succeeded what more is asked
// of it (issue #666), as a resolved Gate is told. The Conversation goes on in
// the same worktree, so the worker updates its own pull request (Polly sends
// fixes "to the SAME implementer conversation ... so the worker keeps its
// worktree/branch context and updates its existing PR", omnigent
// examples/polly/skills/cross-review/SKILL.md:50-56).
func followUp(d statedb.Dispatch) string {
	if d.Accepted == nil || strings.TrimSpace(d.Accepted.FollowUp) == "" {
		return ""
	}

	next := "Continue in this directory, then call `finish` again.\n"
	if d.Offer.Repo != "" {
		next = "Continue in this worktree and on this branch; update the same pull request, then call `finish` again.\n"
	}

	return "You finished this task before, and more is asked of it now:\n" +
		strings.TrimSpace(d.Accepted.FollowUp) + "\n" + next
}

func gate(d statedb.Dispatch) string {
	if d.Accepted == nil || d.Accepted.Gate == nil || d.Accepted.Gate.Answer == nil {
		return ""
	}

	q, a := d.Accepted.Gate, d.Accepted.Gate.Answer
	answer := a.Text

	if a.Option != nil && *a.Option >= 0 && *a.Option < len(q.Options) {
		answer = strings.TrimSpace(q.Options[*a.Option].Label + " " + a.Text)
	}

	// A plain sentence rather than Orca's upper-case "DECISION GATE RESOLVED" (issue #672, after Anthropic's
	// prompting guide on emphatic wording).
	return fmt.Sprintf("The owner answered the question you asked at `finish`.\nYou asked: %s\nThe answer: %s\n"+
		"Continue from there.\n", q.Text, answer)
}

// memoryGuide says what a worker keeps, after Coder's guidance for its
// project memory (coderd/x/chatd/chattool/memory.go, memoryGuidance).
const memoryGuide = "Keep in memory, with the `memory` tool, what will matter in later tasks: the people and how " +
	"they work, corrections you were given and approaches that were confirmed, decisions the code and its history do not " +
	"show, where to find things outside the repository. Do not keep what the code, its AGENTS.md or these " +
	"instructions already say, nor what matters to this task only, nor any secret. `agent` is the memory of the " +
	"agent you run as, shared by all its workers; `owner` is what every agent and Bot knows about the owner. A " +
	"memory may be stale or wrong: check it before relying on it.\n"

// memoryBrief gives a worker its agent's memory and the one about the owner
// (issue #21), as they were when the offer was made: in the first message,
// not the system prompt, so the prompt the provider caches stays the same for
// every worker (Coder sends its memory index as a message for that reason),
// and not changed while the worker runs (Hermes freezes it at session start).
// The guide is given only to a worker that has the tool to write.
func memoryBrief(agent, owner string, canWrite bool) string {
	var b strings.Builder

	for _, m := range []struct{ title, text string }{
		{"Memory of your agent", agent}, {"About the owner", owner},
	} {
		if text := strings.TrimSpace(m.text); text != "" {
			b.WriteString("\n" + m.title + ":\n")

			for line := range strings.SplitSeq(text, "\n") {
				b.WriteString("- " + line + "\n")
			}
		}
	}

	if canWrite {
		b.WriteString("\n" + memoryGuide)
	}

	return b.String()
}
