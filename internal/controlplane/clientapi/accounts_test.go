package clientapi

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/subscription"
)

// signedIn stands in for the service that signs accounts in: it adds the
// state of a sign-in to the accounts orchestration keeps, and removes the
// ones that were signed in here.
type signedIn struct {
	svc     *orchestration.Service
	states  map[string]subscription.State
	expires time.Time
	removed map[string]bool
}

func (s signedIn) List(ctx context.Context) ([]subscription.Account, error) {
	accounts, err := s.svc.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]subscription.Account, 0, len(accounts))

	for _, a := range accounts {
		if s.removed[a.ID] {
			continue
		}

		view := subscription.Account{Account: a, State: s.states[a.ID]}
		if view.State == subscription.StateActive {
			view.AccessTokenExpiresAt = &s.expires
		}

		out = append(out, view)
	}

	return out, nil
}

func (s signedIn) Remove(_ context.Context, id string) error {
	if s.states[id] == "" {
		return subscription.ErrNoTokens
	}

	s.removed[id] = true

	return nil
}

type servedModels []modelgateway.Model

func (m servedModels) ListModels(context.Context) ([]modelgateway.Model, error) { return m, nil }

// Scenario: the owner opens the Models page (issue #199). Each account shows
// whether it is in use and the state of its sign-in; the owner takes one out
// of use, puts it back, and removes the one that was signed in here. The one
// signed in on a node is not removed from here. A coordinator reads the list
// and changes nothing. The models the gateway serves come with their targets
// in the order they are tried.
func TestTheOwnerManagesAccountsFromTheModelsPage(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	here, err := w.svc.AddAccount(ctx, "codex", "main")
	require.NoError(t, err)
	onNode, err := w.svc.AddAccount(ctx, "claude", "laptop")
	require.NoError(t, err)

	expires := time.Date(2026, 11, 19, 8, 12, 0, 0, time.UTC)
	w.srv.WithAccounts(signedIn{
		svc: w.svc, states: map[string]subscription.State{here.ID: subscription.StateActive}, expires: expires,
		removed: map[string]bool{},
	}).WithGatewayModels(servedModels{{
		Name: "claude-sonnet", UpdatedAt: expires,
		Targets: []modelgateway.Target{
			{Provider: "claude-subscription", UpstreamModel: "claude-sonnet-5-5"},
			{Provider: "openrouter", UpstreamModel: "deepseek-v4"},
		},
	}})

	owner, _ := w.token("web", bearertoken.Owner)
	pi, _ := w.token("pi", bearertoken.Coordinator)

	listed, err := pi.ListAccountsWithResponse(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, listed.StatusCode(), string(listed.Body))
	require.Len(t, listed.JSON200.Data, 2)

	byID := map[string]openapi.Account{}
	for _, a := range listed.JSON200.Data {
		byID[a.Id] = a
	}

	assert.Equal(t, openapi.AccountSignInActive, val(byID[here.ID].SignIn))
	assert.Equal(t, expires, val(byID[here.ID].TokenExpiresAt))
	assert.Nil(t, byID[onNode.ID].SignIn, "an account signed in on a node has no sign-in to show")

	refused, err := pi.DisableAccountWithResponse(ctx, here.ID)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, refused.StatusCode(), "a coordinator changes no account")

	off, err := owner.DisableAccountWithResponse(ctx, here.ID)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, off.StatusCode(), string(off.Body))
	assert.Equal(t, openapi.AccountStatusDisabled, off.JSON200.Status)
	assert.Equal(t, openapi.AccountSignInActive, val(off.JSON200.SignIn), "its sign-in is kept")

	on, err := owner.EnableAccountWithResponse(ctx, here.ID)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, on.StatusCode(), string(on.Body))
	assert.Equal(t, openapi.AccountStatusActive, on.JSON200.Status)

	missing, err := owner.DisableAccountWithResponse(ctx, "acc_none")
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, missing.StatusCode())

	kept, err := owner.RemoveAccountWithResponse(ctx, onNode.ID)
	require.NoError(t, err)
	assert.Equal(t, http.StatusConflict, kept.StatusCode(), "its login is on a node, not here")

	notPi, err := pi.RemoveAccountWithResponse(ctx, here.ID)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, notPi.StatusCode())

	removed, err := owner.RemoveAccountWithResponse(ctx, here.ID)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, removed.StatusCode(), string(removed.Body))

	left, err := owner.ListAccountsWithResponse(ctx)
	require.NoError(t, err)
	require.Len(t, left.JSON200.Data, 1)
	assert.Equal(t, onNode.ID, left.JSON200.Data[0].Id)

	models, err := pi.ListGatewayModelsWithResponse(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, models.StatusCode(), string(models.Body))
	require.Len(t, models.JSON200.Data, 1)
	assert.Equal(t, []openapi.GatewayModelTarget{
		{Provider: "claude-subscription", UpstreamModel: "claude-sonnet-5-5"},
		{Provider: "openrouter", UpstreamModel: "deepseek-v4"},
	}, models.JSON200.Data[0].Targets, "the targets come in the order they are tried")
}
