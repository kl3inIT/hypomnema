package orchestration

import (
	"context"
	"database/sql"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
)

// reviewerWorld is the owner's reviewers as deploy/profiles has them (issue #756): `reviewer` on the Codex
// subscription, with `reviewer-claude` on the Claude subscription as its fallback, both served by the gateway under
// the accounts the control plane keeps, and a node that runs both CLIs.
func reviewerWorld(t *testing.T) (*world, Node, Profile) {
	t.Helper()

	return reviewerWorldOn(t, "codex-subscription", nil)
}

// reviewerWorldOn is reviewerWorld with the Codex reviewer's model on the given provider, gpt-6.1-sol; before is
// done to the world before the profiles are applied.
func reviewerWorldOn(t *testing.T, provider string, before func(*world)) (*world, Node, Profile) {
	t.Helper()

	w := newSchedWorld(t)
	ctx := t.Context()

	w.gatewayAccount("claude")
	w.gatewayAccount("codex")

	both := w.addNode("node_both", 4, "claude", "codex")
	require.NoError(t, w.s.DrainNode(ctx, w.node.ID))

	if before != nil {
		before(w)
	}

	backup, _, err := w.s.ApplyProfile(ctx, "", deployProfile(t, "reviewer-claude.yaml"))
	require.NoError(t, err)

	codex := strings.NewReplacer("<gateway provider>", provider, "<gateway model>", "gpt-6.1-sol").
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

// loginAccount is one more account of the runtime the control plane keeps for the gateway.
func (w *world) loginAccount(id, runtime string) {
	w.t.Helper()

	require.NoError(w.t, store.New(w.db).InsertLoginAccount(w.t.Context(), store.InsertLoginAccountParams{
		ID: id, Runtime: runtime, Label: id, SecretRef: sql.NullString{String: "vault:" + id, Valid: true}, Now: 1,
	}))
}

// Scenario (review of PR #786, finding 1): of the two Codex accounts the control plane keeps, one cools and the
// other's login was refused for good, so the gateway can serve under neither. The one that cannot sign in does not
// hide that the usable one cools: the review starts under reviewer-claude, not on Codex again and again.
func TestAnAccountThatCannotSignInDoesNotHideTheCooling(t *testing.T) {
	t.Parallel()

	w, _, backup := reviewerWorld(t)
	ctx := t.Context()

	w.loginAccount("acc_codex_refused", "codex")
	w.s.SetGatewayLogins(func(context.Context) (map[string]bool, error) {
		return map[string]bool{"acc_codex_refused": true}, nil
	})

	resets := t0.Add(time.Hour)
	require.NoError(t, w.s.CoolAccount(ctx, "codex", "acc_gateway_codex", resets, "usage limit"))
	w.tasks(NewTask{Ref: "r", Profile: "reviewer", Spec: "review PR #738"})

	out := w.schedule()
	require.Len(t, out.Created, 1)

	d := w.dispatchOf(out.Created[0])
	assert.Equal(t, backup.VersionID, d.ProfileVersionID)
	assert.Equal(t, "reviewer", d.FallbackFor)
}

// Scenario (review of PR #786, finding 2): a Claude Code worker on a model of the gateway's own table ends on the
// gateway's 429 with no reset. The scheduler cannot see the accounts behind that model, so the Task, which a
// backup could take over, is held for RateLimitCooldown, not started again at once on every pass.
func TestAGatewayLimitTheSchedulerCannotSeeIsWaitedOut(t *testing.T) {
	t.Parallel()

	w := newSchedWorld(t)
	ctx := t.Context()

	_, _, err := w.s.ApplyProfile(ctx, "",
		"name: other\ndescription: d\nruntime: claude\nmodel: {provider: model-gateway, name: claude-sonnet-5-5}\n")
	require.NoError(t, err)
	_, _, err = w.s.ApplyProfile(ctx, "", "name: aliased\ndescription: d\nruntime: claude\n"+
		"model: {provider: model-gateway, name: claude-sonnet-5-5}\nfallbacks: [other]\n")
	require.NoError(t, err)

	task := w.tasks(NewTask{Ref: "a", Profile: "aliased", Spec: "a"})[0]
	created := w.schedule().Created
	require.Len(t, created, 1)
	require.NoError(t, w.finish(w.begin(created[0]), Outcome{Class: FailureRateLimited}))

	require.NotNil(t, w.task(task.ID).NotBefore)
	assert.Equal(t, t0.Add(RateLimitCooldown), *w.task(task.ID).NotBefore)
	assert.Empty(t, w.schedule().Created, "not started again before the wait")

	w.advance(RateLimitCooldown + time.Second)
	w.alive(w.node)
	assert.Len(t, w.schedule().Created, 1, "then it runs again")
}

// Scenario (review of PR #786, finding 3): the request tried both Codex accounts; the first resets in an hour, the
// second later, and the worker names the second's reset, the gateway's last refusal. A Task with no fallback is
// held until the first account is free, not until the reset the worker named.
func TestATaskWaitsForTheFirstAccountFreeNotTheLastRefusal(t *testing.T) {
	t.Parallel()

	w, both, _ := reviewerWorld(t)
	ctx := t.Context()

	w.loginAccount("acc_codex_2", "codex")
	_, _, err := w.s.ApplyProfile(ctx, "",
		"name: codex-alone\ndescription: d\nruntime: codex\nmodel: {provider: codex-subscription, name: gpt-6.1-sol}\n")
	require.NoError(t, err)

	task := w.tasks(NewTask{Ref: "alone", Profile: "codex-alone", Spec: "alone"})[0]
	created := w.schedule().Created
	require.Len(t, created, 1)

	first, last := t0.Add(time.Hour), t0.Add(2*time.Hour)
	require.NoError(t, w.s.CoolAccount(ctx, "codex", "acc_gateway_codex", first, "usage limit"))
	require.NoError(t, w.s.CoolAccount(ctx, "codex", "acc_codex_2", last, "usage limit"))

	named := t0.Add(24 * time.Hour)
	require.NoError(t, w.finish(w.begin(created[0]), Outcome{Class: FailureRateLimited, ResetsAt: &named}))

	require.NotNil(t, w.task(task.ID).NotBefore)
	assert.Equal(t, first, *w.task(task.ID).NotBefore)

	w.advance(61 * time.Minute)
	w.alive(both)
	assert.Len(t, w.schedule().Created, 1, "the first account is free again")
}

// codexAlias makes gpt-6.1-sol a model of the gateway's own table served from the Codex subscription, and offers it
// under the world's model gateway: what deploy/profiles/reviewer-codex.yaml asks the owner to name.
func (w *world) codexAlias() {
	w.t.Helper()

	_, err := w.s.ApplyProvider(w.t.Context(), "",
		"name: model-gateway\nkind: model_gateway\nmodels: [claude-opus-5-5, claude-sonnet-5-5, gpt-6.1-sol]\n")
	require.NoError(w.t, err)

	_, err = w.db.ExecContext(w.t.Context(), `INSERT INTO gateway_models (name, capability, source, created_at, updated_at)
		VALUES ('gpt-6.1-sol', 'chat', 'name: gpt-6.1-sol', 1, 1)`)
	require.NoError(w.t, err)

	_, err = w.db.ExecContext(w.t.Context(), `INSERT INTO gateway_model_targets (model, position, provider, upstream_model)
		VALUES ('gpt-6.1-sol', 0, 'codex-subscription', 'gpt-6.1-sol')`)
	require.NoError(w.t, err)
}

// Scenario (second review of PR #786, finding 1): the Codex reviewer runs on a model of the gateway's own table,
// served from the Codex subscription, as reviewer-codex.yaml asks. Its two Codex accounts cool for one and two
// hours and the worker names the last refusal's reset, a day away. A review not started starts under
// reviewer-claude; one under way is handed to reviewer-claude at once; a profile with no fallback waits for the
// first account, not the day.
func TestAReviewOnAGatewayModelSeesItsCodexAccountsCool(t *testing.T) {
	t.Parallel()

	w, both, backup := reviewerWorldOn(t, "model-gateway", (*world).codexAlias)
	ctx := t.Context()

	w.loginAccount("acc_codex_2", "codex")
	_, _, err := w.s.ApplyProfile(ctx, "",
		"name: codex-alone\ndescription: d\nruntime: codex\nmodel: {provider: model-gateway, name: gpt-6.1-sol}\n")
	require.NoError(t, err)

	tasks := w.tasks(
		NewTask{Ref: "r", Profile: "reviewer", Spec: "review PR #786"},
		NewTask{Ref: "alone", Profile: "codex-alone", Spec: "alone"},
	)

	created := w.schedule().Created
	require.Len(t, created, 2)

	byTask := map[string]Dispatch{}
	for _, id := range created {
		d := w.dispatchOf(id)
		byTask[d.TaskID] = d
		assert.Equal(t, "codex", w.conversation(d.ConversationID).Runtime)
	}

	first, last, named := t0.Add(time.Hour), t0.Add(2*time.Hour), t0.Add(24*time.Hour)
	require.NoError(t, w.s.CoolAccount(ctx, "codex", "acc_gateway_codex", first, "usage limit"))
	require.NoError(t, w.s.CoolAccount(ctx, "codex", "acc_codex_2", last, "usage limit"))

	for _, task := range tasks {
		require.NoError(t, w.finish(w.begin(byTask[task.ID].ID), Outcome{Class: FailureRateLimited, ResetsAt: &named}))
	}

	assert.Nil(t, w.task(tasks[0].ID).NotBefore, "a review a backup may take is not held")
	require.NotNil(t, w.task(tasks[1].ID).NotBefore)
	assert.Equal(t, first, *w.task(tasks[1].ID).NotBefore, "the first account, not the reset the worker named")

	w.tasks(NewTask{Ref: "new", Profile: "reviewer", Spec: "review PR #790"})

	out := w.schedule()
	require.Len(t, out.Created, 2, "the review under way and the new one, both on Claude")

	for _, id := range out.Created {
		d := w.dispatchOf(id)
		assert.Equal(t, backup.VersionID, d.ProfileVersionID)
		assert.Equal(t, "reviewer", d.FallbackFor)
		require.NotNil(t, d.FallbackUntil)
		assert.Equal(t, first, *d.FallbackUntil)
		assert.Equal(t, d.TaskID == tasks[0].ID, d.Handoff)
	}

	profiles, err := w.s.ListProfiles(ctx)
	require.NoError(t, err)

	i := slices.IndexFunc(profiles, func(p Profile) bool { return p.Name == "reviewer" })
	require.GreaterOrEqual(t, i, 0)
	assert.Equal(t, ReasonAccountsCooling, profiles[i].Availability.Reason)

	w.advance(61 * time.Minute)
	w.alive(both)

	after := w.schedule().Created
	require.Len(t, after, 1, "the first account is free again")
	assert.Equal(t, tasks[1].ID, w.dispatchOf(after[0]).TaskID)
}

// Scenario (second review of PR #786, finding 2): of the two Codex accounts, one cools for ten seconds and the
// other's login was refused for good. When a worker's limit settles, the one that cannot sign in does not hide
// that the usable one cools: a review is handed to reviewer-claude at once, and a profile with no fallback waits
// the ten seconds, not RateLimitCooldown.
func TestAnAccountThatCannotSignInDoesNotHideTheCoolingWhenALimitSettles(t *testing.T) {
	t.Parallel()

	w, both, backup := reviewerWorld(t)
	ctx := t.Context()

	w.loginAccount("acc_codex_refused", "codex")
	w.s.SetGatewayLogins(func(context.Context) (map[string]bool, error) {
		return map[string]bool{"acc_codex_refused": true}, nil
	})

	_, _, err := w.s.ApplyProfile(ctx, "",
		"name: codex-alone\ndescription: d\nruntime: codex\nmodel: {provider: codex-subscription, name: gpt-6.1-sol}\n")
	require.NoError(t, err)

	tasks := w.tasks(
		NewTask{Ref: "r", Profile: "reviewer", Spec: "review PR #786"},
		NewTask{Ref: "alone", Profile: "codex-alone", Spec: "alone"},
	)

	created := w.schedule().Created
	require.Len(t, created, 2)

	resets := t0.Add(10 * time.Second)
	require.NoError(t, w.s.CoolAccount(ctx, "codex", "acc_gateway_codex", resets, "usage limit"))

	for _, id := range created {
		require.NoError(t, w.finish(w.begin(id), Outcome{Class: FailureRateLimited}))
	}

	assert.Nil(t, w.task(tasks[0].ID).NotBefore)
	require.NotNil(t, w.task(tasks[1].ID).NotBefore)
	assert.Equal(t, resets, *w.task(tasks[1].ID).NotBefore)

	out := w.schedule().Created
	require.Len(t, out, 1)

	d := w.dispatchOf(out[0])
	assert.Equal(t, tasks[0].ID, d.TaskID)
	assert.Equal(t, backup.VersionID, d.ProfileVersionID)
	assert.True(t, d.Handoff)

	w.advance(11 * time.Second)
	w.alive(both)

	after := w.schedule().Created
	require.Len(t, after, 1, "the usable account is free again")
	assert.Equal(t, tasks[1].ID, w.dispatchOf(after[0]).TaskID)
}

// Scenario (second review of PR #786, finding 3): the Codex account's cooldown ended at second 2 and a worker's
// limit settles at second 3. An account is usable now, so the Task is not held: it runs again at once.
func TestATaskIsNotHeldPastAnAccountAlreadyFree(t *testing.T) {
	t.Parallel()

	w, both, _ := reviewerWorld(t)
	ctx := t.Context()

	_, _, err := w.s.ApplyProfile(ctx, "",
		"name: codex-alone\ndescription: d\nruntime: codex\nmodel: {provider: codex-subscription, name: gpt-6.1-sol}\n")
	require.NoError(t, err)

	task := w.tasks(NewTask{Ref: "alone", Profile: "codex-alone", Spec: "alone"})[0]
	created := w.schedule().Created
	require.Len(t, created, 1)

	require.NoError(t, w.s.CoolAccount(ctx, "codex", "acc_gateway_codex", t0.Add(2*time.Second), "usage limit"))
	w.advance(3 * time.Second)
	w.alive(both)

	require.NoError(t, w.finish(w.begin(created[0]), Outcome{Class: FailureRateLimited}))

	if nb := w.task(task.ID).NotBefore; nb != nil {
		assert.False(t, nb.After(t0.Add(3*time.Second)), "held to %s", nb)
	}

	assert.Len(t, w.schedule().Created, 1, "it runs again at once")
}
