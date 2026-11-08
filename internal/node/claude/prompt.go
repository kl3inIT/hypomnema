package claude

import (
	"fmt"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/node/statedb"
	"github.com/kl3inIT/hypomnema/internal/node/worktree"
)

// recoveryNote is the orchestration core's RecoveryNote: the previous
// attempt's outcome is unknown.
const recoveryNote = "Your previous attempt at this task was interrupted and its outcome is unknown. " +
	"Check the worktree, the branch and any pull request before redoing work."

// Prompt is the worker's first turn, shortened from Orca's dispatch preamble
// (src/main/runtime/orchestration/preamble.ts). The tools to ask and to
// finish come with issue #32's last PR.
func Prompt(d statedb.Dispatch) string {
	o := d.Offer

	var b strings.Builder

	b.WriteString("You are a worker dispatched by Hypomnema, a software factory. ")
	b.WriteString("Carry out the task below on your own.\n\n")
	fmt.Fprintf(&b, "Task (%s):\n%s\n\n", o.Role, o.Spec)

	if o.Repo != "" {
		fmt.Fprintf(&b, "Work only in this directory, a git worktree of %s on branch %s, started from %s. ",
			o.Repo, worktree.Branch(o.TaskID), o.Base)
		b.WriteString("Commit your work, push the branch, and open a pull request with `gh pr create`.\n")
	} else {
		b.WriteString("Work only in this directory.\n")
	}

	b.WriteString("Do not wait for a person: no one can answer you yet. ")
	b.WriteString("End with a short summary of what you did, with the pull request's URL.\n")

	if d.Accepted != nil && d.Accepted.Recovery {
		b.WriteString("\n" + recoveryNote + "\n")
	}

	if g := gate(d); g != "" {
		b.WriteString("\n" + g)
	}

	return b.String()
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
