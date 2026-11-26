package inboxstore_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/inboxstore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/storeerr"
)

// eventFields is an event's before and after, read.
func eventFields(t *testing.T, e inboxstore.Event) (before, after map[string]any) {
	t.Helper()
	require.NoError(t, json.Unmarshal(e.Before, &before))
	require.NoError(t, json.Unmarshal(e.After, &after))

	return before, after
}

// project is a department the owner made in the sidebar, with the Bots named in it: an item's project (issue #1188).
func (w *world) project(t *testing.T, name string, bots ...string) orchestration.Department {
	t.Helper()

	d, err := w.svc.CreateDepartment(t.Context(), name, bots)
	require.NoError(t, err)

	return d
}

func intp(n int) *int       { return &n }
func strp(s string) *string { return &s }

// Scenario (issue #1157): the main Bot adds an item from its chat with a priority, the reason and a project; the item
// and its creation say them. The owner then sets another priority with one click, without a reason: the reason the Bot
// gave does not stay beside a priority it was not given for, and the change is an event of his, which the Bot reads
// back as his correction.
func TestABotSetsThePriorityWithItsReasonAndTheOwnerCorrectsIt(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()
	vmap := w.project(t, "VMAP")

	td, err := w.inbox.Create(ctx, bot(w.kai), inboxstore.NewTodo{
		Title: "Trả lời cô Laura", State: inboxstore.Open, Assignee: me,
		Source:   inboxstore.Source{Kind: inboxstore.FromChat, ID: w.chat},
		Priority: inboxstore.Urgent, PriorityReason: "Her deadline is today", Project: vmap.ID,
	})
	require.NoError(t, err)
	assert.Equal(t, inboxstore.Urgent, td.Priority)
	assert.Equal(t, "Her deadline is today", td.PriorityReason)
	assert.Equal(t, vmap.ID, td.Project)

	_, created := eventFields(t, td.Events[0])
	assert.EqualValues(t, inboxstore.Urgent, created["priority"])
	assert.Equal(t, vmap.ID, created["project_id"])

	td, err = w.inbox.Edit(ctx, by(owner, td), inboxstore.Edit{Priority: intp(inboxstore.Low)})
	require.NoError(t, err)
	assert.Equal(t, inboxstore.Low, td.Priority)
	assert.Empty(t, td.PriorityReason, "the Bot's reason was for urgent")

	last := td.Events[len(td.Events)-1]
	assert.Equal(t, inboxstore.EventEdited, last.Kind)
	assert.Equal(t, owner, last.Actor)
	before, after := eventFields(t, last)
	assert.Equal(t, map[string]any{"priority": float64(inboxstore.Urgent), "priority_reason": "Her deadline is today"},
		before)
	assert.Equal(t, map[string]any{"priority": float64(inboxstore.Low), "priority_reason": ""}, after)

	changed, err := w.inbox.List(ctx, inboxstore.Filter{OwnerChangedSince: t0}, "", 0)
	require.NoError(t, err)
	require.Len(t, changed.Items, 1, "the triage reads the owner's correction")
	assert.Equal(t, td.ID, changed.Items[0].ID)
}

// Scenario: a priority, its reason and a project are checked as every field is: the scale is 0 to 4, a reason comes
// with its priority and is one line, a project is a department that is there. A refused change writes nothing.
func TestAPriorityOrAProjectThatIsNotOneIsRefused(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()
	td := w.fromChat(t)

	for name, e := range map[string]inboxstore.Edit{
		"past low":             {Priority: intp(5)},
		"below none":           {Priority: intp(-1)},
		"a reason alone":       {PriorityReason: strp("why")},
		"a reason on two rows": {Priority: intp(inboxstore.High), PriorityReason: strp("one\ntwo")},
	} {
		_, err := w.inbox.Edit(ctx, by(owner, td), e)
		require.ErrorIs(t, err, storeerr.ErrInvalid, name)
	}

	_, err := w.inbox.Edit(ctx, by(owner, td), inboxstore.Edit{Project: strp("dep_gone")})
	require.ErrorIs(t, err, storeerr.ErrNotFound)

	_, err = w.inbox.Create(ctx, owner, inboxstore.NewTodo{
		Title: "x", State: inboxstore.Open, Source: inboxstore.Source{Kind: inboxstore.ByHand}, Assignee: me, Priority: 9,
	})
	require.ErrorIs(t, err, storeerr.ErrInvalid)

	_, err = w.inbox.Create(ctx, owner, inboxstore.NewTodo{
		Title: "x", State: inboxstore.Open, Source: inboxstore.Source{Kind: inboxstore.ByHand}, Assignee: me,
		Project: "dep_gone",
	})
	require.ErrorIs(t, err, storeerr.ErrNotFound)

	w.unchanged(t, td)
}

