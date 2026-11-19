package orchestration

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
)

func roleWorld(t *testing.T) *world {
	t.Helper()
	w := newSeedWorld(t)
	ctx := t.Context()
	require.NoError(t, w.s.DrainNode(ctx, w.node.ID))
	w.node = w.addNode("node_roles", 12, "claude", "codex", "omp")
	w.gatewayAccount("claude")
	w.gatewayAccount("codex")
	w.gatewayAccount(OpenCodeGoRuntime)
	_, err := w.s.AddProviderModels(ctx, "codex-subscription", []string{"gpt-6.2-sol"})
	require.NoError(t, err)
	_, err = w.s.AddProviderModels(ctx, "opencode-go", []string{"deepseek-v4.1-flash", "glm-5.3-flash"})
	require.NoError(t, err)
	for _, role := range roles {
		_, _, err := w.s.ApplyProfile(ctx, role, "name: "+role+"\ndescription: role\n")
		require.NoError(t, err)
	}
	return w
}

func TestRoleMatrixRoutesKindsAndValidatesAtomically(t *testing.T) {
	t.Parallel()
	w := roleWorld(t)
	ctx := t.Context()
	source, err := w.s.RoleRouting(ctx)
	require.NoError(t, err)
	require.NoError(t, w.s.ApplyRoleRouting(ctx, source))
	require.ErrorIs(t, w.s.ApplyRoleRouting(ctx, strings.ReplaceAll(source, "gpt-6.2-sol", "unknown-model")), ErrInvalid)
	kept, err := w.s.RoleRouting(ctx)
	require.NoError(t, err)
	assert.Equal(t, source, kept)
	_, err = w.s.CreateTasks(ctx, w.run.ID, []NewTask{{Ref: "invalid", Kind: "large", Spec: "test"}})
	require.ErrorIs(t, err, ErrInvalid)
	_, err = w.s.CreateTasks(ctx, w.run.ID, []NewTask{{Ref: "review", Profile: "reviewer", Spec: "test"}})
	require.ErrorIs(t, err, ErrInvalid)

	tasks := w.tasks(
		NewTask{Ref: "broad", Spec: "broad"},
		NewTask{Ref: "narrow", Kind: "narrow", Spec: "narrow"},
		NewTask{Ref: "quick", Kind: "quick", Spec: "quick"},
		NewTask{Ref: "plan", Kind: "quick", Profile: "planner", Spec: "plan"},
		NewTask{Ref: "explore", Kind: "narrow", Profile: "explorer", Spec: "explore"},
	)
	want := [][2]string{
		{"claude-subscription/claude-opus-5-5", "high"},
		{"codex-subscription/gpt-6.2-sol", "medium"},
		{"claude-subscription/claude-sonnet-5-5", "low"},
		{"claude-subscription/claude-opus-5-5", "high"},
		{"claude-subscription/claude-sonnet-5-5", "medium"},
	}
	assert.Equal(t, "broad", tasks[0].Kind)
	require.Len(t, w.schedule().Created, len(tasks))
	for index, task := range tasks {
		dispatch, err := w.s.LatestDispatch(ctx, task.ID)
		require.NoError(t, err)
		assert.Equal(t, want[index][0], dispatch.Model)
		assert.Equal(t, want[index][1], dispatch.Effort)
		require.NotNil(t, dispatch.ChainEntry)
		assert.Equal(t, 1, *dispatch.ChainEntry)
		require.NoError(t, w.finish(w.begin(dispatch.ID), success(task.Spec)))
	}

	reviews := w.tasks(
		NewTask{Ref: "review-plan", Profile: "reviewer", ReviewOf: tasks[3].ID, Spec: "plan review"},
		NewTask{Ref: "review-code", Profile: "reviewer", Kind: "narrow", ReviewOf: tasks[1].ID, Spec: "review"},
	)
	require.Len(t, w.schedule().Created, len(reviews))
	for index, task := range reviews {
		dispatch, err := w.s.LatestDispatch(ctx, task.ID)
		require.NoError(t, err)
		assert.Equal(t, want[1-index][0], dispatch.Model)
		assert.Equal(t, "high", dispatch.Effort)
	}
}

