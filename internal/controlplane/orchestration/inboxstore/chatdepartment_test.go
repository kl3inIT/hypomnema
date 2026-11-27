package inboxstore_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/inboxstore"
)

// askIn is an ask kai found in a chat of the owner's account, by service, account and chat.
func (w *world) askIn(t *testing.T, kind, account, chat string, project string) inboxstore.Todo {
	t.Helper()

	td, err := w.inbox.Create(t.Context(), bot(w.kai), inboxstore.NewTodo{
		Title: "Trả lời: xem giúp số liệu tuần", State: inboxstore.Open, Assignee: me,
		WhyOpen: inboxstore.AskedDirectly, Project: project,
		Source: inboxstore.Source{Kind: kind, ID: chat, Account: account, Quote: "anh xem giúp em số liệu tuần nhé",
			Ref: &inboxstore.Link{Kind: kind + "_message", Ref: "m_" + chat}},
	})
	require.NoError(t, err)

	return td
}

// Scenario: issue #1206, the department rule the owner approved on 2026-11-27. The triage's item from a Zalo group
// has no department; the owner puts it in VMAP. That teaches the chat: the next item a Bot adds from the same group
// of the same account, without a department, takes VMAP, as Linear's Asks put every request of a Slack channel in
// that channel's team. Another chat, or the same chat id of another account, learns nothing; a department the Bot
// names stays its own; a Bot's change teaches nothing; and the owner taking the item out of its department forgets
// the chat.
func TestTheOwnersDepartmentChangeTeachesItsChat(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newWorld(t)
	vmap := w.project(t, "VMAP")
	beyond := w.project(t, "BeyondPilot")

	first := w.askIn(t, inboxstore.FromZalo, "main", "g_data", "")
	assert.Empty(t, first.Project, "nothing is known of the chat")

	moved, err := w.inbox.Edit(ctx, by(owner, first), inboxstore.Edit{Project: &vmap.ID})
	require.NoError(t, err)
	assert.Equal(t, vmap.ID, moved.Project)

	assert.Equal(t, vmap.ID, w.askIn(t, inboxstore.FromZalo, "main", "g_data", "").Project, "the chat is known")
	assert.Empty(t, w.askIn(t, inboxstore.FromZalo, "main", "g_other", "").Project, "another chat")
	assert.Empty(t, w.askIn(t, inboxstore.FromWhatsApp, "main", "g_data", "").Project, "another service")
	assert.Equal(t, beyond.ID, w.askIn(t, inboxstore.FromZalo, "main", "g_data", beyond.ID).Project,
		"a department the Bot names stays")

	// A Bot's change teaches nothing.
	other := w.askIn(t, inboxstore.FromLark, "me", "oc_team", "")
	_, err = w.inbox.Edit(ctx, by(bot(w.kai), other), inboxstore.Edit{Project: &beyond.ID})
	require.NoError(t, err)
	assert.Empty(t, w.askIn(t, inboxstore.FromLark, "me", "oc_team", "").Project, "a Bot's change")

	// The owner moves an item to BeyondPilot, then out of any department: the chat follows him each time.
	again := w.askIn(t, inboxstore.FromZalo, "main", "g_data", "")
	again, err = w.inbox.Edit(ctx, by(owner, again), inboxstore.Edit{Project: &beyond.ID})
	require.NoError(t, err)
	assert.Equal(t, beyond.ID, w.askIn(t, inboxstore.FromZalo, "main", "g_data", "").Project, "his latest change")

	none := ""
	_, err = w.inbox.Edit(ctx, by(owner, again), inboxstore.Edit{Project: &none})
	require.NoError(t, err)
	assert.Empty(t, w.askIn(t, inboxstore.FromZalo, "main", "g_data", "").Project, "he took it out of any")
}

