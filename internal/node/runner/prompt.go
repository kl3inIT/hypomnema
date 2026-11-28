package runner

import (
	"fmt"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/node/statedb"
	"github.com/kl3inIT/hypomnema/internal/node/worktree"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
	"github.com/kl3inIT/hypomnema/internal/shared/githost"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// recoveryNote is the orchestration core's RecoveryNote: the previous
// attempt's outcome is unknown.
const recoveryNote = "Your previous attempt at this task was interrupted and its outcome is unknown. " +
	"Check the worktree, the branch and any pull request before redoing work."

// rules are the worker's instructions for its tools and its work, the same
// for every worker (Orca's preamble forbids AskUserQuestion and subagents and
// requires worker_done exactly once).
const writeRules =
// Issue #672: one decision boundary, stated once, in place of "when a decision is not yours to make": what a
// worker decides itself and what needs `ask` (GPT-5.6 Sol's prompt guidance, "Require confirmation for external
// writes, destructive actions ... or a material expansion of scope"; OpenAI's GPT-6 guide, "replacing blanket
// 'always ask' rules with clear boundaries"; OpenClaw's template "Do freely / Ask first",
// docs/reference/templates/AGENTS.md L19-23). The list is the owner's (2026-11-17). "Within what the task and
// your instructions allow" keeps an explorer's or a reviewer's own instruction, which changes nothing.
// Issue #714, decision 1: merging left the list; the worker turns on the host's auto-merge where the
// repository waits for its required checks (pullRequest), and never merges directly.
// Issue #1069: the options a task leaves open, such as how to fix a review finding, are the worker's too, and it
// presents the choice after making it: pstack skills/principle-never-block-on-the-human/SKILL.md:9-15 ("Proceed,
// then present", reversible work proceeds, "Product direction comes from the human. Execution should not
// block"), OpenClaw's "Do freely / Ask first" above. Asking when no option keeps the goal is pstack's product
// direction (owner, 2026-11-22). Workers had asked the coordinator four such questions in an hour, and it forwarded
// each to the owner, who took the option the worker had already recommended. This rule makes fewer such questions
// start; the coordinator guide's Questions stops the forward when a worker still asks.
"- Decide yourself, within what the task and your instructions allow: how to build it and where the code " +
	"goes, which tests to add or change, running the repository's checks, committing and pushing your branch, " +
	"opening or updating this task's pull request and turning on its auto-merge as said below. Never merge it " +
	"directly. The options the task leaves open, such as how to fix a review finding or which limit or library " +
	"to use, are yours too: take the one most references take, or the one you would recommend, say in the pull " +
	"request which you took and what it is learned from, and do not ask about it. " +
	"Ask first, with the `ask` tool, and wait for the answer: " +
	"before changing the task's scope, deploying or changing a server, deleting what your change did " +
	"not make, or acting outside this directory and this task's pull request, and when no option keeps the " +
	"task's goal. Never use AskUserQuestion; no one reads it.\n" +
	"- Do not end your turn on a plan, an offer to continue or a list of next steps when the Task asks for a change.\n" +
	// Issue #894, this repository's AGENTS.md rule of 2026-11-19 given to every repository: the touched checks
	// once before pushing, after t3code AGENTS.md:102,111, OpenClaw AGENTS.md:86 and CONTRIBUTING.md:94, Orca
	// AGENTS.md:8 and Coder AGENTS.md:46.
	"- Run the checks of what you changed, once, after your last edit or merge and before pushing; CI runs the " +
	"full suite. A repository's own instructions win where they say otherwise.\n" +
	// Issue #894: merge the base only when needed, after OpenClaw AGENTS.md:46 and oh-my-opencode AGENTS.md:63.
	"- Merge the base branch into yours only for a conflict, a red check whose cause is on the base, or when the " +
	"repository's rules ask (such as a migration number); then rerun only the checks the merge affects.\n" +
	// Issue #894: one blocking command to wait for CI, after Orca AGENTS.md:12-14, Coder
	// .agents/skills/pull-requests/SKILL.md:65, Hermes
	// skills/software-development/github/references/ci-troubleshooting.md:181, oh-my-opencode
	// .agents/skills/work-with-pr/SKILL.md:199-212 and t3code AGENTS.md:159. Not waiting when a review comes first
	// has no reference of its own: the coordinator's review step waits for CI itself
	// (internal/controlplane/clientcli/skills/hypomnema-coordinator/references/review.md; owner, 2026-11-19).
	// Issue #1053: once auto-merge is on, the host lands the head when it is ready, so the worker does not wait
	// either: pstack arms --auto on a verified head and reads the PR back (skills/poteto-mode/playbooks/shipping.md:8),
	// and a watcher, not the step that armed it, follows it to merged (shipping.md:9); here the control plane's
	// (orchestration/pullrequests.go). One rule says when to wait; the merge steps below say whether Hypomnema
	// follows an auto-merge, which it does only with the repository's token (review round 2), and give the command.
	"- Wait for CI only when the task needs its result or the merge steps below say to. When the task says a " +
	"review comes first, do not wait: report the pushed commit and finish; the coordinator follows CI. When you " +
	"wait, use one command that blocks until it ends (the host's watch command, as in the merge steps below when " +
	"they are given), never a polling loop.\n" +
	// Issue #952: a flake earns one fresh build and only the diff's own failures get a commit, after pstack
	// skills/poteto-mode/playbooks/babysit.md:8; the flaky test is fixed in its own change, after
	// playbooks/autonomous-run.md:9. The issue is the owner's allowance (owner, 2026-11-19).
	"- Fix only the failures your diff causes. A red check that fails outside your diff and is also red on the " +
	"base, or green on one rerun, is a flake: rerun it once, do not fix it in this pull request, and open an issue " +
	"for the flaky test or comment on the one that exists (yours to do without asking).\n" +
	// Issue #952: the failing test lands before the fix, so a disputed finding is settled by running it, after
	// pstack skills/poteto-mode/playbooks/bug-fix.md:11 and skills/tdd/SKILL.md:9. The exception is theirs too:
	// bug-fix.md:11 "Skip it when the test would be expensive, integration-heavy, or unclear", tdd/SKILL.md:11.
	"- For a bug fix, commit a test that fails without the fix first, then the fix; skip it when no cheap test " +
	"reaches the bug, and say so.\n"

const rules =
// Issue #672: Orca's worker "needs no progress updates unless the task asks"
// (src/main/runtime/orchestration/workers/launch/worker-dispatch-preamble.ts:71-73); omp's subagent, "No TODO
// tracking, no progress updates" (packages/coding-agent/src/prompts/system/subagent-system-prompt.md:90).
"- No one reads what you write before `finish`, so no progress messages are needed.\n" +
	// Issue #895: Hermes hermes_bootstrap.py:455-465 exports a scratch path; Orca
	// src/main/acp/launch/acp-private-temp.ts:36-40,73-85 gives each run a folder and removes it on exit.
	// Codex codex-rs/linux-sandbox/src/linux_run_main.rs:59-61 uses bubblewrap for security isolation, not this.
	"- Keep temporary files and directories under $TMPDIR, private to this task and removed after it ends. " +
	"Never use /tmp or another hard-coded temp path, even for databases, sockets or clones you start yourself.\n" +
	// Issue #672: finish the whole task, after Anthropic's Opus 5.5 page (its text for unattended runs) and Fable
	// 5.2 page ("Finishing work"), omp's subagent ("While work remains ... continue with another tool call", and
	// not to end "on a plan, a promise of further work, or a question", subagent-system-prompt.md:92,98) and Hermes's
	// TASK_COMPLETION_GUIDANCE (agent/prompt_builder.py:383-392).
	"- Finish the whole task. Errors, timeouts and failing tests are ordinary obstacles: find their cause and " +
	"work through them. Stop early " +
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
	// Issue #1219: the Gate stands alone, after the summary rule above, Codex's request_user_input (1-3 questions,
	// each option "One short sentence explaining impact/tradeoff if selected", the recommended one first;
	// codex-rs/core/src/tools/handlers/request_user_input_spec.rs:20-35,72) and Gemini CLI's plan mode ("pros/cons
	// and your recommendation", "detailed descriptions for each option to help the user understand the
	// implications"; packages/core/src/prompts/snippets.ts:606-607,633); the reference of each option is this
	// repository's "From" rule (AGENTS.md, "Do not invent logic").
	"- If the owner must decide before the work goes on (such as a choice between designs), " +
	"pass that question as `gate` to `finish` instead of waiting. The owner reads the gate on its own, without " +
	"your report, and may not remember the work: ask one decision a question, at most three, each naming the " +
	"case it is about; give each option a sentence on what changes if it is chosen and the reference it is " +
	"learned from, or say that no reference does it; put the option you recommend first.\n" +
	// Issue #513, after OpenClaw's and Hermes's AGENTS.md (verify the claim against source and observed
	// behavior; close as implemented_on_main or cannot_reproduce) and Coder's ("state uncertainty instead of
	// guessing").
	"- The task, an issue, a document or a memory is a claim. Before you build on it, check it in the code of " +
	"the base branch, or on the system the task names, and cite what you saw (a path and line, a command and " +
	"its output). When it is false (the work is already there, the problem does not reproduce), change nothing " +
	"and call `finish` with that evidence. When you are unsure, say so instead of guessing.\n" +
	// Issue #719, the owner's rule of this repository's AGENTS.md ("Research before design", "Do not invent
	// logic") given to workers on every repository (owner, 2026-11-17).
	"- For a task that asks for research or a design, clone and read the code of the references, not only their " +
	"documents, and cite the paths and lines you read.\n"

// Prompt is the worker's first turn, after Orca's dispatch preamble
// (src/main/runtime/orchestration/workers/launch/worker-dispatch-preamble.ts):
// who it is, the rules and the "Tools" block, the same for every worker with
// the same tools; the profile's
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
	readOnly := o.RoleRouted && o.Profile != "implementer"

	// Issue #952: a read-only worker may also use its scratch copy (below), so every "only here" line names it.
	here := "this directory"
	if readOnly {
		here = "this directory and your scratch copy under $TMPDIR"
	}

	var b strings.Builder

	b.WriteString("You are a worker dispatched by Hypomnema, a software factory. ")
	b.WriteString("Carry out the task at the end of this message on your own.\n")
	b.WriteString("\nRules:\n")
	if readOnly {
		b.WriteString("- Read and report. Do not edit project files, commit, push, open a pull request, " +
			"or use AskUserQuestion.\n")
		// Issue #952: run the code to check a claim, after orchestrate skills/orchestrate/prompts/verifier.md:23
		// ("Run the code. Reading the diff is not verification") and pstack skills/interrogate/SKILL.md:19 ("Keep
		// repros in your own worktree"). $TMPDIR is the Dispatch's own (issue #895).
		b.WriteString("- You may run code to check a claim (the tests, a repro script) in a scratch copy of this " +
			"worktree under $TMPDIR. Leave the tracked files here unchanged.\n")
		b.WriteString("- Deliver the complete answer, plan or review requested in the Task through `finish`.\n")
	} else {
		b.WriteString(writeRules)
	}
	if o.RoleRouted && o.Profile == "planner" {
		// Codex codex-rs/core/src/tools/handlers/multi_agents_spec.rs:738-745; owner, 2026-11-18.
		b.WriteString("- Keep analysis and blocking work here. You may use this CLI's subagents for independent " +
			"read-only lookups. Do not dispatch separate Hypomnema Tasks or work outside " + here + ".\n")
	} else {
		b.WriteString("- Do not start subagents, and do not work outside " + here + ".\n")
	}
	b.WriteString(rules)

	// The "Tools" block (issue #723, decisions 2.2 to 2.4): built from the tools the worker gets alone, so it is the
	// same for every worker with the same tools and sits with the rules, before anything of the task.
	if guide := agenttool.ToolGuide(o.Tools); guide != "" {
		b.WriteString("\n" + guide)
	}

	// The profile's instruction comes after the rules it may not override.
	if s := strings.TrimSpace(o.Instruction); s != "" {
		b.WriteString("\nFor this kind of task:\n" + s + "\n")
	}

	// The accounts the Run was given, and the command each is used with (issue #103). The phone (issue #417) is a
	// family of the "Tools" block (issue #789).
	b.WriteString(agenttool.Brief(o.Uses))
	b.WriteString(memoryBrief(o.Memory, o.OwnerMemory, o.MemoryNotes, o.AgentKey != ""))

	if o.Repo != "" {
		b.WriteString(repoBrief(o, readOnly, here))
	} else {
		b.WriteString("\nWork only in " + here + ".\n")
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

// autoMerge tells the worker how the host merges its pull request by itself
// (issue #714): the decisions of the issue's design, in the host's commands
// (githost.AutoMerge). The worker turns auto-merge on, instead of asking,
// only when the request is not a draft, has no conflict, the task does not
// say a review or the owner comes first, and the repository waits for its
// required checks; t3code and Orca offer auto-merge only when the repository
// allows it and never on a draft or a conflict (PullRequestDetailPanel.tsx:
// 1303-1312; pull-request-auto-merge-availability.ts:13-34). Without a
// required check or with auto-merge off, the request stays open and the
// summary says what is missing, as Kodiak and bors refuse
// (evaluation.py:718-723; bors_toml.ex:157-158); no gate is opened only to ask
// to merge. A conflict is resolved keeping both sides' intent and checked
// again, after t3code's prompt (pullRequestDetail.logic.ts:1172-1189), by
// merging the base, as this repository's AGENTS.md does. What the
// repository's instructions ask right before a merge is done before
// auto-merge is turned on, since it lands whatever head is ready (issue
// #1053; this repository's AGENTS.md runs `make migration-fix` there, #954).
// Once auto-merge is on, the worker reads the request back, as a requested
// auto-merge is not a merge, and finishes without waiting for the checks:
// pstack arms --auto on a verified head and reads it back
// (skills/poteto-mode/playbooks/shipping.md:8), and a watcher follows it to
// merged (shipping.md:9), here the control plane, which sends a follow-up
// when it goes red or conflicts (issue #1053, orchestration/pullrequests.go). Before #1053 the worker stayed
// until the checks were green, after OpenClaw's "until verified merge"
// (landing.md:82). Without auto-merge it waits only when the task needs the
// checks' result.
//
// The offer names the repository by its path, with its host apart
// (orchestration's Offer); it is read as worktree.SourceOf reads it, so a
// GitLab subgroup and a company's host are kept. An offer without a host is
// one of before hosts existed, on github.com.
//
// app says the repository's token is the owner's GitHub App's (issue #1262):
// the worker waits for CI with `hypomnema ci wait <url>`, which the App's
// events end, instead of polling GitHub with gh.
func autoMerge(kind, host, path, base, branch string, followed, app bool) string {
	if host == "" {
		host = githost.GitHubHost
	}

	r, err := githost.Parse("https://" + host + "/" + path)
	adapter, ok := githost.For(githost.Kind(kind))

	if err != nil || !ok {
		return ""
	}

	m := adapter.AutoMerge(r, base)

	var b strings.Builder

	b.WriteString("Then have the host merge it by itself once its required checks pass; never merge it directly. " +
		"When the task says a review comes first, that the owner merges, or to ask before merging, skip this: " +
		"leave the pull request open and say so in the summary. Otherwise:\n")

	b.WriteString("1. Check, read-only, that the repository waits for its checks (auto-merge without them merges " +
		"at once, before CI):")

	lacks := make([]string, 0, len(m.Ready))

	for _, c := range m.Ready {
		cmds := make([]string, 0, len(c.Commands))
		for _, cmd := range c.Commands {
			cmds = append(cmds, "`"+cmd+"`")
		}

		fmt.Fprintf(&b, " %s: %s;", strings.Join(cmds, " and "), c.Shows)
		lacks = append(lacks, "\""+c.Lacks+"\"")
	}

	fmt.Fprintf(&b, " if one does not hold, do not turn auto-merge on and do not merge: leave the pull request "+
		"open and say in the summary's first line what the repository lacks (%s), so the owner can change the "+
		"setting or merge; do not open a gate only to ask whether to merge.\n", strings.Join(lacks, " or "))

	fmt.Fprintf(&b, "2. Read the pull request with `%s`. A draft (%s) is left open. On a conflict (%s), merge %s "+
		"into your branch, resolve it keeping the intent of both sides, rerun the checks the merge affects and "+
		"push; if you cannot tell what one side meant, ask. Then do what the repository's instructions ask right "+
		"before a merge, such as merging %s and numbering a migration after its last, and push that too.",
		m.View, m.Draft, m.Conflict, base, base)

	if m.Before != "" {
		fmt.Fprintf(&b, " Before step 3, check that %s.", m.Before)
	}

	fmt.Fprintf(&b, "\n3. Turn auto-merge on with `%s`, where <sha> is the commit you pushed", m.Enable)

	if m.Method != "" {
		b.WriteString("; " + m.Method)
	}

	watch := strings.ReplaceAll(m.Watch, "<branch>", branch)
	if app {
		watch = "hypomnema ci wait <url>"
	}

	if !followed {
		// Without the repository's token the worker uses the node's own login, which the control plane cannot
		// read the request with: the worker stays until the checks are green, as before #1053, after OpenClaw's
		// "until verified merge" (landing.md:82); a red check outside the diff is a flake (writeRules), after
		// pstack babysit.md:8 (issue #1053, review round 2; coordinator's choice).
		fmt.Fprintf(&b, ".\n4. Wait for the required checks with `%s`; fix a red one your diff causes and push, "+
			"rerun a flake once as the rules say, and do step 2 again if a conflict appears. Once they are green, "+
			"call `finish` without waiting longer, saying whether the pull request merged (`%s`) or that its checks "+
			"are green with auto-merge on.\n", watch, m.Merged)

		return b.String()
	}

	fmt.Fprintf(&b, ".\n4. Read the pull request back with `%s`, since a requested auto-merge is not a merge, "+
		"then call `finish` at once, without waiting for the checks, saying that it merged or \"auto-merge on at "+
		"<sha>\". The control plane follows the pull request from there and sends you a follow-up if a check goes "+
		"red or a conflict appears.\nWhen auto-merge is not turned on, wait for the required checks with `%s` "+
		"only when the task needs their result.\n",
		m.Merged, watch)

	return b.String()
}

// followUp tells the worker of a Task that had succeeded, or had stopped at a
// Gate, what more is asked of it (issues #666, #714), as a resolved Gate is
// told. A follow-up to a Gate replaces the Gate, so it is the only answer. The Conversation goes on in
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

	return "You called `finish` on this task before, and more is asked of it now (this replaces the answer to " +
		"any question you asked at `finish`):\n" +
		strings.TrimSpace(d.Accepted.FollowUp) + "\n" + next
}

func gate(d statedb.Dispatch) string {
	if d.Accepted == nil || d.Accepted.Gate == nil || d.Accepted.Gate.Answer == nil {
		return ""
	}

	ds := decisions(*d.Accepted.Gate)

	// A plain sentence rather than Orca's upper-case "DECISION GATE RESOLVED" (issue #672, after Anthropic's
	// prompting guide on emphatic wording).
	if len(ds) == 1 {
		return fmt.Sprintf("The owner answered the question you asked at `finish`.\nYou asked: %s\nThe answer: %s\n"+
			"Continue from there.\n", ds[0].text, ds[0].answer)
	}

	// A Gate of several decisions (issue #1219) is told one decision after another, each with its answer.
	var b strings.Builder

	b.WriteString("The owner answered the questions you asked at `finish`.\n")

	for _, q := range ds {
		fmt.Fprintf(&b, "- %s: %s\n  The answer: %s\n", q.header, q.text, q.answer)
	}

	b.WriteString("Continue from there.\n")

	return b.String()
}

// memoryGuide says what a worker keeps, after Coder's guidance for its
// project memory (coderd/x/chatd/chattool/memory.go, memoryGuidance).
const memoryGuide = "Keep in memory, with the `memory` tool, what will matter in later tasks: the people and how " +
	"they work, corrections you were given and approaches that were confirmed, decisions the code and its history do not " +
	"show, where to find things outside the repository. Do not keep what the code, its AGENTS.md or these " +
	"instructions already say, nor what matters to this task only, nor any secret. Your agent's memory is the " +
	"directory /memories, shared by all its workers: MEMORY.md is the memory of your agent above, a line for each " +
	"note; USER.md is what every agent and Bot knows about the owner; a longer subject goes in a note of its own, " +
	"read when it comes up. A memory may be stale or wrong: check it before relying on it.\n"

// memoryBrief gives a worker its agent's memory and the one about the owner
// (issue #21), as they were when the offer was made: in the first message,
// not the system prompt, so the prompt the provider caches stays the same for
// every worker (Coder sends its memory index as a message for that reason),
// and not changed while the worker runs (Hermes freezes it at session start).
// The notes (issue #589) are listed, not given: a worker reads one with the
// memory's tool, so they and the guide go only to a worker that has it.
func memoryBrief(agent, owner string, notes []string, hasTool bool) string {
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

	if !hasTool {
		return b.String()
	}

	if len(notes) > 0 {
		b.WriteString("\nNotes of your agent's memory, read with the `memory` tool's view when their subject comes " +
			"up:\n")

		for _, n := range notes {
			b.WriteString("- " + n + "\n")
		}
	}

	b.WriteString("\n" + memoryGuide)

	return b.String()
}

// repoBrief says where the worker works and where its commits go. A Task that continues another's branch pushes into
// it, so that branch's pull request takes the commits and keeps its number and reviews, as Orca's worktree for a pull
// request pushes to its head (pr-start-point.ts:226-245); one that starts from another branch opens its pull request
// against the base it was given, as t3code's stacked work on a parent branch (orchestrator-mcp-server.md:354-355)
// (issue #690).
func repoBrief(o nodeproto.Offer, readOnly bool, here string) string {
	var b strings.Builder

	// Issue #1259 follows GitHub's REST API rate limit guidance for all repository roles.
	if githost.Kind(o.RepoKind) == githost.GitHub {
		b.WriteString("\nIf GitHub reports a rate limit, run `gh api rate_limit` on the affected host. " +
			"This call does not count against the primary limit. " +
			"Wait until the exhausted quota resets (`X-RateLimit-Reset`) before resuming. " +
			"For a secondary limit, honor `Retry-After` if present. If neither delay is given for a secondary limit, " +
			"wait at least one minute before resuming. Report the limit and the wait in your summary.\n")
	}

	own, push, base := worktree.Branch(o.TaskID), pushBranch(o), prBase(o)

	fmt.Fprintf(&b, "\nWork only in %s", here)
	if readOnly {
		b.WriteString("; this directory is")
	} else {
		b.WriteString(",")
	}
	fmt.Fprintf(&b, " a git worktree of %s on branch %s, started from %s. ",
		o.Repo, own, startBranch(o))

	// The command is the host's (internal/shared/githost): gh on GitHub, glab on GitLab.
	switch {
	case readOnly:
		b.WriteString("Read the repository here; leave it unchanged.\n")
	case push != own:
		publish := fmt.Sprintf("push it with `git push origin %s:%s` (never forced)", own, push)
		if canPublish(o) {
			publish = fmt.Sprintf("push it with `publish_branch` (not `git push`, so the Run can prove its PR heads), "+
				"which pushes it to %s", push)
		}

		fmt.Fprintf(&b, "%s is the branch of an earlier Task whose work this Task continues, and of its pull request: "+
			"commit your work and %s, which updates that pull request. Do not open a new one. Find it with `%s`; "+
			"if none is open, open one with `%s`.\n",
			push, publish, listCommand(o.RepoKind, push), openCommand(o.RepoKind, base, push))
		b.WriteString(autoMerge(o.RepoKind, o.RepoHost, o.Repo, base, push, o.RepoToken != "" || o.GitHubApp,
			o.GitHubApp))
	default:
		publish := "push the branch"
		if canPublish(o) {
			publish = "push the branch with `publish_branch` (not `git push`, so the Run can prove its PR heads)"
		}

		fmt.Fprintf(&b, "Commit your work, %s, and open a pull request with `%s`.\n",
			publish, openCommand(o.RepoKind, base, own))
		b.WriteString(autoMerge(o.RepoKind, o.RepoHost, o.Repo, base, own, o.RepoToken != "" || o.GitHubApp,
			o.GitHubApp))
	}

	if !readOnly && o.GitHubApp && o.GitCoAuthor != "" {
		fmt.Fprintf(&b, "\nWhen committing, include this trailer on its own line after a blank line in the commit message: %q.\n",
			o.GitCoAuthor)
	}

	return b.String()
}

// listCommand is the host's command that lists the open pull requests of a branch.
func listCommand(kind, branch string) string {
	if githost.Kind(kind) == githost.GitLab {
		return "glab mr list --source-branch " + branch
	}

	return "gh pr list --head " + branch + " --state open"
}
