package clientcli

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/muse"
)

// thePool stands for the Muse pool.
type thePool struct {
	tools bool
	on    map[string]bool
}

func (p *thePool) Accounts(context.Context) ([]muse.Account, error) {
	return []muse.Account{
		{ID: "acc_1", Label: "one", On: p.on["acc_1"], State: muse.StateSignedIn, Usage: "62% used · resets Sep 30"},
		{ID: "acc_2", Label: "two", On: true, State: muse.StateRefused, Cause: "403", Resting: true, Usage: "unknown"},
	}, nil
}

func (p *thePool) ToolsOn(context.Context) (bool, error) { return p.tools, nil }

func (p *thePool) SetToolsOn(_ context.Context, on bool) error {
	p.tools = on

	return nil
}

func (p *thePool) SetOn(_ context.Context, id string, on bool) error {
	p.on[id] = on

	return nil
}

func (p *thePool) Move(context.Context, string, bool) error { return nil }
func (p *thePool) Remove(context.Context, string) error     { return nil }
func (p *thePool) SignInStart(context.Context) error        { return nil }

func (p *thePool) SignInCheck(context.Context, string, string) (muse.SignIn, error) {
	return muse.SignIn{}, nil
}

// The owner reads the Muse pool and switches its tools and an account.
func TestTheOwnerReadsAndSwitchesTheMusePool(t *testing.T) {
	t.Parallel()

	p := &thePool{tools: true, on: map[string]bool{"acc_1": true}}
	w := newWorldWith(t, nil, func(s *clientapi.Server) { s.WithMuse(p) })
	w.login(t, bearertoken.Owner)

	out, err := w.cmd(t, "muse", "accounts")
	require.NoError(t, err)
	assert.Equal(t, "tools on\n1\tacc_1\tone\ton\tsigned in\t62% used · resets Sep 30\n"+
		"2\tacc_2\ttwo\ton\trefused, resting (403)\tunknown\n", out)

	out, err = w.cmd(t, "muse", "tools", "off")
	require.NoError(t, err)
	assert.Equal(t, "the Muse tools are off\n", out)
	assert.False(t, p.tools)

	_, err = w.cmd(t, "muse", "account", "set", "acc_1", "off")
	require.NoError(t, err)
	assert.False(t, p.on["acc_1"])

	_, err = w.cmd(t, "muse", "tools", "maybe")
	require.Error(t, err)
}
