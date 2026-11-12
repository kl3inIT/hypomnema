package clientapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
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
	// usedResets are the requests that used a reset.
	usedResets map[string]bool
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

// Begin starts a sign-in the way the two kinds do: claude asks for the
// address the browser ends on, codex shows a code.
func (s *signedIn) Begin(_ context.Context, runtime string) (*subscription.Attempt, error) {
	switch runtime {
	case "claude":
		return &subscription.Attempt{
			Runtime: runtime, URL: "https://claude.example/authorize?state=abc", Paste: true, ExpiresIn: 10 * time.Minute,
		}, nil
	case "codex":
		return &subscription.Attempt{
			Runtime: runtime, URL: "https://chatgpt.example/device", UserCode: "ABCD-1234", ExpiresIn: 15 * time.Minute,
		}, nil
	default:
		return nil, fmt.Errorf("%w: %q", subscription.ErrUnknownVendor, runtime)
	}
}

// Complete signs the account in when the pasted address carries a code, as a
// vendor accepts one; a sign-in with a code needs nothing pasted.
func (s *signedIn) Complete(ctx context.Context, a *subscription.Attempt, pasted, label string,
) (subscription.Account, error) {
	if a.Paste && !strings.Contains(pasted, "code=") {
		return subscription.Account{}, fmt.Errorf("%w: the address has no code", subscription.ErrLoginFailed)
	}

	if label == "" {
		label = "owner@example.com"
	}

	made, err := s.svc.AddAccount(ctx, a.Runtime, label)
	if err != nil {
		return subscription.Account{}, err
	}

	s.states[made.ID] = subscription.StateActive

	return subscription.Account{Account: made, State: subscription.StateActive}, nil
}

// UseReset uses one of the account's resets as a vendor does: the same
// request sent again uses no second one, an account with none is refused,
// and the windows start again.
func (s *signedIn) UseReset(_ context.Context, id, requestID string) (subscription.Limits, error) {
	if strings.ContainsAny(requestID, " !") {
		return subscription.Limits{}, subscription.ErrBadResetRequest
	}

	limits, ok := s.limits[id]
	if !ok {
		return subscription.Limits{}, fmt.Errorf("%w: account %s", orchestration.ErrNotFound, id)
	}

	if s.usedResets == nil {
		s.usedResets = map[string]bool{}
	}

	if !s.usedResets[requestID] {
		if limits.Resets.Available == 0 {
			return subscription.Limits{}, subscription.ErrNoResets
		}

		s.usedResets[requestID] = true
		limits.Resets.Available--
	}

	for i := range limits.Windows {
		limits.Windows[i].UsedPercent = 0
	}

	s.limits[id] = limits

	return limits, nil
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
			ReadAt: read,
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

// Scenario: the owner adds accounts from the Models page (issue #199, step
// 2). A Claude sign-in gives a link and is completed with the address the
// browser ended on; a Codex sign-in gives a link and a code and is completed
// by waiting. A sign-in is completed once, a wrong address is refused and the
// sign-in is begun again, and a coordinator starts none.
func TestTheOwnerSignsAccountsInFromTheModelsPage(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	fake := &signedIn{svc: w.svc, states: map[string]subscription.State{}, removed: map[string]bool{}}
	w.srv.WithAccounts(fake).WithSecrets(noSecrets{})

	owner, _ := w.token("web", bearertoken.Owner)
	pi, _ := w.token("pi", bearertoken.Coordinator)

	refused, err := pi.BeginAccountSignInWithResponse(ctx, openapi.NewAccountSignIn{Runtime: openapi.Claude})
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, refused.StatusCode())

	label := "work"
	claude := openapi.NewAccountSignIn{Runtime: openapi.Claude, Label: &label}

	begun, err := owner.BeginAccountSignInWithResponse(ctx, claude)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, begun.StatusCode(), string(begun.Body))
	assert.True(t, begun.JSON200.Paste)
	assert.Nil(t, begun.JSON200.UserCode)
	assert.Contains(t, begun.JSON200.Url, "authorize")
	assert.Equal(t, 600, begun.JSON200.ExpiresInSeconds)

	wrong := "http://localhost:54545/callback?error=denied"
	bad, err := owner.CompleteAccountSignInWithResponse(ctx, begun.JSON200.Id,
		openapi.AccountSignInCallback{Callback: &wrong})
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, bad.StatusCode(), string(bad.Body))

	right := "http://localhost:54545/callback?code=xyz&state=abc"
	again, err := owner.CompleteAccountSignInWithResponse(ctx, begun.JSON200.Id,
		openapi.AccountSignInCallback{Callback: &right})
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, again.StatusCode(), "a sign-in is completed once; one that failed is begun again")

	begun, err = owner.BeginAccountSignInWithResponse(ctx, claude)
	require.NoError(t, err)

	empty, err := owner.CompleteAccountSignInWithResponse(ctx, begun.JSON200.Id, openapi.AccountSignInCallback{})
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, empty.StatusCode(), "this kind needs the address")

	begun, err = owner.BeginAccountSignInWithResponse(ctx, claude)
	require.NoError(t, err)

	done, err := owner.CompleteAccountSignInWithResponse(ctx, begun.JSON200.Id,
		openapi.AccountSignInCallback{Callback: &right})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, done.StatusCode(), string(done.Body))
	assert.Equal(t, "work", done.JSON200.Label)
	assert.Equal(t, "claude", done.JSON200.Runtime)
	assert.Equal(t, openapi.AccountSignInActive, val(done.JSON200.SignIn))

	coded, err := owner.BeginAccountSignInWithResponse(ctx, openapi.NewAccountSignIn{Runtime: openapi.Codex})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, coded.StatusCode(), string(coded.Body))
	assert.False(t, coded.JSON200.Paste)
	assert.Equal(t, "ABCD-1234", val(coded.JSON200.UserCode))

	waited, err := owner.CompleteAccountSignInWithResponse(ctx, coded.JSON200.Id, openapi.AccountSignInCallback{})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, waited.StatusCode(), string(waited.Body))
	assert.Equal(t, "owner@example.com", waited.JSON200.Label, "an account with no name given takes its e-mail")

	listed, err := owner.ListAccountsWithResponse(ctx)
	require.NoError(t, err)
	assert.Len(t, listed.JSON200.Data, 2)
}

