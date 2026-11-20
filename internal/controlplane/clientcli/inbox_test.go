package clientcli

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/inboxstore"
)

const hcm = "Asia/Ho_Chi_Minh"

// inboxCLI is the owner's CLI over the Inbox, with one item he wrote.
func inboxCLI(t *testing.T) (*world, openapi.Todo) {
	t.Helper()

	w := newWorld(t)
	w.login(t, bearertoken.Owner)

	var td openapi.Todo
	w.jsonCmd(t, &td, "", "inbox", "add", "Send Linh the contract", "--due", "2026-11-25", "--tz", hcm,
		"--who", "Linh", "--notes", "The signed one", "--done-when", "Linh has it")

	return w, td
}

// proposal is an item a Routine's Bot found, put in the store as part 3's tools will.
func proposal(t *testing.T, w *world, title string) string {
	t.Helper()

	td, err := w.svc.Inbox().Create(t.Context(), inboxstore.Actor{Kind: inboxstore.Bot, ID: orchestration.DefaultBotID},
		inboxstore.NewTodo{Title: title, State: inboxstore.Proposed, Assignee: inboxstore.Assignee{Kind: inboxstore.Me},
			Source: inboxstore.Source{Kind: inboxstore.FromRoutine, ID: "rrn_1"}})
	require.NoError(t, err)

	return td.ID
}

// Scenario: the owner adds a to-do with a due date, which stays a date in his zone, for a person; and one due at a
// time. A due date without a zone is refused before anything is sent.
func TestInboxAddFromTheCLI(t *testing.T) {
	t.Parallel()

	w, td := inboxCLI(t)
	assert.Equal(t, openapi.TodoStateOpen, td.State)
	assert.Equal(t, openapi.TodoDueKindDate, td.Due.Kind)
	assert.Equal(t, "2026-11-25", td.Due.Date.Format(time.DateOnly))
	assert.Equal(t, hcm, *td.Due.Zone)
	linh := "Linh"
	assert.Equal(t, openapi.TodoAssignee{Kind: openapi.TodoAssigneeKindPerson, Name: &linh}, td.Assignee)
	assert.Equal(t, "The signed one", td.Notes)
	assert.Equal(t, "Linh has it", td.DoneWhen)
	assert.Equal(t, openapi.TodoSourceKindOwner, td.Source.Kind)

	out, err := w.cmd(t, "inbox", "add", "Call the bank", "--due", "2026-11-26 14:30", "--tz", hcm)
	require.NoError(t, err)
	assert.Contains(t, out, "2026-11-26 14:30")
	assert.Contains(t, out, "Call the bank")

	_, err = w.cmd(t, "inbox", "add", "Pay", "--due", "2026-11-26", "--tz", "Mars/Olympus")
	require.ErrorContains(t, err, "--due needs --tz")

	_, err = w.cmd(t, "inbox", "add", "Pay", "--due", "Friday", "--tz", hcm)
	require.ErrorContains(t, err, "is not YYYY-MM-DD")
}

// Scenario: the list prints one line a to-do and filters as the API does; a closed item is left out unless
// --closed, a stale-free proposal is listed; show prints the item and its history.
func TestInboxListAndShowFromTheCLI(t *testing.T) {
	t.Parallel()

	w, td := inboxCLI(t)
	found := proposal(t, w, "Answer Minh")

	out, err := w.cmd(t, "inbox", "list")
	require.NoError(t, err)
	assert.Contains(t, out, td.Id+"  open")
	assert.Contains(t, out, "2026-11-25")
	assert.Contains(t, out, "Linh")
	assert.Contains(t, out, found+"  proposed")

	out, err = w.cmd(t, "inbox", "list", "--state", "proposed")
	require.NoError(t, err)
	assert.NotContains(t, out, td.Id)
	assert.Contains(t, out, found)

	var list openapi.TodoList
	w.jsonCmd(t, &list, "", "inbox", "list", "--who", "Linh")
	require.Len(t, list.Data, 1)
	assert.Equal(t, td.Id, list.Data[0].Id)
	assert.Equal(t, 1, list.Total)

	w.jsonCmd(t, &list, "", "inbox", "list", "--needs-you")
	require.Len(t, list.Data, 1)
	assert.Equal(t, found, list.Data[0].Id, "a proposal needs the owner")

	out, err = w.cmd(t, "inbox", "list", "--source", "zalo")
	require.NoError(t, err)
	assert.Equal(t, "No to-dos.\n", out)

	out, err = w.cmd(t, "inbox", "show", td.Id)
	require.NoError(t, err)
	assert.Contains(t, out, "Send Linh the contract\n"+td.Id+" · open · due 2026-11-25 · Linh · revision 1")
	assert.Contains(t, out, "Done when: Linh has it")
	assert.Contains(t, out, "History:")
	assert.Contains(t, out, "created")
}

