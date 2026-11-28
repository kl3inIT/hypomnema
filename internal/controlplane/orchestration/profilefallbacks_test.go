package orchestration

import (
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
)

// writerSource is a profile with fallbacks (issue #1336): Claude Code under the subscription's Opus, then its Sonnet.
const writerSource = "name: writer\ndescription: d\nruntime: claude\n" +
	"model: {provider: claude-subscription, name: claude-opus-5-5, effort: high}\n" +
	"fallbacks: [claude-subscription/claude-sonnet-5-5]\n"

// fallbackWorld is a world whose node runs Claude Code through the gateway, with the writer profile applied.
func fallbackWorld(t *testing.T) *world {
	t.Helper()

	w := newSeedWorld(t)
	require.NoError(t, w.s.DrainNode(t.Context(), w.node.ID))
	w.node = w.addNode("node_writer", 8, "claude")
	w.gatewayAccount("claude")

	_, _, err := w.s.Profiles().ApplyProfile(t.Context(), "", writerSource)
	require.NoError(t, err)

	return w
}

// Scenario (issue #1336): a profile's fallbacks are models, as a Bot's. While the gateway cools the profile's own
// model, a Task that has not started runs its first fallback that is served, on the profile's runtime, with the
// profile's effort; the Run is told once, and a Task that names its own model waits. Once the model is free again a
// new Task runs it.
func TestAProfilesTaskRunsItsFallbackModelWhileItsOwnCools(t *testing.T) {
	t.Parallel()

	w := fallbackWorld(t)

	first := w.tasks(NewTask{Ref: "a", Profile: "writer", Spec: "a"})[0]
	created := w.schedule().Created
	require.Len(t, created, 1)

	own := w.dispatchOf(created[0])
	assert.Equal(t, "claude-subscription/claude-opus-5-5", own.Model)
	assert.Equal(t, "high", own.Effort)
	require.NotNil(t, own.ChainEntry)
	assert.Equal(t, 1, *own.ChainEntry)
	assert.Empty(t, own.FallbackFor)
	require.NoError(t, w.finish(w.begin(own.ID), success("a")))
	assert.Equal(t, TaskSucceeded, w.task(first.ID).Status)

	resets := w.clock.Now().Add(time.Hour)
	w.gatewayCooling("/v1/messages", "claude-subscription/claude-opus-5-5", resets)

	w.tasks(
		NewTask{Ref: "b", Profile: "writer", Spec: "b"},
		NewTask{Ref: "strict", Profile: "writer", Spec: "strict", Model: "claude-subscription/claude-opus-5-5"},
	)

	out := w.schedule()
	require.Len(t, out.Created, 1, "the Task that names its own model waits")

	fallback := w.dispatchOf(out.Created[0])
	assert.Equal(t, "b", w.task(fallback.TaskID).Spec)
	assert.Equal(t, "claude-subscription/claude-sonnet-5-5", fallback.Model)
	assert.Equal(t, "high", fallback.Effort, "the profile's effort, not a role's")
	assert.Equal(t, own.ProfileVersionID, fallback.ProfileVersionID, "the same profile version")
	require.NotNil(t, fallback.ChainEntry)
	assert.Equal(t, 2, *fallback.ChainEntry)
	assert.Equal(t, "writer", fallback.FallbackFor)
	require.Len(t, out.Escalations, 1)
	assert.Equal(t, EscalationFallback, out.Escalations[0].Kind)
	assert.Equal(t, "claude-subscription/claude-sonnet-5-5", out.Escalations[0].Fallback)

	w.advance(time.Hour + time.Minute)
	w.alive(w.node)
	w.tasks(NewTask{Ref: "later", Profile: "writer", Spec: "later"})

	for _, id := range w.schedule().Created {
		d := w.dispatchOf(id)
		if w.task(d.TaskID).Spec == "later" {
			assert.Equal(t, "claude-subscription/claude-opus-5-5", d.Model, "its own model again")
		}
	}
}