// Scenario: the owner changes which account the gateway tries first. An
// account moves one place among the accounts of its own runtime, an account
// at the end stays, and a coordinator moves none.
func TestTheOwnerChangesTheOrderOfAccounts(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	first, err := w.svc.AddAccount(ctx, "codex", "first")
	require.NoError(t, err)
	second, err := w.svc.AddAccount(ctx, "codex", "second")
	require.NoError(t, err)
	third, err := w.svc.AddAccount(ctx, "codex", "third")
	require.NoError(t, err)
	other, err := w.svc.AddAccount(ctx, "claude", "other")
	require.NoError(t, err)

	owner, _ := w.token("web", bearertoken.Owner)
	pi, _ := w.token("pi", bearertoken.Coordinator)

	order := func() []string {
		listed, err := owner.ListAccountsWithResponse(ctx)
		require.NoError(t, err)

		var ids []string

		for _, a := range listed.JSON200.Data {
			if a.Runtime == "codex" {
				ids = append(ids, a.Id)
			}
		}

		return ids
	}

	up := openapi.AccountMove{Direction: openapi.Up}
	down := openapi.AccountMove{Direction: openapi.Down}

	refused, err := pi.MoveAccountWithResponse(ctx, third.ID, up)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, refused.StatusCode())

	moved, err := owner.MoveAccountWithResponse(ctx, third.ID, up)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, moved.StatusCode(), string(moved.Body))
	assert.Equal(t, 2, moved.JSON200.Position)
	assert.Equal(t, []string{first.ID, third.ID, second.ID}, order())

	_, err = owner.MoveAccountWithResponse(ctx, third.ID, up)
	require.NoError(t, err)
	stays, err := owner.MoveAccountWithResponse(ctx, third.ID, up)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, stays.StatusCode(), "the first account has nowhere to go up to")
	assert.Equal(t, []string{third.ID, first.ID, second.ID}, order())

	_, err = owner.MoveAccountWithResponse(ctx, third.ID, down)
	require.NoError(t, err)
	assert.Equal(t, []string{first.ID, third.ID, second.ID}, order())

	alone, err := owner.MoveAccountWithResponse(ctx, other.ID, down)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, alone.StatusCode())
	assert.Equal(t, []string{first.ID, third.ID, second.ID}, order(), "another runtime's account moves among its own")

	missing, err := owner.MoveAccountWithResponse(ctx, "acc_none", up)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, missing.StatusCode())
}