// Scenario: edit reads the item's revision and sends it; the fields given change, a flag set to "" clears its
// field, and nothing given is refused.
func TestInboxEditFromTheCLI(t *testing.T) {
	t.Parallel()

	w, td := inboxCLI(t)

	var edited openapi.Todo
	w.jsonCmd(t, &edited, "", "inbox", "edit", td.Id, "--title", "Send Linh the signed contract", "--notes", "",
		"--who", "me", "--due", "none")
	assert.Equal(t, 2, edited.Revision)
	assert.Equal(t, "Send Linh the signed contract", edited.Title)
	assert.Empty(t, edited.Notes)
	assert.Equal(t, openapi.TodoAssigneeKindMe, edited.Assignee.Kind)
	assert.Equal(t, openapi.TodoDueKindNone, edited.Due.Kind)

	w.jsonCmd(t, &edited, "", "inbox", "edit", td.Id, "--done-when", "She signed")
	assert.Equal(t, 3, edited.Revision, "the second edit read the first's revision")

	_, err := w.cmd(t, "inbox", "edit", td.Id)
	require.ErrorContains(t, err, "nothing to change")
}

// Scenario: the owner accepts a proposal; accepting it again is refused by the store, not by the CLI.
func TestInboxAcceptFromTheCLI(t *testing.T) {
	t.Parallel()

	w, _ := inboxCLI(t)
	found := proposal(t, w, "Answer Minh")

	out, err := w.cmd(t, "inbox", "accept", found)
	require.NoError(t, err)
	assert.Contains(t, out, found+"  open")

	_, err = w.cmd(t, "inbox", "accept", found)
	require.ErrorContains(t, err, "conflict")
}

// Scenario: done closes with a URL as evidence; reopen opens it again.
func TestInboxDoneAndReopenFromTheCLI(t *testing.T) {
	t.Parallel()

	w, td := inboxCLI(t)

	var done openapi.Todo
	w.jsonCmd(t, &done, "", "inbox", "done", td.Id, "--evidence", "https://example.com/sent", "--reason", "Mailed")
	assert.Equal(t, openapi.TodoStateDone, done.State)
	assert.Equal(t, []openapi.TodoLink{{Kind: openapi.TodoLinkKindUrl, Ref: "https://example.com/sent"}}, done.Evidence)
	assert.Equal(t, "Mailed", done.CloseReason)
	assert.Equal(t, &openapi.TodoAssignee{Kind: openapi.TodoAssigneeKindMe}, done.DoneBy)

	out, err := w.cmd(t, "inbox", "list")
	require.NoError(t, err)
	assert.NotContains(t, out, td.Id, "a closed item is left out")

	out, err = w.cmd(t, "inbox", "list", "--closed")
	require.NoError(t, err)
	assert.Contains(t, out, td.Id+"  done")

	out, err = w.cmd(t, "inbox", "reopen", td.Id)
	require.NoError(t, err)
	assert.Contains(t, out, td.Id+"  open")
}

// Scenario: drop closes with its reason; duplicate closes one item as a copy of another.
func TestInboxDropAndDuplicateFromTheCLI(t *testing.T) {
	t.Parallel()

	w, td := inboxCLI(t)
	twice := proposal(t, w, "Send the contract")
	other := proposal(t, w, "Answer Minh")

	var dup openapi.Todo
	w.jsonCmd(t, &dup, "", "inbox", "duplicate", twice, td.Id)
	assert.Equal(t, openapi.TodoStateDuplicate, dup.State)
	assert.Equal(t, td.Id, *dup.DuplicateOf)

	var dropped openapi.Todo
	w.jsonCmd(t, &dropped, "", "inbox", "drop", other, "Minh answered himself")
	assert.Equal(t, openapi.TodoStateDropped, dropped.State)
	assert.Equal(t, "Minh answered himself", dropped.CloseReason)
}

// Scenario: snooze hides an item until a time in this machine's zone and off shows it again; a time past is
// refused.
func TestInboxSnoozeFromTheCLI(t *testing.T) {
	t.Parallel()

	w, td := inboxCLI(t)
	until := time.Now().Add(48 * time.Hour).Format("2006-01-02 15:04")

	var snoozed openapi.Todo
	w.jsonCmd(t, &snoozed, "", "inbox", "snooze", td.Id, until)
	require.NotNil(t, snoozed.SnoozedUntil)
	assert.Equal(t, until, snoozed.SnoozedUntil.Local().Format("2006-01-02 15:04"))

	out, err := w.cmd(t, "inbox", "list")
	require.NoError(t, err)
	assert.NotContains(t, out, td.Id)

	out, err = w.cmd(t, "inbox", "list", "--snoozed")
	require.NoError(t, err)
	assert.Contains(t, out, td.Id+"  open (snoozed)")

	var shown openapi.Todo
	w.jsonCmd(t, &shown, "", "inbox", "snooze", td.Id, "off")
	assert.Nil(t, shown.SnoozedUntil)

	_, err = w.cmd(t, "inbox", "snooze", td.Id, "2020-01-01")
	require.ErrorContains(t, err, "invalid_request")
}

// Scenario: a coordinator token reads the Inbox and changes nothing.
func TestACoordinatorOnlyReadsTheInboxFromTheCLI(t *testing.T) {
	t.Parallel()

	w, td := inboxCLI(t)
	w.login(t, bearertoken.Coordinator)

	out, err := w.cmd(t, "inbox", "list")
	require.NoError(t, err)
	assert.Contains(t, out, td.Id)

	_, err = w.cmd(t, "inbox", "add", "Pay")
	require.ErrorContains(t, err, "forbidden")

	_, err = w.cmd(t, "inbox", "done", td.Id)
	require.ErrorContains(t, err, "forbidden")
}
