package controlplane

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

type fakeServed map[string][]string

func (f fakeServed) ServedNames(_ context.Context, runtime string) ([]string, bool) {
	names, ok := f[runtime]

	return names, ok
}

type fakeProviders struct {
	list  []orchestration.Provider
	asked map[string][]string
	fail  string
}

func (f *fakeProviders) ListProviders(context.Context) ([]orchestration.Provider, error) {
	return f.list, nil
}

func (f *fakeProviders) AddProviderModels(_ context.Context, name string, models []string) ([]string, error) {
	f.asked[name] = models
	if name == f.fail {
		return nil, errors.New("the store is busy")
	}

	return models[:1], nil
}

// Scenario (issue #623): once an hour each subscription provider is given what its vendor serves. Codex and Claude
// answer and their providers are given those names; Antigravity has no account logged in, so its provider is left
// as it is, and so is a provider that is no subscription. One that cannot be written is logged, and the next goes on.
func TestEachSubscriptionProviderIsGivenWhatItsVendorServes(t *testing.T) {
	providers := &fakeProviders{
		list: []orchestration.Provider{
			{Name: "codex-subscription", Kind: orchestration.ProviderCodexSubscription},
			{Name: "claude-subscription", Kind: orchestration.ProviderClaudeSubscription},
			{Name: "antigravity-subscription", Kind: orchestration.ProviderAntigravitySubscription},
			{Name: "router", Kind: orchestration.ProviderOpenAICompatible},
		},
		asked: map[string][]string{},
		fail:  "claude-subscription",
	}
	served := fakeServed{"codex": {"gpt-6.1-terra"}, "claude": {"claude-opus-5-5"}}

	var logs bytes.Buffer

	addVendorModels(t.Context(), served, providers, slog.New(slog.NewTextHandler(&logs, nil)))

	assert.Equal(t, map[string][]string{
		"codex-subscription": {"gpt-6.1-terra"}, "claude-subscription": {"claude-opus-5-5"},
	}, providers.asked)
	assert.Contains(t, logs.String(), "event=gateway.vendor_models_added provider=codex-subscription models=gpt-6.1-terra")
	assert.Contains(t, logs.String(), "event=gateway.vendor_models_failed provider=claude-subscription")
}