// Scenario: a Bot sets the priority and the project of the items it added or was given, as it edits their other
// fields, and not of another Bot's.
func TestABotSetsThePriorityOfItsOwnItemsOnly(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()
	vmap := w.project(t, "VMAP")
	mine := w.fromChat(t)

	got, err := w.inbox.Edit(ctx, by(bot(w.kai), mine), inboxstore.Edit{
		Priority: intp(inboxstore.High), PriorityReason: strp("A client waits"), Project: &vmap.ID,
	})
	require.NoError(t, err)
	assert.Equal(t, inboxstore.High, got.Priority)
	assert.Equal(t, vmap.ID, got.Project)

	got, err = w.inbox.Edit(ctx, by(bot(w.kai), got), inboxstore.Edit{Project: strp("")})
	require.NoError(t, err)
	assert.Empty(t, got.Project, "taken out of its project")

	_, err = w.inbox.Edit(ctx, by(bot(w.lan), got), inboxstore.Edit{Priority: intp(inboxstore.Low)})
	require.ErrorIs(t, err, storeerr.ErrForbidden)
	w.unchanged(t, got)
}

// Scenario: the Inbox lists the items of one project, or those of none, and counts them so.
func TestTheListIsFilteredByProject(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()
	vmap := w.project(t, "VMAP")
	bp := w.project(t, "BeyondPilot")

	add := func(title, project string) inboxstore.Todo {
		td, err := w.inbox.Create(ctx, owner, inboxstore.NewTodo{
			Title: title, State: inboxstore.Open, Source: inboxstore.Source{Kind: inboxstore.ByHand}, Assignee: me,
			Project: project,
		})
		require.NoError(t, err)

		return td
	}
	a, b, none := add("a", vmap.ID), add("b", bp.ID), add("c", "")

	for project, want := range map[string]inboxstore.Todo{vmap.ID: a, bp.ID: b, inboxstore.NoProject: none} {
		page, err := w.inbox.List(ctx, inboxstore.Filter{Project: project}, "", 0)
		require.NoError(t, err)
		require.Len(t, page.Items, 1, project)
		assert.Equal(t, want.ID, page.Items[0].ID, project)
		assert.Equal(t, 1, page.Total, project)
	}

	every, err := w.inbox.List(ctx, inboxstore.Filter{}, "", 0)
	require.NoError(t, err)
	assert.Equal(t, 3, every.Total)
}

// Scenario (issue #1188): the projects are the sidebar's departments, in its order, found by a name in any case or
// by id. A department renamed there keeps its items; one removed there leaves each of its items, closed ones too,
// without a project.
func TestTheProjectsAreTheDepartments(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()
	vmap := w.project(t, "VMAP")
	bp := w.project(t, "BeyondPilot")

	all, err := w.inbox.Projects(ctx)
	require.NoError(t, err)
	assert.Equal(t, []inboxstore.Project{{ID: vmap.ID, Name: "VMAP"}, {ID: bp.ID, Name: "BeyondPilot"}}, all,
		"in the sidebar's order, not by name")

	open, err := w.inbox.Create(ctx, owner, inboxstore.NewTodo{
		Title: "a", State: inboxstore.Open, Source: inboxstore.Source{Kind: inboxstore.ByHand}, Assignee: me,
		Project: vmap.ID,
	})
	require.NoError(t, err)
	closed := w.done(t)
	closed, err = w.inbox.Reopen(ctx, by(owner, closed))
	require.NoError(t, err)
	closed, err = w.inbox.Edit(ctx, by(owner, closed), inboxstore.Edit{Project: &vmap.ID})
	require.NoError(t, err)
	closed, err = w.inbox.Drop(ctx, by(owner, closed), "Not needed", nil)
	require.NoError(t, err)

	_, err = w.svc.RenameDepartment(ctx, vmap.ID, "VMAP AI")
	require.NoError(t, err)

	found, err := w.inbox.ProjectNamed(ctx, "vmap ai")
	require.NoError(t, err)
	assert.Equal(t, inboxstore.Project{ID: vmap.ID, Name: "VMAP AI"}, found)

	found, err = w.inbox.ProjectNamed(ctx, bp.ID)
	require.NoError(t, err)
	assert.Equal(t, "BeyondPilot", found.Name)

	_, err = w.inbox.ProjectNamed(ctx, "Personal")
	require.ErrorIs(t, err, storeerr.ErrNotFound, "no project but a department")
	require.ErrorContains(t, err, "VMAP AI, BeyondPilot")

	kept, err := w.inbox.Get(ctx, open.ID)
	require.NoError(t, err)
	assert.Equal(t, vmap.ID, kept.Project, "a rename keeps its items")

	require.NoError(t, w.svc.DeleteDepartment(ctx, vmap.ID))

	all, err = w.inbox.Projects(ctx)
	require.NoError(t, err)
	assert.Equal(t, []inboxstore.Project{{ID: bp.ID, Name: "BeyondPilot"}}, all)

	for _, before := range []inboxstore.Todo{open, closed} {
		after, err := w.inbox.Get(ctx, before.ID)
		require.NoError(t, err)
		assert.Empty(t, after.Project, "its department was removed")
		assert.Equal(t, before.State, after.State)
	}

	_, err = w.inbox.Edit(ctx, by(owner, open), inboxstore.Edit{Project: &vmap.ID})
	require.ErrorIs(t, err, storeerr.ErrNotFound, "a removed department is no project")
}

