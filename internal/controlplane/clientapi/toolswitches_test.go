package clientapi

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
)

// Scenario: the owner reads Hypomnema's tools by family and turns a Bot's
// browser off by its family (issue #536, step 7). A change that leaves the
// switches out keeps them; a name no tool has is refused.
func TestTheOwnerSwitchesABotsTools(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	owner, _ := w.token("web", bearertoken.Owner)
	ctx := t.Context()

	listed, err := owner.ListToolsWithResponse(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, listed.StatusCode())

	patterns := map[string]string{}
	for _, f := range listed.JSON200.Data {
		patterns[f.Name] = f.Pattern
	}

	require.Equal(t, "agent_browser_*", patterns["browser"])

	persona := "You read pages."
	made, err := owner.CreateBotWithResponse(ctx, openapi.NewBot{
		Name: "reader", Settings: &openapi.BotSettings{Persona: &persona},
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, made.StatusCode())
	assert.Empty(t, made.JSON201.ToolsOff)

	off := []string{patterns["browser"]}
	set, err := owner.PutBotWithResponse(ctx, made.JSON201.Id, openapi.BotSettings{Persona: &persona, ToolsOff: &off})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, set.StatusCode())
	assert.Equal(t, off, set.JSON200.ToolsOff)

	kept, err := owner.PutBotWithResponse(ctx, made.JSON201.Id, openapi.BotSettings{Persona: &persona})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, kept.StatusCode())
	assert.Equal(t, off, kept.JSON200.ToolsOff, "a change that leaves them out keeps them")

	unknown := []string{"browser"}
	bad, err := owner.PutBotWithResponse(ctx, made.JSON201.Id, openapi.BotSettings{Persona: &persona, Tools: &unknown})
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, bad.StatusCode())
}
