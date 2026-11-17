package clientapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/muse"
)

// pool stands for the Muse pool.
type pool struct {
	accounts []muse.Account
	tools    bool
	started  int
	label    string
}

func (p *pool) Accounts(context.Context) ([]muse.Account, error) { return p.accounts, nil }
func (p *pool) ToolsOn(context.Context) (bool, error)            { return p.tools, nil }

func (p *pool) SetToolsOn(_ context.Context, on bool) error {
	p.tools = on

	return nil
}

func (p *pool) SetOn(_ context.Context, id string, on bool) error {
	for i := range p.accounts {
		if p.accounts[i].ID == id {
			p.accounts[i].On = on
		}
	}

	return nil
}

func (p *pool) Move(context.Context, string, bool) error {
	p.accounts[0], p.accounts[1] = p.accounts[1], p.accounts[0]

	return nil
}

func (p *pool) Remove(context.Context, string) error {
	p.accounts = p.accounts[1:]

	return nil
}

func (p *pool) SignInStart(context.Context) error {
	p.started++

	return nil
}

func (p *pool) SignInCheck(_ context.Context, _, label string) (muse.SignIn, error) {
	if label == "" {
		return muse.SignIn{}, muse.ErrInvalid
	}

	p.label = label
	a := muse.Account{ID: "acc_3", Label: label, State: muse.StateSignedIn, Usage: "unknown", On: true}
	p.accounts = append(p.accounts, a)

	return muse.SignIn{Done: true, Account: &a}, nil
}

// Scenario: the owner opens Settings → Gateway → Muse, adds an account by
// signing in, reorders, switches and removes; a coordinator sees nothing.
func TestTheOwnerKeepsTheMusePool(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()
	p := &pool{tools: true, accounts: []muse.Account{
		{ID: "acc_1", Label: "one", On: true, State: muse.StateSignedIn, Usage: "62% used · resets Sep 30"},
		{ID: "acc_2", Label: "two", On: true, State: muse.StateRefused, Cause: "403", Resting: true, Usage: "unknown"},
	}}
	w.srv.WithMuse(p)

	owner, _ := w.token("web", bearertoken.Owner)
	pi, _ := w.token("pi", bearertoken.Coordinator)

	refused, err := pi.GetMuseWithResponse(ctx)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, refused.StatusCode())

	got, err := owner.GetMuseWithResponse(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, got.StatusCode(), string(got.Body))
	assert.True(t, got.JSON200.ToolsOn)
	assert.Equal(t, "62% used · resets Sep 30", got.JSON200.Accounts[0].Usage)
	assert.Equal(t, "refused", got.JSON200.Accounts[1].State)
	assert.Equal(t, "403", *got.JSON200.Accounts[1].Cause)

	started, err := owner.StartMuseSignInWithResponse(ctx)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNoContent, started.StatusCode())
	assert.Equal(t, 1, p.started)

	bad, err := owner.CheckMuseSignInWithResponse(ctx, openapi.MuseSignInCheck{})
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, bad.StatusCode())

	label := "research"
	in, err := owner.CheckMuseSignInWithResponse(ctx, openapi.MuseSignInCheck{Label: &label})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, in.StatusCode(), string(in.Body))
	assert.True(t, in.JSON200.Done)
	assert.Equal(t, "research", in.JSON200.Account.Label)

	moved, err := owner.MoveMuseAccountWithResponse(ctx, "acc_2", openapi.AccountMove{Direction: openapi.Up})
	require.NoError(t, err)
	assert.Equal(t, "acc_2", moved.JSON200.Accounts[0].Id)

	off, err := owner.SetMuseAccountWithResponse(ctx, "acc_1", openapi.MuseSwitch{On: false})
	require.NoError(t, err)
	assert.False(t, off.JSON200.Accounts[1].On)

	tools, err := owner.SetMuseToolsWithResponse(ctx, openapi.MuseSwitch{On: false})
	require.NoError(t, err)
	assert.False(t, tools.JSON200.ToolsOn)

	removed, err := owner.RemoveMuseAccountWithResponse(ctx, "acc_2")
	require.NoError(t, err)
	assert.Len(t, removed.JSON200.Accounts, 2)
}

// A control plane with no Muse says so.
func TestNoMuseIsUnavailable(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	owner, _ := w.token("web", bearertoken.Owner)

	got, err := owner.GetMuseWithResponse(t.Context())
	require.NoError(t, err)
	assert.Equal(t, http.StatusServiceUnavailable, got.StatusCode(), string(got.Body))
}
