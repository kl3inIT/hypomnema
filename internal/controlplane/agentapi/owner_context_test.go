package agentapi_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/inboxstore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/memorystore"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// wake starts a run of the triage Routine, has its check wake the Bot, and gives the token of the run's pi, as the
// coordinator makes it for the run's chat.
func (sw *secretaryWorld) wake() string {
	sw.t.Helper()

	_, runID := sw.check()
	woke, err := sw.svc.WakeRoutineRun(sw.t.Context(), runID, "- Lark chat ThanhTV: 1 new\n")
	require.NoError(sw.t, err)

	pi, err := sw.svc.AddBotToken(sw.t.Context(), "pi", woke.Run.ChatID, sw.bot.ID)
	require.NoError(sw.t, err)

	return pi
}

func memoryArgs(t *testing.T, args map[string]any) string {
	t.Helper()

	raw, err := json.Marshal(args)
	require.NoError(t, err)

	return string(raw)
}

// Scenario: issue #1119, items 2 and 6: the triage Routine's read run learns. At its end the Bot writes what it
// learned, each entry with where it was read, into its note /memories/context/owner.md, which every prompt run of its
// Routines begins with. That note is the only part of its memory a read run changes: MEMORY.md, which is in every
// chat's prompt, USER.md and the other notes stay as they are, and the note is not removed or moved. The note keeps a
// note's limit: a change past it is refused and changes nothing, so the Bot merges and prunes instead (Hermes's
// memory tool, tools/memory_tool_store.py).
func TestAReadRunKeepsTheOwnersContextAndNoOtherMemory(t *testing.T) {
	t.Parallel()

	sw := newSecretaryWorld(t)
	ctx := t.Context()
	pi := sw.wake()
	dir := memorystore.BotDir(sw.bot.ID)

	_, err := sw.svc.Memories().WriteNote(ctx, dir, "/memories/people.md", "Kai: GenAI Fund.")
	require.NoError(t, err)

	people := "## People\n- Laura Nguyen (cô Laura): his lead at VTII; Vietnamese (lark chat oc_9, message om_1)\n"
	process := "## Recurring processes\n- ThanhTV sends a CV on Lark: add it to cô Laura's candidate sheet, tell anh " +
		"Thanh (lark chat oc_1, messages om_19, om_20)\n"

	for _, args := range []map[string]any{
		{"command": memorystore.Create, "path": memorystore.ContextFile, "file_text": people},
		{"command": memorystore.Insert, "path": memorystore.ContextFile, "insert_line": 2, "insert_text": process},
		{"command": memorystore.StrReplace, "path": memorystore.ContextFile, "old_str": "his lead at VTII",
			"new_str": "his lead at VTII, VMAP"},
	} {
		_, text, failed := sw.use(pi, agenttool.ToolMemory, memoryArgs(t, args))
		require.False(t, failed, "%v: %s", args["command"], text)
	}

	note, err := sw.svc.Memories().ReadNote(ctx, dir, memorystore.ContextFile)
	require.NoError(t, err)
	assert.Equal(t, strings.Replace(people, "VTII", "VTII, VMAP", 1)+process, note.Text)

	for _, args := range []map[string]any{
		{"command": memorystore.Create, "path": "/memories/thanh.md", "file_text": "CVs go to the sheet."},
		{"command": memorystore.StrReplace, "path": "/memories/people.md", "old_str": "Kai", "new_str": "Kai Yong"},
		{"command": memorystore.Insert, "path": "/memories/MEMORY.md", "insert_line": 0, "insert_text": "x"},
		{"command": memorystore.Insert, "path": "/memories/USER.md", "insert_line": 0, "insert_text": "x"},
		{"command": memorystore.Delete, "path": memorystore.ContextFile},
		{"command": memorystore.Rename, "old_path": memorystore.ContextFile, "new_path": "/memories/old.md"},
		{"command": memorystore.Create, "path": "/memories/context/other.md", "file_text": "x"},
	} {
		_, text, failed := sw.use(pi, agenttool.ToolMemory, memoryArgs(t, args))
		assert.True(t, failed, "%v", args)
		assert.Contains(t, text, agenttool.ReadRunRefusal, "%v", args)
	}

	notes, err := sw.svc.Memories().Notes(ctx, dir)
	require.NoError(t, err)
	require.Len(t, notes, 2, "no note was made, removed or moved")

	kept, err := sw.svc.Memories().ReadNote(ctx, dir, "/memories/people.md")
	require.NoError(t, err)
	assert.Equal(t, "Kai: GenAI Fund.", kept.Text)

	bot, err := sw.svc.GetBot(ctx, sw.bot.ID)
	require.NoError(t, err)
	assert.Empty(t, bot.Memory, "MEMORY.md is unchanged")

	_, text, failed := sw.use(pi, agenttool.ToolMemory, memoryArgs(t, map[string]any{
		"command": memorystore.Insert, "path": memorystore.ContextFile, "insert_line": 0,
		"insert_text": strings.Repeat("x", memorystore.MaxNoteBytes),
	}))
	assert.True(t, failed, text)
	assert.Contains(t, text, "over a note's limit")

	after, err := sw.svc.Memories().ReadNote(ctx, dir, memorystore.ContextFile)
	require.NoError(t, err)
	assert.Equal(t, note.Text, after.Text, "a change past the limit changes nothing")
}

