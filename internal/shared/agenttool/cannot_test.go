package agenttool

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Scenario (issue #567, rule 8): an agent given the Lark app "work", which
// the control plane found has no scope for mail and no right to delete
// messages, runs lark-cli. Before the tool runs it is told, in one line and
// service by service, what the account cannot do; an account that can do all
// it was read for is run without the line.
func TestTheLauncherSaysWhatTheAccountCannotDoBeforeTheToolRuns(t *testing.T) {
	home := installedAs(t, LarkCLI())
	c := newControlPlane(t, Connection{Label: "work", Account: "Hypomnema", ClientID: "cli_a1b2", Cannot: []Cannot{
		{Action: "lark.im.risky", Service: "Messages", Name: "Delete and other high-risk changes", Reason: "no scope"},
		{Action: "lark.mail.read", Service: "Mail", Name: "Read", Reason: "no scope"},
		{Action: "lark.mail.write", Service: "Mail", Name: "Create or change", Reason: "no scope"},
	}}, Connection{Label: "report", Account: "Superset Report", ClientID: "cli_report"})

	var out, errs bytes.Buffer

	launcher := c.launcher(home, &out)
	launcher.Stderr = &errs

	require.NoError(t, launcher.Run(t.Context(), "lark-cli", []string{"--account", "work", "im", "+chat-search"}))
	assert.True(t, strings.HasPrefix(errs.String(), "hypomnema: lark:work cannot do these at the service: "+
		"Messages: Delete and other high-risk changes; Mail: Read, Create or change. A command that needs one is "+
		"refused before it is sent; do not try it, tell the owner what is missing.\n"), errs.String())

	errs.Reset()
	require.NoError(t, launcher.Run(t.Context(), "lark-cli", []string{"--account", "report", "im", "+chat-search"}))
	assert.NotContains(t, errs.String(), "cannot")
}

// The line itself, which the scenario above checks only where a tool can run (not on Windows).
func TestTheCannotLineNamesEachServiceOnce(t *testing.T) {
	t.Parallel()

	list := ConnectionList{Data: []Connection{{Label: "prod", Cannot: []Cannot{
		{Service: "Logs", Name: "Read"}, {Service: "Queries", Name: "Read"}, {Service: "Queries", Name: "Delete"},
	}}, {Label: "dev"}}}

	assert.Equal(t, "hypomnema: superset:prod cannot do these at the service: Logs: Read; Queries: Read, Delete. "+
		"A command that needs one is refused before it is sent; do not try it, tell the owner what is missing.",
		list.cannotLine("superset", "prod"))
	assert.Empty(t, list.cannotLine("superset", "dev"))
	assert.Empty(t, list.cannotLine("superset", "other"))
}