// Scenario (issue #1188): an item given or passed to a Bot in a department, without a project, takes that
// department, in the write that gives it, whose event says so; one with a project keeps it, and one given to a Bot
// in no department, or to the owner, stays without one.
func TestAnItemGivenToABotInADepartmentTakesIt(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()
	vmap := w.project(t, "VMAP", w.lan)
	bp := w.project(t, "BeyondPilot")

	handed, err := w.inbox.Hand(ctx, by(owner, w.fromChat(t)), w.lan, "")
	require.NoError(t, err)
	assert.Equal(t, vmap.ID, handed.Project, "handed by the owner")

	last := handed.Events[len(handed.Events)-1]
	_, after := eventFields(t, last)
	assert.Equal(t, vmap.ID, after["project_id"])

	main := orchestration.DefaultBotID
	chat, err := w.svc.CreateChat(ctx, main, "Hypo")
	require.NoError(t, err)
	found, err := w.inbox.Create(ctx, bot(main), inboxstore.NewTodo{
		Title: "Answer the VMAP ticket", State: inboxstore.Open, Assignee: inboxstore.Assignee{Kind: inboxstore.ABot,
			Name: main},
		Source: inboxstore.Source{Kind: inboxstore.FromChat, ID: chat.ID},
	})
	require.NoError(t, err)
	assert.Empty(t, found.Project, "the main Bot is in no department")

	passed, err := w.inbox.Edit(ctx, by(bot(main), found), inboxstore.Edit{
		Assignee: &inboxstore.Assignee{Kind: inboxstore.ABot, Name: w.lan},
	})
	require.NoError(t, err)
	assert.Equal(t, vmap.ID, passed.Project, "passed by the main Bot")

	back, err := w.inbox.Edit(ctx, by(bot(w.lan), passed), inboxstore.Edit{
		Assignee: &inboxstore.Assignee{Kind: inboxstore.ABot, Name: main},
	})
	require.NoError(t, err)
	assert.Equal(t, vmap.ID, back.Project, "given back, it keeps its project")

	added, err := w.inbox.Create(ctx, owner, inboxstore.NewTodo{
		Title: "Book the room", State: inboxstore.Open, Source: inboxstore.Source{Kind: inboxstore.ByHand},
		Assignee: inboxstore.Assignee{Kind: inboxstore.ABot, Name: w.lan},
	})
	require.NoError(t, err)
	assert.Equal(t, vmap.ID, added.Project, "added for the Bot")

	theirs, err := w.inbox.Create(ctx, owner, inboxstore.NewTodo{
		Title: "Send the deck", State: inboxstore.Open, Source: inboxstore.Source{Kind: inboxstore.ByHand},
		Assignee: me, Project: bp.ID,
	})
	require.NoError(t, err)
	theirs, err = w.inbox.Hand(ctx, by(owner, theirs), w.lan, "")
	require.NoError(t, err)
	assert.Equal(t, bp.ID, theirs.Project, "an item with a project keeps it")

	kais, err := w.inbox.Hand(ctx, by(owner, w.fromChat(t)), w.kai, "")
	require.NoError(t, err)
	assert.Empty(t, kais.Project, "kai is in no department")
}
