package clientcli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
)

// Scenario (issue #1157): the owner adds a to-do with a priority and a project by its name, changes them with edit,
// lists a project's to-dos, and show prints them. An unknown priority or project is refused before anything changes.
func TestInboxPriorityAndProjectFromTheCLI(t *testing.T) {
	t.Parallel()

	w, other := inboxCLI(t)

	vmap, err := w.svc.Inbox().CreateProject(t.Context(), "VMAP", "#2f80ed")
	require.NoError(t, err)

	var td openapi.Todo
	w.jsonCmd(t, &td, "", "inbox", "add", "Trả lời cô Laura", "--priority", "urgent", "--priority-reason",
		"Her deadline is today", "--project", "vmap")
	assert.Equal(t, 1, td.Priority)
	assert.Equal(t, "Her deadline is today", td.PriorityReason)
	assert.Equal(t, vmap.ID, *td.ProjectId)

	var edited openapi.Todo
	w.jsonCmd(t, &edited, "", "inbox", "edit", td.Id, "--priority", "3", "--project", "none")
	assert.Equal(t, 3, edited.Priority)
	assert.Empty(t, edited.PriorityReason)
	assert.Nil(t, edited.ProjectId)

	w.jsonCmd(t, &edited, "", "inbox", "edit", other.Id, "--project", "VMAP")

	out, err := w.cmd(t, "inbox", "list", "--project", "VMAP")
	require.NoError(t, err)
	assert.Contains(t, out, other.Id)
	assert.NotContains(t, out, td.Id)

	out, err = w.cmd(t, "inbox", "show", td.Id)
	require.NoError(t, err)
	assert.Contains(t, out, "Priority: medium")

	_, err = w.cmd(t, "inbox", "edit", td.Id, "--priority", "asap")
	require.ErrorContains(t, err, "--priority")

	_, err = w.cmd(t, "inbox", "add", "x", "--project", "SecondBite")
	require.ErrorContains(t, err, "VMAP")
}