// Scenario: review round 4 of PR #1243. The chat's department reaches an item the triage already listed too: once
// the owner put the chat in a department, the triage's next update of an older item of that chat without one, which
// names no project (it makes the ask his promise), puts it there. An item with a department, an update naming one,
// and the owner's own update keep theirs.
func TestAnUpdateTakesTheChatsDepartment(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newWorld(t)
	atlas := w.project(t, "Atlas")
	harbor := w.project(t, "Harbor")
	promised := inboxstore.Promised

	older := w.askIn(t, inboxstore.FromZalo, "main", "g_data", "")
	kept := w.askIn(t, inboxstore.FromZalo, "main", "g_data", harbor.ID)
	named := w.askIn(t, inboxstore.FromZalo, "main", "g_data", "")
	his := w.askIn(t, inboxstore.FromZalo, "main", "g_data", "")

	taught := w.askIn(t, inboxstore.FromZalo, "main", "g_data", "")
	_, err := w.inbox.Edit(ctx, by(owner, taught), inboxstore.Edit{Project: &atlas.ID})
	require.NoError(t, err)

	got, err := w.inbox.Edit(ctx, by(bot(w.kai), older), inboxstore.Edit{WhyOpen: &promised})
	require.NoError(t, err)
	assert.Equal(t, inboxstore.Promised, got.WhyOpen)
	assert.Equal(t, atlas.ID, got.Project, "the chat's department")

	got, err = w.inbox.Edit(ctx, by(bot(w.kai), kept), inboxstore.Edit{WhyOpen: &promised})
	require.NoError(t, err)
	assert.Equal(t, harbor.ID, got.Project, "an item with a department keeps it")

	got, err = w.inbox.Edit(ctx, by(bot(w.kai), named), inboxstore.Edit{Project: &harbor.ID})
	require.NoError(t, err)
	assert.Equal(t, harbor.ID, got.Project, "an update naming one")

	title := "Trả lời: xem lại số liệu tuần"
	got, err = w.inbox.Edit(ctx, by(owner, his), inboxstore.Edit{Title: &title})
	require.NoError(t, err)
	assert.Empty(t, got.Project, "the owner's own update")
}

// Scenario: review round 5 of PR #1243. The triage moves an item it listed before its chat was known to Waiting on
// others: the move takes the chat's department, as the triage's other updates do, in the same write; an item with a
// department keeps it, and one from a chat nobody taught stays without one. The move is the same as ever: given to
// the person who took it, due 48 hours later.
func TestAMoveToWaitingTakesTheChatsDepartment(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newWorld(t)
	atlas := w.project(t, "Atlas")
	harbor := w.project(t, "Harbor")

	older := w.zaloAsk(t, w.kai)
	kept := w.askIn(t, inboxstore.FromZalo, "main", "team", harbor.ID)
	unknown := w.askIn(t, inboxstore.FromZalo, "main", "g_unknown", "")

	taught := w.askIn(t, inboxstore.FromZalo, "main", "team", "")
	_, err := w.inbox.Edit(ctx, by(owner, taught), inboxstore.Edit{Project: &atlas.ID})
	require.NoError(t, err)

	moved, err := w.inbox.WaitOn(ctx, by(bot(w.kai), older), took("Việt"))
	require.NoError(t, err)
	assert.Equal(t, atlas.ID, moved.Project, "the chat's department")
	assert.Equal(t, inboxstore.Assignee{Kind: inboxstore.Person, Name: "Việt"}, moved.Assignee)
	assert.Equal(t, inboxstore.Waiting, moved.WhyOpen)
	assert.Equal(t, t0.Add(48*time.Hour), moved.Due.At.UTC())

	e := last(moved)
	_, after := eventFields(t, e)
	assert.Equal(t, atlas.ID, after["project_id"], "recorded in the move's own event")

	moved, err = w.inbox.WaitOn(ctx, by(bot(w.kai), kept), took("Việt"))
	require.NoError(t, err)
	assert.Equal(t, harbor.ID, moved.Project, "an item with a department keeps it")

	moved, err = w.inbox.WaitOn(ctx, by(bot(w.kai), unknown), took("Việt"))
	require.NoError(t, err)
	assert.Empty(t, moved.Project, "a chat nobody taught")
}
