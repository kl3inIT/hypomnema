package inboxstore_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

func (w *world) project(t *testing.T, name, color string) inboxstore.Project {
	t.Helper()

	p, err := w.inbox.CreateProject(t.Context(), name, color)
	require.NoError(t, err)

	return p
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
	vmap := w.project(t, "VMAP", "#2f80ed")

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
// with its priority and is one line, a project is one that is there. A refused change writes nothing.
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

	_, err := w.inbox.Edit(ctx, by(owner, td), inboxstore.Edit{Project: strp("prj_gone")})
	require.ErrorIs(t, err, storeerr.ErrNotFound)

	_, err = w.inbox.Create(ctx, owner, inboxstore.NewTodo{
		Title: "x", State: inboxstore.Open, Source: inboxstore.Source{Kind: inboxstore.ByHand}, Assignee: me, Priority: 9,
	})
	require.ErrorIs(t, err, storeerr.ErrInvalid)

	_, err = w.inbox.Create(ctx, owner, inboxstore.NewTodo{
		Title: "x", State: inboxstore.Open, Source: inboxstore.Source{Kind: inboxstore.ByHand}, Assignee: me, Project: "prj_gone",
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
	vmap := w.project(t, "VMAP", "#2f80ed")
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
	vmap := w.project(t, "VMAP", "#2f80ed")
	bp := w.project(t, "BeyondPilot", "#27ae60")

	add := func(title, project string) inboxstore.Todo {
		td, err := w.inbox.Create(ctx, owner, inboxstore.NewTodo{
			Title: title, State: inboxstore.Open, Source: inboxstore.Source{Kind: inboxstore.ByHand}, Assignee: me, Project: project,
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

// Scenario: the owner keeps the list of projects: he adds them, a name once whatever its case and a colour as
// #rrggbb; renames one, which its items keep; and removes one, which leaves each of its items, closed ones too,
// without a project, with an event that says so.
func TestTheOwnerAddsRenamesAndRemovesProjects(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()
	vmap := w.project(t, "VMAP", "#2F80ED")
	assert.Equal(t, "#2f80ed", vmap.Color, "kept in lower case")

	_, err := w.inbox.CreateProject(ctx, "vmap", "#000000")
	require.ErrorIs(t, err, storeerr.ErrConflict)
	_, err = w.inbox.CreateProject(ctx, "Other", "blue")
	require.ErrorIs(t, err, storeerr.ErrInvalid)
	_, err = w.inbox.CreateProject(ctx, " ", "#000000")
	require.ErrorIs(t, err, storeerr.ErrInvalid)

	open, err := w.inbox.Create(ctx, owner, inboxstore.NewTodo{
		Title: "a", State: inboxstore.Open, Source: inboxstore.Source{Kind: inboxstore.ByHand}, Assignee: me, Project: vmap.ID,
	})
	require.NoError(t, err)
	closed := w.done(t)
	closed, err = w.inbox.Reopen(ctx, by(owner, closed))
	require.NoError(t, err)
	closed, err = w.inbox.Edit(ctx, by(owner, closed), inboxstore.Edit{Project: &vmap.ID})
	require.NoError(t, err)
	closed, err = w.inbox.Drop(ctx, by(owner, closed), "Not needed", nil)
	require.NoError(t, err)

	renamed, err := w.inbox.EditProject(ctx, vmap.ID, strp("VMAP AI"), nil)
	require.NoError(t, err)
	assert.Equal(t, "VMAP AI", renamed.Name)
	assert.Equal(t, vmap.Color, renamed.Color)

	found, err := w.inbox.ProjectNamed(ctx, "vmap ai")
	require.NoError(t, err)
	assert.Equal(t, vmap.ID, found.ID)

	require.NoError(t, w.inbox.DeleteProject(ctx, owner, vmap.ID))

	all, err := w.inbox.Projects(ctx)
	require.NoError(t, err)
	assert.Empty(t, all)

	for _, before := range []inboxstore.Todo{open, closed} {
		after, err := w.inbox.Get(ctx, before.ID)
		require.NoError(t, err)
		assert.Empty(t, after.Project)
		assert.Equal(t, before.Revision+1, after.Revision)
		assert.Equal(t, before.State, after.State)

		last := after.Events[len(after.Events)-1]
		was, now := eventFields(t, last)
		assert.Equal(t, owner, last.Actor)
		assert.Equal(t, vmap.ID, was["project_id"])
		assert.Nil(t, now["project_id"])
		assert.Equal(t, "Project VMAP AI was removed", now["reason"])
	}

	require.ErrorIs(t, w.inbox.DeleteProject(ctx, owner, vmap.ID), storeerr.ErrNotFound)
}