// Scenario (issues #187, #1336): the limit comes under way. The Task's worker had begun; its profile's fallback
// takes it over in the same worktree with a session of its own, once, and a later Dispatch keeps that model.
func TestAProfilesFallbackTakesOverATaskUnderWay(t *testing.T) {
	t.Parallel()

	w := fallbackWorld(t)

	task := w.tasks(NewTask{Ref: "a", Profile: "writer", Spec: "do a"})[0]
	created := w.schedule().Created
	require.Len(t, created, 1)

	first := w.dispatchOf(created[0])
	call := w.begin(first.ID)
	old := w.conversation(first.ConversationID)
	resets := w.clock.Now().Add(time.Hour)
	w.gatewayCooling("/v1/messages", first.Model, resets)
	require.NoError(t, w.finish(call, Outcome{Class: FailureRateLimited, ResetsAt: &resets}))

	created = w.schedule().Created
	require.Len(t, created, 1, "wait: %s", w.task(task.ID).WaitingReason)

	second := w.dispatchOf(created[0])
	assert.True(t, second.Handoff)
	assert.Equal(t, "writer", second.FallbackFor)
	assert.Equal(t, "claude-subscription/claude-sonnet-5-5", second.Model)
	assert.Equal(t, old.Worktree, w.conversation(second.ConversationID).Worktree, "the same worktree")
	assert.NotEqual(t, old.ID, second.ConversationID, "a session of its own")

	require.NoError(t, w.finish(w.begin(second.ID), Outcome{Class: FailureWorkerError}))
	w.advance(time.Minute)
	w.alive(w.node)

	next := w.schedule().Created
	require.Len(t, next, 1)

	again := w.dispatchOf(next[0])
	assert.Equal(t, second.ConversationID, again.ConversationID)
	assert.Equal(t, second.Model, again.Model)
	assert.False(t, again.Handoff)
}

// A Task for which a request to a service was approved is not handed to a fallback: its worker acted outside the
// worktree (issue #187).
func TestATaskThatWroteThroughAnAccountIsNotHandedToAFallback(t *testing.T) {
	t.Parallel()

	w := fallbackWorld(t)
	ctx := t.Context()

	task := w.tasks(NewTask{Ref: "a", Profile: "writer", Spec: "a"})[0]
	created := w.schedule().Created
	require.Len(t, created, 1)

	first := w.dispatchOf(created[0])
	call := w.begin(first.ID)
	require.NoError(t, w.s.write(ctx, func(q *store.Queries, now int64) error {
		if err := q.InsertApproval(ctx, store.InsertApprovalParams{
			ID: "apr_1", Integration: "google", Label: "work", Action: "gmail.send", ActionName: "Send a mail",
			DispatchID: nullString(first.ID), TaskID: nullString(task.ID), RunID: nullString(task.RunID),
			AskedBy: "worker", Title: "Send a mail", Payload: "{}", MatchHash: "h", Now: now,
		}); err != nil {
			return err
		}

		_, err := q.DecideApproval(ctx, store.DecideApprovalParams{
			ID: "apr_1", Status: "approved", Scope: nullString("once"), Now: sql.NullInt64{Int64: now, Valid: true},
		})

		return err
	}))

	resets := w.clock.Now().Add(time.Hour)
	w.gatewayCooling("/v1/messages", first.Model, resets)
	require.NoError(t, w.finish(call, Outcome{Class: FailureRateLimited, ResetsAt: &resets}))
	assert.Empty(t, w.schedule().Created)
}

// A profile's fallbacks are checked when it is applied, as a Bot's are (issue #1336): each provider/model, offered
// to the profile's runtime, named once, not its own model, at most five, and only after a model of its own. A role
// takes its models from the role routing.
func TestAProfilesFallbacksAreCheckedWhenApplied(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	apply := func(source string) error {
		_, _, err := w.s.Profiles().ApplyProfile(t.Context(), "", source)

		return err
	}

	const head = "name: a\ndescription: d\nruntime: claude\n"

	const own = "model: {provider: claude-subscription, name: claude-opus-5-5}\n"

	for name, source := range map[string]string{
		"a profile's name": head + own + "fallbacks: [reviewer]\n",
		"an empty part":    head + own + "fallbacks: [claude-subscription/]\n",
		"its own model":    head + own + "fallbacks: [claude-subscription/claude-opus-5-5]\n",
		"a model named twice": head + own +
			"fallbacks: [claude-subscription/claude-sonnet-5-5, claude-subscription/claude-sonnet-5-5]\n",
		"no model of its own": head + "fallbacks: [claude-subscription/claude-sonnet-5-5]\n",
		"more than five": head + own + "fallbacks: [claude-subscription/a, claude-subscription/b, " +
			"claude-subscription/c, claude-subscription/d, claude-subscription/e, claude-subscription/f]\n",
		"a role":           "name: reviewer\ndescription: d\nfallbacks: [claude-subscription/claude-sonnet-5-5]\n",
		"an unknown model": head + own + "fallbacks: [claude-subscription/no-such-model]\n",
	} {
		require.ErrorIs(t, apply(source), ErrInvalid, name)
	}

	require.ErrorIs(t, apply(head+own+"fallbacks: [nobody/claude-sonnet-5-5]\n"), ErrNotFound)

	p, _, err := w.s.Profiles().ApplyProfile(t.Context(), "", head+own+
		"fallbacks: [claude-subscription/claude-sonnet-5-5, claude-subscription/claude-haiku-5-5]\n")
	require.NoError(t, err)
	assert.Equal(t, []string{"claude-subscription/claude-sonnet-5-5", "claude-subscription/claude-haiku-5-5"},
		p.Fallbacks)
}