func TestRoleHandoffPinsEntryAndSharesMemory(t *testing.T) {
	t.Parallel()
	w := roleWorld(t)
	ctx := t.Context()
	_, err := w.s.Memories().SetAgentMemory(ctx, "implementer", "Shared role fact.")
	require.NoError(t, err)
	task := w.tasks(NewTask{Ref: "role", Spec: "make the change"})[0]
	created := w.schedule().Created
	require.Len(t, created, 1)
	first := w.dispatchOf(created[0])
	firstCall := w.begin(first.ID)
	firstConv := w.conversation(first.ConversationID)
	key := "gwk_role_first"
	w.gatewayKey(key, first.ID)
	w.gatewayRequest("usage_role_first", key, true)
	end := t0.Add(time.Hour)
	w.gatewayCooling("/v1/messages", first.Model, end)
	require.NoError(t, w.finish(firstCall, Outcome{Class: FailureRateLimited, ResetsAt: &end}))
	created = w.schedule().Created
	require.Len(t, created, 1, "wait: %s", w.task(task.ID).WaitingReason)
	second := w.dispatchOf(created[0])
	require.NotNil(t, second.ChainEntry)
	assert.Equal(t, 2, *second.ChainEntry)
	assert.Equal(t, "codex-subscription/gpt-6.2-sol", second.Model)
	assert.Equal(t, first.ProfileVersionID, second.ProfileVersionID)
	assert.True(t, second.Handoff)
	assert.Equal(t, firstConv.Worktree, w.conversation(second.ConversationID).Worktree)
	row, err := w.s.read().GetDispatch(ctx, second.ID)
	require.NoError(t, err)
	offer, err := offerOf(ctx, w.s.read(), row)
	require.NoError(t, err)
	assert.Equal(t, "Shared role fact.", offer.Memory)
	require.NoError(t, w.finish(w.begin(second.ID), success("done")))
	metrics, err := w.s.RoleMetrics(ctx, time.Time{})
	require.NoError(t, err)
	require.Len(t, metrics, 2)
	assert.Equal(t, int64(1), metrics[0].Failed)
	assert.Equal(t, int64(2000), metrics[0].CostMicros)
	assert.Equal(t, int64(1), metrics[1].Succeeded)
	assert.Equal(t, "implementer", metrics[1].Role)
	assert.Equal(t, "broad", metrics[1].Kind)

	w.advance(time.Hour)
	w.alive(w.node)
	_, err = w.s.FollowUpTask(ctx, task.ID, AddressOwner, "verify again")
	require.NoError(t, err)
	created = w.schedule().Created
	require.Len(t, created, 1)
	continued := w.dispatchOf(created[0])
	assert.Equal(t, second.ChainEntry, continued.ChainEntry)
	assert.Equal(t, second.ConversationID, continued.ConversationID)
	assert.Equal(t, second.Model, continued.Model)
	assert.False(t, continued.Handoff)
}

func TestRoleRoutingWaitsForPinsAndReportsMissingModels(t *testing.T) {
	t.Parallel()
	w := roleWorld(t)
	ctx := t.Context()
	w.gatewayCooling("/v1/messages", "claude-subscription/claude-opus-5-5", t0.Add(time.Hour))
	tasks := w.tasks(
		NewTask{Ref: "strict", Runtime: "claude", Spec: "wait"},
		NewTask{Ref: "default", Spec: "fall back"},
	)
	created := w.schedule().Created
	require.Len(t, created, 1)
	dispatch := w.dispatchOf(created[0])
	assert.Equal(t, tasks[1].ID, dispatch.TaskID)
	assert.Equal(t, "accounts_cooling", w.task(tasks[0].ID).WaitingReason)
	assert.Equal(t, "codex", dispatch.Runtime)
	assert.False(t, dispatch.Handoff)

	require.NoError(t, w.s.write(ctx, func(q *store.Queries, _ int64) error {
		return q.SetRoleRouting(ctx, strings.ReplaceAll(defaultRoleRouting, "gpt-6.2-sol", "missing-model"))
	}))
	missing := w.tasks(NewTask{Ref: "missing", Kind: "narrow", Spec: "do not substitute"})[0]
	assert.Empty(t, w.schedule().Created)
	assert.Contains(t, w.task(missing.ID).WaitingReason, "missing-model")
}
