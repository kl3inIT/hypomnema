package orchestration

import (
	"testing"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const checkerSource = "name: checker\ndescription: Reads a pull request and reports.\nruntime: claude\n"

// Scenario: the Bot coordinator makes a Bot from its chat (issue #169). The
// change is listed in that chat and can be undone; undone, the Bot is gone
// and the change says so, and a second undo is refused.
func TestTheCoordinatorsNewBotCanBeUndone(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	chat, err := w.s.CreateChat(ctx, "", "")
	require.NoError(t, err)

	_, err = w.s.PutBotFromChat(ctx, chat.ID, Bot{Name: "researcher", Persona: "You find sources."})
	require.NoError(t, err)

	changes, err := w.s.ListSetupChanges(ctx, chat.ID)
	require.NoError(t, err)
	require.Len(t, changes, 1)
	assert.Equal(t, []string{"bot", "researcher", "made"}, []string{changes[0].Kind, changes[0].Name, changes[0].Action})
	assert.True(t, changes[0].CanUndo, changes[0].Problem)

	undone, err := w.s.UndoSetupChange(ctx, changes[0].ID)
	require.NoError(t, err)
	assert.NotNil(t, undone.UndoneAt)
	assert.False(t, undone.CanUndo)

	_, err = w.s.GetBot(ctx, "researcher")
	require.ErrorIs(t, err, ErrNotFound, "a Bot it made is removed")

	_, err = w.s.UndoSetupChange(ctx, changes[0].ID)
	require.ErrorIs(t, err, ErrConflict)
}

// Scenario: the Bot coordinator changes a Bot and the change is undone: the
// Bot is as it was, its memory untouched. A change the owner made since, or
// the Bot's answer in a chat since, makes the undo stale; the coordinator's
// own reply in the chat of the change does not.
func TestAnUndoOfAChangedBotIsRefusedOnceChangedOrUsed(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	_, err := w.s.PutBot(ctx, Bot{Name: "vmap", Persona: "You answer about VMAP."})
	require.NoError(t, err)

	_, err = w.s.ChangeBotMemory(ctx, "vmap", MemoryOp{Kind: MemoryAdd, Text: "Linh leads the maps team."})
	require.NoError(t, err)

	chat, err := w.s.CreateChat(ctx, "", "")
	require.NoError(t, err)

	vmapChat, err := w.s.CreateChat(ctx, "vmap", "")
	require.NoError(t, err)

	change := func(persona string) SetupChange {
		w.clock.now = w.clock.now.Add(time.Minute)

		_, err := w.s.PutBotFromChat(ctx, chat.ID, Bot{
			Name: "vmap", Persona: persona, Workers: AllowList{Limited: true, Names: []string{"reviewer"}},
		})
		require.NoError(t, err)

		changes, err := w.s.ListSetupChanges(ctx, chat.ID)
		require.NoError(t, err)

		return changes[len(changes)-1]
	}

	first := change("You answer about VMAP and Tasco.")
	assert.Equal(t, "changed", first.Action)

	_, err = w.s.AddChatMessage(ctx, chat.ID, ChatCoordinator, "I changed vmap.")
	require.NoError(t, err)

	undone, err := w.s.UndoSetupChange(ctx, first.ID)
	require.NoError(t, err, "its own reply in the chat of the change is not a use")
	assert.NotNil(t, undone.UndoneAt)

	vmap, err := w.s.GetBot(ctx, "vmap")
	require.NoError(t, err)
	assert.Equal(t, "You answer about VMAP.", vmap.Persona)
	assert.False(t, vmap.Workers.Limited, "the list it had: none")
	assert.Equal(t, "Linh leads the maps team.", vmap.Memory)

	used := change("You answer about maps.")

	_, err = w.s.AddChatMessage(ctx, vmapChat.ID, ChatCoordinator, "Tiles are up.")
	require.NoError(t, err)

	_, err = w.s.UndoSetupChange(ctx, used.ID)
	require.ErrorIs(t, err, ErrConflict, "used since")

	changed := change("You answer about roads.")

	_, err = w.s.PutBot(ctx, Bot{Name: "vmap", Persona: "The owner's words."})
	require.NoError(t, err)

	listed, err := w.s.GetSetupChange(ctx, changed.ID)
	require.NoError(t, err)
	assert.False(t, listed.CanUndo)
	assert.Contains(t, listed.Problem, "changed or used since")

	_, err = w.s.UndoSetupChange(ctx, changed.ID)
	require.ErrorIs(t, err, ErrConflict, "changed since")
}

// Scenario: the Bot coordinator removes a Bot and the undo puts it back. It
// never removes itself, and a chat of another Bot makes no change at all.
func TestARemovedBotComesBackAndTheCoordinatorStays(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	_, err := w.s.PutBot(ctx, Bot{Name: "vmap", Persona: "You answer about VMAP.", Skills: nil})
	require.NoError(t, err)

	chat, err := w.s.CreateChat(ctx, "", "")
	require.NoError(t, err)

	require.ErrorIs(t, w.s.DeleteBotFromChat(ctx, chat.ID, DefaultBot), ErrConflict)

	_, err = w.s.PutBot(ctx, Bot{Name: "maps"})
	require.NoError(t, err)

	vmapChat, err := w.s.CreateChat(ctx, "vmap", "")
	require.NoError(t, err)

	require.ErrorIs(t, w.s.DeleteBotFromChat(ctx, vmapChat.ID, "maps"), ErrForbidden,
		"only the Bot coordinator's chat makes changes")

	require.NoError(t, w.s.DeleteBotFromChat(ctx, chat.ID, "maps"))

	changes, err := w.s.ListSetupChanges(ctx, chat.ID)
	require.NoError(t, err)
	require.Len(t, changes, 1)
	assert.Equal(t, "removed", changes[0].Action)

	_, err = w.s.UndoSetupChange(ctx, changes[0].ID)
	require.NoError(t, err)

	_, err = w.s.GetBot(ctx, "maps")
	require.NoError(t, err, "back")
}

// Scenario: the Bot coordinator makes a worker profile and changes another.
// Undone, the new profile is gone and the changed one is as it was, as a new
// version; a profile a Task was given since is not undone.
func TestTheCoordinatorsProfilesCanBeUndoneUntilUsed(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	chat, err := w.s.CreateChat(ctx, "", "")
	require.NoError(t, err)

	_, created, err := w.s.ApplyProfileFromChat(ctx, chat.ID, "", checkerSource)
	require.NoError(t, err)
	require.True(t, created)

	_, created, err = w.s.ApplyProfileFromChat(ctx, chat.ID, "", checkerSource)
	require.NoError(t, err)
	require.False(t, created)

	before, err := w.s.GetProfile(ctx, "implementer")
	require.NoError(t, err)

	_, _, err = w.s.ApplyProfileFromChat(ctx, chat.ID, "",
		"name: implementer\ndescription: Writes code.\nruntime: codex\n")
	require.NoError(t, err)

	changes, err := w.s.ListSetupChanges(ctx, chat.ID)
	require.NoError(t, err)
	require.Len(t, changes, 2, "the same source again is no change")
	made, changed := changes[0], changes[1]
	assert.Equal(t, []string{"made", "changed"}, []string{made.Action, changed.Action})

	_, err = w.s.UndoSetupChange(ctx, changed.ID)
	require.NoError(t, err)

	restored, err := w.s.GetProfile(ctx, "implementer")
	require.NoError(t, err)
	assert.Equal(t, before.Source, restored.Source)
	assert.Equal(t, before.Version+2, restored.Version, "what it was comes back as a new version")

	w.clock.now = w.clock.now.Add(time.Minute)

	_, err = w.s.CreateTasks(ctx, w.run.ID, []NewTask{{Ref: "check", Spec: "review it", Profile: "checker"}})
	require.NoError(t, err)

	_, err = w.s.UndoSetupChange(ctx, made.ID)
	require.ErrorIs(t, err, ErrConflict, "a Task was given to it")

	_, err = w.s.GetProfile(ctx, "checker")
	require.NoError(t, err)
}

// Scenario: a Bot with lists (issue #169). Without a list it may use every
// Connection switched on for chats and give work to every profile; with one,
// only those named, and a Connection off for chats stays off. A Run it
// creates is given only what it may use. A list names what exists.
func TestABotsListsNarrowWhatItMayUse(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	for _, label := range []string{"work", "home", "old"} {
		_, err := w.s.SaveServiceConnection(ctx, Connection{Integration: "google", Label: label, Secret: "s-" + label})
		require.NoError(t, err)
	}

	for _, label := range []string{"work", "home"} {
		_, err := w.s.SetConnectionChats(ctx, "google", label, true)
		require.NoError(t, err)
	}

	_, err := w.s.PutBot(ctx, Bot{Name: "vmap", Connections: AllowList{Limited: true, Names: []string{"google:nope"}}})
	require.ErrorIs(t, err, ErrNotFound)

	_, err = w.s.PutBot(ctx, Bot{Name: "vmap", Workers: AllowList{Limited: true, Names: []string{"ghost"}}})
	require.ErrorIs(t, err, ErrNotFound)

	open, err := w.s.PutBot(ctx, Bot{Name: "open"})
	require.NoError(t, err)

	vmap, err := w.s.PutBot(ctx, Bot{
		Name:        "vmap",
		Connections: AllowList{Limited: true, Names: []string{"google:work", "google:old"}},
		Workers:     AllowList{Limited: true, Names: []string{"reviewer"}},
	})
	require.NoError(t, err)

	conns, err := w.s.ConnectionsOf(ctx, "google")
	require.NoError(t, err)

	may := func(b Bot) []string {
		var out []string

		for _, c := range conns {
			if b.MayUse(c) {
				out = append(out, c.Label)
			}
		}

		return out
	}

	assert.ElementsMatch(t, []string{"work", "home"}, may(open))
	assert.Equal(t, []string{"work"}, may(vmap), "old is on its list but off for chats")

	require.NoError(t, open.MayGive("implementer"))
	require.NoError(t, vmap.MayGive("reviewer"))
	require.ErrorIs(t, vmap.MayGive(""), ErrForbidden, "a Task that names none is the implementer's")
	require.ErrorContains(t, vmap.MayGive("implementer"), "(allowed: reviewer)")

	none := Bot{Name: "none", Workers: AllowList{Limited: true}}
	require.ErrorContains(t, none.MayGive("reviewer"), "(allowed: none)")

	tok, _, err := w.s.AddToken(ctx, "pi-coordinator", bearertoken.Coordinator)
	require.NoError(t, err)

	_, err = w.s.CreateRun(ctx, NewRun{
		Objective: "x", CreatedBy: "pi", CoordinatorTokenID: tok.ID, Bot: "vmap", Uses: []string{"google:home"},
	})
	require.ErrorIs(t, err, ErrForbidden)

	run, err := w.s.CreateRun(ctx, NewRun{
		Objective: "x", CreatedBy: "pi", CoordinatorTokenID: tok.ID, Bot: "vmap", Uses: []string{"google:work"},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"google:work"}, run.Uses)
}

// Scenario: a profile's undo is refused, and its card says so, when another
// profile names it as a backup, when a worker ran the version the change made
// for a Task given before it, and when what it was names a backup gone since.
func TestAProfilesUndoIsRefusedOnceItIsReliedOn(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	chat, err := w.s.CreateChat(ctx, "", "")
	require.NoError(t, err)

	last := func() SetupChange {
		changes, err := w.s.ListSetupChanges(ctx, chat.ID)
		require.NoError(t, err)

		return changes[len(changes)-1]
	}

	// Made, then named as a backup by the owner: not undone, and the card says why.
	_, _, err = w.s.ApplyProfileFromChat(ctx, chat.ID, "", checkerSource)
	require.NoError(t, err)
	madeChecker := last()

	_, _, err = w.s.ApplyProfile(ctx, "", "name: writer\ndescription: d\nruntime: claude\nfallbacks: [checker]\n")
	require.NoError(t, err)

	shown, err := w.s.GetSetupChange(ctx, madeChecker.ID)
	require.NoError(t, err)
	assert.False(t, shown.CanUndo)
	assert.Contains(t, shown.Problem, "as a backup")

	// The coordinator takes the backup away; the checker can then be undone, and the writer's change cannot,
	// since what it was names the checker, gone.
	_, _, err = w.s.ApplyProfileFromChat(ctx, chat.ID, "", "name: writer\ndescription: d\nruntime: claude\n")
	require.NoError(t, err)
	changedWriter := last()

	_, err = w.s.UndoSetupChange(ctx, madeChecker.ID)
	require.NoError(t, err)

	_, err = w.s.UndoSetupChange(ctx, changedWriter.ID)
	require.ErrorIs(t, err, ErrConflict)
	require.ErrorContains(t, err, "which is gone")

	// A Task given before a change runs under the version it made: the change was used.
	w.tasks(NewTask{Ref: "a", Profile: "implementer", Spec: "a"})
	w.clock.now = w.clock.now.Add(time.Minute)

	_, _, err = w.s.ApplyProfileFromChat(ctx, chat.ID, "",
		"name: implementer\ndescription: Writes code.\nruntime: claude\ninstruction: Run the tests.\n")
	require.NoError(t, err)
	changedImplementer := last()

	shown, err = w.s.GetSetupChange(ctx, changedImplementer.ID)
	require.NoError(t, err)
	require.True(t, shown.CanUndo, "the Task was given before the change: not yet a use")

	w.alive(w.node)
	require.Len(t, w.schedule().Created, 1)

	_, err = w.s.UndoSetupChange(ctx, changedImplementer.ID)
	require.ErrorIs(t, err, ErrConflict, "a worker ran it")
}

// Scenario: the Bot coordinator has every right on the Bots (owner,
// 2026-11-12, confirmed 2026-11-13): it widens a Bot's lists, makes a Bot
// with more than it has itself, and changes its own lists.
func TestTheCoordinatorChangesWhatAnyBotMayUse(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	chat, err := w.s.CreateChat(ctx, "", "")
	require.NoError(t, err)

	only := func(names ...string) AllowList { return AllowList{Limited: true, Names: names} }

	_, err = w.s.PutBot(ctx, Bot{Name: "vmap", Workers: only("reviewer")})
	require.NoError(t, err)

	widened, err := w.s.PutBotFromChat(ctx, chat.ID, Bot{Name: "vmap", Workers: only("reviewer", "implementer")})
	require.NoError(t, err, "it widens a Bot's list")
	assert.Equal(t, only("reviewer", "implementer"), widened.Workers)

	_, err = w.s.PutBotFromChat(ctx, chat.ID, Bot{Name: DefaultBot, Workers: only("reviewer")})
	require.NoError(t, err, "it changes its own lists")

	made, err := w.s.PutBotFromChat(ctx, chat.ID, Bot{Name: "researcher"})
	require.NoError(t, err, "a Bot it makes may have more than it has")
	assert.False(t, made.Workers.Limited)
}
