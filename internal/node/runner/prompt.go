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

// rules are the worker's instructions for its tools (Orca's preamble forbids
// AskUserQuestion and subagents and requires worker_done exactly once).
const rules = "\nRules:\n" +
	"- When a decision is not yours to make, call the `ask` tool and wait for the answer. " +
	"Never use AskUserQuestion; no one reads it.\n" +
	"- When the work is done, call the `finish` tool exactly once with a short summary and the evidence " +
	"(the pull request as its URL), then end your turn. A task ended without `finish` counts as failed.\n" +
	"- If the owner must decide before the work goes on (such as whether to merge), " +
	"pass that question as `gate` to `finish` instead of waiting.\n" +
	"- Do not start subagents, and do not work outside this directory.\n"

// Prompt is the worker's first turn, after Orca's dispatch preamble
// (src/main/runtime/orchestration/preamble.ts L52-113): who it is, the task,
// where to work, ask instead of AskUserQuestion, finish exactly once, no
// subagents; then the recovery note and a resolved Gate.
func Prompt(d statedb.Dispatch) string {
	o := d.Offer

	var b strings.Builder

	b.WriteString("You are a worker dispatched by Hypomnema, a software factory. ")
	b.WriteString("Carry out the task below on your own.\n\n")
	fmt.Fprintf(&b, "Task (%s):\n%s\n\n", o.Profile, o.Spec)

	if o.Repo != "" {
		fmt.Fprintf(&b, "Work only in this directory, a git worktree of %s on branch %s, started from %s. ",
			o.Repo, worktree.Branch(o.TaskID), o.Base)
		// The command is the host's (internal/shared/githost): gh on GitHub, glab on GitLab.
		fmt.Fprintf(&b, "Commit your work, push the branch, and open a pull request with `%s`.\n",
			openCommand(o.RepoKind, o.Base, worktree.Branch(o.TaskID)))
	} else {
		b.WriteString("Work only in this directory.\n")
	}

	b.WriteString(rules)
	// The accounts the Run was given, and the command each is used with (issue #103).
	b.WriteString(agenttool.Brief(o.Uses))

	// The profile's instruction comes after the rules it may not override.
	if s := strings.TrimSpace(o.Instruction); s != "" {
		b.WriteString("\nFor this kind of task:\n" + s + "\n")
	}

	b.WriteString(memoryBrief(o.Memory, o.OwnerMemory, o.AgentKey != ""))

	if d.Accepted != nil && d.Accepted.Recovery {
		b.WriteString("\n" + recoveryNote + "\n")
	}

	if g := gate(d); g != "" {
		b.WriteString("\n" + g)
	}

	return b.String()
}

// openCommand is the host's command to open a pull request; an offer from
// before hosts existed names no kind and means GitHub.
func openCommand(kind, base, branch string) string {
	adapter, ok := githost.For(githost.Kind(kind))
	if !ok {
		adapter, _ = githost.For(githost.GitHub)
	}

	return adapter.OpenCommand(base, branch)
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

	return fmt.Sprintf("DECISION GATE RESOLVED. You asked: %s\nThe answer: %s\nContinue from there.\n", q.Text, answer)
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
