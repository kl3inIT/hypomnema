package orchestration

import (
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// reviewerWorld is the owner's reviewers as deploy/profiles has them (issue #756): `reviewer` on the Codex
// subscription, with `reviewer-claude` on the Claude subscription as its fallback, both served by the gateway under
// the accounts the control plane keeps, and a node that runs both CLIs.
func reviewerWorld(t *testing.T) (*world, Node, Profile) {
	t.Helper()

	w := newSchedWorld(t)
	ctx := t.Context()

	w.gatewayAccount("claude")
	w.gatewayAccount("codex")

	both := w.addNode("node_both", 4, "claude", "codex")
	require.NoError(t, w.s.DrainNode(ctx, w.node.ID))

	backup, _, err := w.s.ApplyProfile(ctx, "", deployProfile(t, "reviewer-claude.yaml"))
	require.NoError(t, err)

	codex := strings.NewReplacer("<gateway provider>", "codex-subscription", "<gateway model>", "gpt-6.1-sol").
		Replace(deployProfile(t, "reviewer-codex.yaml"))
	reviewer, _, err := w.s.ApplyProfile(ctx, "", codex)
	require.NoError(t, err)
	require.Equal(t, []string{"reviewer-claude"}, reviewer.Fallbacks)

	return w, both, backup
}

func deployProfile(t *testing.T, name string) string {
	t.Helper()

	raw, err := os.ReadFile("../../../deploy/profiles/" + name) //nolint:gosec // a file of the repository's samples
	require.NoError(t, err)

	return string(raw)
}

// Scenario: every Codex account the gateway serves the reviewer under waits for its limit (issue #756; zm,
// 2026-11-17). A review that has not started starts under reviewer-claude, its Dispatch marked as the fallback of
// `reviewer` until the first account is free; it does not start a Codex worker the gateway would only refuse. Once
// the limit has reset, a new review runs under the Codex reviewer again.
func TestAReviewWhoseCodexAccountsCoolStartsUnderTheClaudeReviewer(t *testing.T) {
	t.Parallel()

	w, both, backup := reviewerWorld(t)
	ctx := t.Context()

	resets := t0.Add(90 * time.Minute)
	require.NoError(t, w.s.CoolAccount(ctx, "codex", "acc_gateway_codex", resets, "usage limit"))

	review := w.tasks(NewTask{Ref: "r", Profile: "reviewer", Spec: "review PR #709"})[0]

	out := w.schedule()
	require.Len(t, out.Created, 1)

	d := w.dispatchOf(out.Created[0])
	assert.Equal(t, review.ID, d.TaskID)
	assert.Equal(t, backup.VersionID, d.ProfileVersionID)
	assert.Equal(t, "reviewer", d.FallbackFor)
	require.NotNil(t, d.FallbackUntil)
	assert.Equal(t, resets, *d.FallbackUntil)
	assert.Equal(t, "claude", w.conversation(d.ConversationID).Runtime)
	assert.Equal(t, "claude-subscription/claude-opus-5-5", d.Model)
	assert.Equal(t, both.ID, d.NodeID)

	require.Len(t, out.Escalations, 1)
	assert.Equal(t, EscalationFallback, out.Escalations[0].Kind)

	// The profiles say so too: the coordinator sees the reviewer's accounts cooling until the reset.
	profiles, err := w.s.ListProfiles(ctx)
	require.NoError(t, err)

	i := slices.IndexFunc(profiles, func(p Profile) bool { return p.Name == "reviewer" })
	require.GreaterOrEqual(t, i, 0)
	assert.Equal(t, ReasonAccountsCooling, profiles[i].Availability.Reason)
	require.NotNil(t, profiles[i].Availability.At)
	assert.Equal(t, resets, *profiles[i].Availability.At)

	w.advance(91 * time.Minute)
	w.alive(both)
	w.tasks(NewTask{Ref: "later", Profile: "reviewer", Spec: "review PR #725"})

	later := w.schedule().Created
	require.Len(t, later, 1)

	again := w.dispatchOf(later[0])
	assert.Empty(t, again.FallbackFor)
	assert.Equal(t, "codex", w.conversation(again.ConversationID).Runtime)
}

// Scenario: the limit comes while a Codex review is under way (issue #756). Its worker ends as rate_limited with the
// reset the CLI wrote, and the gateway has cooled its accounts: the review is handed to reviewer-claude in the same
// worktree, not started again on Codex. A Codex profile with no fallback waits for the reset instead, and is not
// started on Codex again before it.
func TestAReviewTheLimitMetUnderWayGoesToTheClaudeReviewer(t *testing.T) {
	t.Parallel()

	w, both, backup := reviewerWorld(t)
	ctx := t.Context()

	_, _, err := w.s.ApplyProfile(ctx, "",
		"name: codex-alone\ndescription: d\nruntime: codex\nmodel: {provider: codex-subscription, name: gpt-6.1-sol}\n")
	require.NoError(t, err)

	tasks := w.tasks(
		NewTask{Ref: "r", Profile: "reviewer", Spec: "review PR #730"},
		NewTask{Ref: "alone", Profile: "codex-alone", Spec: "alone"},
	)

	created := w.schedule().Created
	require.Len(t, created, 2)

	byTask := map[string]Dispatch{}
	for _, id := range created {
		d := w.dispatchOf(id)
		byTask[d.TaskID] = d
		assert.Empty(t, d.FallbackFor, "the Codex accounts are free")
	}

	first := byTask[tasks[0].ID]
	old := w.conversation(first.ConversationID)

	// What the gateway did when the limit came, then what the node reported of the turns it ended.
	resets := t0.Add(2 * time.Hour)
	require.NoError(t, w.s.CoolAccount(ctx, "codex", "acc_gateway_codex", resets, "usage limit"))
	require.NoError(t, w.finish(w.begin(first.ID), Outcome{Class: FailureRateLimited, ResetsAt: &resets}))
	require.NoError(t, w.finish(w.begin(byTask[tasks[1].ID].ID),
		Outcome{Class: FailureRateLimited, ResetsAt: &resets}))

	out := w.schedule()
	require.Len(t, out.Created, 1, "the Codex profile with no fallback waits")

	d := w.dispatchOf(out.Created[0])
	conv := w.conversation(d.ConversationID)
	assert.Equal(t, tasks[0].ID, d.TaskID)
	assert.True(t, d.Handoff)
	assert.Equal(t, "reviewer", d.FallbackFor)
	assert.Equal(t, backup.VersionID, d.ProfileVersionID)
	assert.Equal(t, both.ID, d.NodeID)
	assert.Equal(t, old.Worktree, conv.Worktree, "the same worktree")
	assert.Equal(t, "claude", conv.Runtime)

	// Until the reset nothing starts on Codex, however often the scheduler passes.
	w.advance(time.Hour)
	w.alive(both)
	assert.Empty(t, w.schedule().Created)
	assert.Equal(t, TaskReady, w.task(tasks[1].ID).Status)

	w.advance(time.Hour + time.Minute)
	w.alive(both)

	after := w.schedule().Created
	require.Len(t, after, 1)
	assert.Equal(t, tasks[1].ID, w.dispatchOf(after[0]).TaskID)
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
