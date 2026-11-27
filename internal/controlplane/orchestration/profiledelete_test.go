package orchestration

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/profilestore"
)

// scoutSource is a worker's profile the starting set does not have.
const scoutSource = "name: scout\ndescription: reads and reports\nruntime: claude\n" +
	"model: {provider: model-gateway, name: claude-sonnet-5-5}\n"

func (w *world) applyScout() profilestore.Profile {
	w.t.Helper()

	p, _, err := w.s.Profiles().ApplyProfile(w.t.Context(), "", scoutSource)
	require.NoError(w.t, err)

	return p
}

// Scenario: the owner deletes a profile no Task ever named. It goes with its
// versions, as Omnigent removes an agent no session uses: it reads as never
// there, and its name applied again starts at version 1.
func TestANeverUsedProfileIsDeletedWithItsVersions(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	w.applyScout()
	require.NoError(t, w.s.Profiles().DeleteProfile(ctx, "scout"))

	_, err := w.s.Profiles().GetProfile(ctx, "scout")
	require.ErrorIs(t, err, ErrNotFound)
	assert.NotContains(t, err.Error(), "deleted", "nothing of it is left to bring back")

	again := w.applyScout()
	assert.Equal(t, 1, again.Version, "its versions went with it")
}

// Scenario: the owner deletes a profile a Task ran under. It is marked
// deleted, as Coder marks a template: hidden from the list, read as deleted,
// refused for a new Task, as a backup and on a Bot's workers list, while the
// Dispatch that ran it still reads its version. Applying the same source
// brings it back as a new version, as Onyx's mark_persona_as_not_deleted.
func TestAUsedProfileIsHiddenAndComesBackWhenApplied(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	v1 := w.applyScout()
	task := w.tasks(NewTask{Ref: "a", Profile: "scout", Spec: "look around"})[0]
	ran := w.dispatchOf(w.schedule().Created[0])
	require.NoError(t, w.s.CancelTask(ctx, task.ID))
	require.NoError(t, w.s.Profiles().DeleteProfile(ctx, "scout"))

	profiles, err := w.s.Profiles().ListProfiles(ctx)
	require.NoError(t, err)

	for _, p := range profiles {
		assert.NotEqual(t, "scout", p.Name, "a deleted profile is not listed")
	}

	_, err = w.s.Profiles().GetProfile(ctx, "scout")
	require.ErrorIs(t, err, ErrNotFound)
	require.ErrorContains(t, err, "was deleted")

	_, err = w.s.CreateTasks(ctx, w.run.ID, []NewTask{{Ref: "b", Profile: "scout", Spec: "b"}})
	require.ErrorContains(t, err, "was deleted", "no new task names it")

	_, _, err = w.s.Profiles().ApplyProfile(ctx, "", "name: helper\ndescription: d\nruntime: claude\n"+
		"model: {provider: model-gateway}\nfallbacks: [scout]\n")
	require.ErrorIs(t, err, ErrNotFound, "nor a backup")

	_, err = w.s.PutBot(ctx, Bot{Name: "vmap", Workers: AllowList{Limited: true, Names: []string{"scout"}}})
	require.ErrorIs(t, err, ErrNotFound, "nor a bot's worker")

	assert.Equal(t, v1.VersionID, w.dispatchOf(ran.ID).ProfileVersionID, "what ran keeps its version")

	back, created, err := w.s.Profiles().ApplyProfile(ctx, "", scoutSource)
	require.NoError(t, err)
	assert.True(t, created, "the same source brings it back as a new version")
	assert.Equal(t, 2, back.Version)

	_, err = w.s.Profiles().GetProfile(ctx, "scout")
	require.NoError(t, err)
	w.tasks(NewTask{Ref: "c", Profile: "scout", Spec: "c"})
}

// Scenario: a deletion that would leave something naming the profile is
// refused, saying what to change first: the starting implementer, a Task
// not yet ended (a failed one of an open Run may be reopened), another
// profile's backup, a Bot's workers list.
func TestAProfileStillNamedIsNotDeleted(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	err := w.s.Profiles().DeleteProfile(ctx, profilestore.DefaultProfile)
	require.ErrorIs(t, err, ErrConflict)
	require.ErrorContains(t, err, "always there")

	require.ErrorIs(t, w.s.Profiles().DeleteProfile(ctx, "nobody"), ErrNotFound)

	w.applyScout()
	task := w.tasks(NewTask{Ref: "a", Profile: "scout", Spec: "a"})[0]

	err = w.s.Profiles().DeleteProfile(ctx, "scout")
	require.ErrorIs(t, err, ErrConflict)
	require.ErrorContains(t, err, "1 unfinished tasks")

	w.failForGood(task.ID)
	require.Equal(t, TaskFailed, w.task(task.ID).Status)
	require.ErrorContains(t, w.s.Profiles().DeleteProfile(ctx, "scout"), "unfinished", "the owner may reopen it")

	require.NoError(t, w.s.CancelRun(ctx, w.run.ID))

	_, _, err = w.s.Profiles().ApplyProfile(ctx, "", "name: helper\ndescription: d\nruntime: claude\n"+
		"model: {provider: model-gateway}\nfallbacks: [scout]\n")
	require.NoError(t, err)

	err = w.s.Profiles().DeleteProfile(ctx, "scout")
	require.ErrorIs(t, err, ErrConflict)
	require.ErrorContains(t, err, "as a backup")

	_, _, err = w.s.Profiles().ApplyProfile(ctx, "", "name: helper\ndescription: d\nruntime: claude\n"+
		"model: {provider: model-gateway}\n")
	require.NoError(t, err)

	vmap, err := w.s.PutBot(ctx, Bot{
		Name: "vmap", Workers: AllowList{Limited: true, Names: []string{"reviewer", "scout"}},
	})
	require.NoError(t, err)

	err = w.s.Profiles().DeleteProfile(ctx, "scout")
	require.ErrorIs(t, err, ErrConflict)
	require.ErrorContains(t, err, "bots vmap")

	_, err = w.s.PutBot(ctx, Bot{
		ID: vmap.ID, Name: "vmap", Workers: AllowList{Limited: true, Names: []string{"reviewer"}},
	})
	require.NoError(t, err)
	require.NoError(t, w.s.Profiles().DeleteProfile(ctx, "scout"))
}