// Scenario: issue #1119, item 4: the owner corrects the Bot between two triage runs. He adds an item by hand and
// drops one the Bot proposed; the Bot's other item he leaves alone. The next run lists, with inbox_list's
// owner_changed_since, the two he touched with what he did, closed ones too, and not the one he left alone.
func TestATriageRunSeesTheOwnersCorrections(t *testing.T) {
	t.Parallel()

	sw := newSecretaryWorld(t)
	ctx := t.Context()
	pi := sw.wake()

	proposed := sw.inbox(pi, agenttool.ToolInboxAdd, map[string]any{"title": "Maybe answer Linh's mail"})
	untouched := sw.inbox(pi, agenttool.ToolInboxAdd, map[string]any{"title": "Maybe send Lan the slides"})

	_, err := sw.svc.Inbox().Drop(ctx, inboxstore.Change{
		Actor: inboxstore.Actor{Kind: inboxstore.Owner}, ID: proposed.ID, Revision: proposed.Revision,
	}, "Linh's mail is Lan's.", nil)
	require.NoError(t, err)

	byHand, err := sw.svc.Inbox().Create(ctx, inboxstore.Actor{Kind: inboxstore.Owner}, inboxstore.NewTodo{
		Title: "Add ThanhTV's CV to cô Laura's candidate sheet", State: inboxstore.Open,
		Source: inboxstore.Source{Kind: inboxstore.ByHand}, Assignee: inboxstore.Assignee{Kind: inboxstore.Me},
	})
	require.NoError(t, err)

	_, text, failed := sw.use(pi, agenttool.ToolInboxList, `{"owner_changed_since":"2000-01-01T00:00:00Z"}`)
	require.False(t, failed, text)

	var page struct {
		Total int `json:"total"`
		Items []struct {
			ID      string           `json:"id"`
			State   string           `json:"state"`
			History []map[string]any `json:"history"`
		} `json:"items"`
	}
	require.NoError(t, json.Unmarshal([]byte(text), &page), text)

	assert.Equal(t, 2, page.Total)

	got := map[string]int{}
	for i, item := range page.Items {
		got[item.ID] = i
	}

	require.Contains(t, got, proposed.ID)
	require.Contains(t, got, byHand.ID)
	assert.NotContains(t, got, untouched.ID, "the owner left it alone")

	dropped := page.Items[got[proposed.ID]]
	assert.Equal(t, inboxstore.Dropped, dropped.State)
	require.Len(t, dropped.History, 1, "only his change, not the Bot's create")
	assert.Equal(t, "owner", dropped.History[0]["by"])
	assert.Contains(t, dropped.History[0]["after"], "close_reason")

	_, text, failed = sw.use(pi, agenttool.ToolInboxList, `{"owner_changed_since":"yesterday"}`)
	assert.True(t, failed, text)
	assert.Contains(t, text, "RFC 3339")
}