// noSecrets stands for a vault that is set up: a sign-in is begun only then,
// since its tokens are kept there.
type noSecrets struct{}

func (noSecrets) List(context.Context) ([]SecretInfo, error) { return nil, nil }

func (noSecrets) Value(context.Context, string) (map[string]string, error) {
	return map[string]string{}, nil
}

func (noSecrets) Put(context.Context, string, map[string]string) (SecretInfo, error) {
	return SecretInfo{}, nil
}

func (noSecrets) Delete(context.Context, string) error { return nil }

// Scenario: an account is out of its 5 hours and has limit resets (issue
// #199, decision 17). The page shows how many are left; the owner uses one
// and gets the limits as they are after it, with one reset fewer. The same
// request sent again uses no second reset. An account with none left is
// refused with the reason, and a coordinator uses none.
func TestTheOwnerUsesAnAccountsLimitReset(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	codex, err := w.svc.AddAccount(ctx, "codex", "main")
	require.NoError(t, err)
	spent, err := w.svc.AddAccount(ctx, "claude", "spent")
	require.NoError(t, err)

	expires := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	fake := &signedIn{
		svc: w.svc, states: map[string]subscription.State{}, removed: map[string]bool{},
		limits: map[string]subscription.Limits{
			codex.ID: {
				Windows: []subscription.Window{{Name: "5 hours", UsedPercent: 100}},
				Resets:  subscription.Resets{Available: 2, ExpiresAt: expires},
			},
			spent.ID: {Windows: []subscription.Window{{Name: "5 hours", UsedPercent: 100}}},
		},
	}
	w.srv.WithAccounts(fake)

	owner, _ := w.token("web", bearertoken.Owner)
	pi, _ := w.token("pi", bearertoken.Coordinator)

	read, err := owner.GetAccountLimitsWithResponse(ctx, codex.ID, nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, read.StatusCode(), string(read.Body))
	require.NotNil(t, read.JSON200.Resets)
	assert.Equal(t, 2, read.JSON200.Resets.Available)
	assert.Equal(t, expires, val(read.JSON200.Resets.ExpiresAt))

	none, err := owner.GetAccountLimitsWithResponse(ctx, spent.ID, nil)
	require.NoError(t, err)
	assert.Nil(t, none.JSON200.Resets, "an account with no reset says nothing of resets")

	refused, err := pi.RedeemAccountLimitResetWithResponse(ctx, codex.ID, openapi.AccountLimitReset{RequestId: "req-1"})
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, refused.StatusCode())

	used, err := owner.RedeemAccountLimitResetWithResponse(ctx, codex.ID, openapi.AccountLimitReset{RequestId: "req-1"})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, used.StatusCode(), string(used.Body))
	assert.InDelta(t, 0, used.JSON200.Windows[0].UsedPercent, 0.001, "the limits come back as they are after the reset")
	assert.Equal(t, 1, used.JSON200.Resets.Available)

	again, err := owner.RedeemAccountLimitResetWithResponse(ctx, codex.ID, openapi.AccountLimitReset{RequestId: "req-1"})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, again.StatusCode(), string(again.Body))
	assert.Equal(t, 1, again.JSON200.Resets.Available, "the same request uses no second reset")

	empty, err := owner.RedeemAccountLimitResetWithResponse(ctx, spent.ID, openapi.AccountLimitReset{RequestId: "req-2"})
	require.NoError(t, err)
	assert.Equal(t, http.StatusConflict, empty.StatusCode())
	assert.Contains(t, string(empty.Body), "no limit reset to use")

	bad, err := owner.RedeemAccountLimitResetWithResponse(ctx, codex.ID,
		openapi.AccountLimitReset{RequestId: "not an id!"})
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, bad.StatusCode())

	missing, err := owner.RedeemAccountLimitResetWithResponse(ctx, "acc_none",
		openapi.AccountLimitReset{RequestId: "req-3"})
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, missing.StatusCode())
}
