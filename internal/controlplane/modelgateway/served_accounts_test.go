package modelgateway_test

import (
	"context"
	"net/http"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
)

// rosterGoogle are Antigravity accounts that say which models each is served (modelgateway.ModelTokens).
type rosterGoogle struct {
	googleAccount

	served map[string][]string
	asked  []string
}

func (a *rosterGoogle) TokenFor(
	ctx context.Context, runtime, model, session string, skip ...string,
) (modelgateway.AccountToken, error) {
	a.mu.Lock()
	a.asked = append(a.asked, model)

	var others []string

	for _, id := range a.ids {
		if !slices.Contains(a.served[id], model) {
			others = append(others, id)
		}
	}
	a.mu.Unlock()

	return a.Token(ctx, runtime, session, append(skip, others...)...)
}

// Scenario (issue #814): the first Antigravity account is not served the model asked for, the second is. The chat
// is served under the second, asked for by the model.
func TestAChatGoesToAnAntigravityAccountServedItsModel(t *testing.T) {
	t.Parallel()

	backend, got := cloudCode(t, stream(geminiEvents...))
	account := &rosterGoogle{
		googleAccount: googleAccount{accounts: accounts{ids: []string{"acc_pro", "acc_ultra"}}},
		served:        map[string][]string{"acc_ultra": {"gemini-3.8-flash-high"}},
	}
	g := newAntigravityGateway(t, backend, account)

	status, _, body := g.call(t, g.key, askFlash+`,"stream":true}`)
	require.Equal(t, http.StatusOK, status, body)

	sentHeaders, _ := got.last(t)
	assert.Equal(t, "Bearer acc_ultra-access-0", sentHeaders.Get("Authorization"))

	account.mu.Lock()
	defer account.mu.Unlock()

	assert.Equal(t, []string{"gemini-3.8-flash-high"}, account.asked)
}
