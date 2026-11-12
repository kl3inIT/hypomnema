package clientapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
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
	// limits are the readings of the accounts that have one; refused is why another has none.
	limits  map[string]subscription.Limits
	refused map[string]error
	fresh   map[string]bool
}

func (s *signedIn) Limits(ctx context.Context, id string, fresh bool) (subscription.Limits, error) {
	accounts, err := s.svc.ListAccounts(ctx)
	if err != nil {
		return subscription.Limits{}, err
	}

	if !slices.ContainsFunc(accounts, func(a orchestration.Account) bool { return a.ID == id }) {
		return subscription.Limits{}, fmt.Errorf("%w: account %s", orchestration.ErrNotFound, id)
	}

	if s.fresh == nil {
		s.fresh = map[string]bool{}
	}

	s.fresh[id] = fresh

	if err := s.refused[id]; err != nil {
		return subscription.Limits{}, err
	}

	return s.limits[id], nil
}

func (s *signedIn) List(ctx context.Context) ([]subscription.Account, error) {
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

func (s *signedIn) Remove(_ context.Context, id string) error {
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
	w.srv.WithAccounts(&signedIn{
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

// Scenario: the Models page asks for an account's limits (issue #199). The
// owner gets each window with the part used and when it starts again; an
// account with nothing to show is answered without windows and with the
// reason, not with an error; a coordinator is refused.
func TestTheOwnerReadsAnAccountsLimits(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	codex, err := w.svc.AddAccount(ctx, "codex", "main")
	require.NoError(t, err)
	pasted, err := w.svc.AddAccount(ctx, "claude", "kept")
	require.NoError(t, err)
	google, err := w.svc.AddAccount(ctx, "antigravity", "trial")
	require.NoError(t, err)

	reset := time.Date(2026, 11, 12, 15, 30, 0, 0, time.UTC)
	read := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	fake := &signedIn{
		svc: w.svc, states: map[string]subscription.State{}, removed: map[string]bool{},
		limits: map[string]subscription.Limits{codex.ID: {
			ReadAt:  read,
			Windows: []subscription.Window{
				{Name: "5 hours", UsedPercent: 12, ResetsAt: reset},
				{Name: "week", UsedPercent: 45.5},
			},
		}},
		refused: map[string]error{
			pasted.ID: errors.New("the vendor refused the account's token for its limits (403)"),
			google.ID: subscription.ErrNoLimits,
		},
	}
	w.srv.WithAccounts(fake)

	owner, _ := w.token("web", bearertoken.Owner)
	pi, _ := w.token("pi", bearertoken.Coordinator)

	got, err := owner.GetAccountLimitsWithResponse(ctx, codex.ID, nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, got.StatusCode(), string(got.Body))
	require.Len(t, got.JSON200.Windows, 2)
	assert.Equal(t, "5 hours", got.JSON200.Windows[0].Name)
	assert.InDelta(t, 12, got.JSON200.Windows[0].UsedPercent, 0.001)
	assert.Equal(t, reset, val(got.JSON200.Windows[0].ResetsAt))
	assert.Nil(t, got.JSON200.Windows[1].ResetsAt, "the vendor did not say when the week starts again")
	assert.Equal(t, read, val(got.JSON200.ReadAt))
	assert.Nil(t, got.JSON200.Unavailable)
	assert.False(t, fake.fresh[codex.ID])

	fresh := true
	_, err = owner.GetAccountLimitsWithResponse(ctx, codex.ID, &openapi.GetAccountLimitsParams{Fresh: &fresh})
	require.NoError(t, err)
	assert.True(t, fake.fresh[codex.ID], "the refresh button asks the vendor now")

	failed, err := owner.GetAccountLimitsWithResponse(ctx, pasted.ID, nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, failed.StatusCode(), string(failed.Body))
	assert.Empty(t, failed.JSON200.Windows)
	assert.Equal(t, openapi.AccountLimitsUnavailableFailed, val(failed.JSON200.Unavailable))
	assert.Contains(t, val(failed.JSON200.Detail), "403")

	none, err := owner.GetAccountLimitsWithResponse(ctx, google.ID, nil)
	require.NoError(t, err)
	assert.Equal(t, openapi.AccountLimitsUnavailableNotGiven, val(none.JSON200.Unavailable))
	assert.Nil(t, none.JSON200.Detail)

	missing, err := owner.GetAccountLimitsWithResponse(ctx, "acc_none", nil)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, missing.StatusCode())

	refused, err := pi.GetAccountLimitsWithResponse(ctx, codex.ID, nil)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, refused.StatusCode())
}
