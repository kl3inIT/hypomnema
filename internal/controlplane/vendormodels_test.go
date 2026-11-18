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

func (f *fakeProviders) SetProviderModels(_ context.Context, name string, models []string) ([]string, []string, error) {
	f.asked["set "+name] = models

	return models, []string{"gemini-old"}, nil
}

func (f *fakeProviders) AddProviderModels(_ context.Context, name string, models []string) ([]string, error) {
	f.asked[name] = models
	if name == f.fail {
		return nil, errors.New("the store is busy")
	}

	return models[:1], nil
}

// Scenario (issues #623, #639): once an hour each subscription provider is given what its vendor serves. Codex and
// Claude only gain the names they serve; Antigravity's provider is set to its roster, as omp takes it, and the
// models it no longer lists are logged as removed. A provider that is no subscription is left as it is. One that
// cannot be written is logged, and the next goes on.
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
	served := fakeServed{
		"codex": {"gpt-6.1-terra"}, "claude": {"claude-opus-5-5"}, "antigravity": {"gemini-3.8-flash-high"},
	}

	var logs bytes.Buffer

	addVendorModels(t.Context(), served, providers, slog.New(slog.NewTextHandler(&logs, nil)), nil)

	assert.Equal(t, map[string][]string{
		"codex-subscription": {"gpt-6.1-terra"}, "claude-subscription": {"claude-opus-5-5"},
		"set antigravity-subscription": {"gemini-3.8-flash-high"},
	}, providers.asked)
	assert.Contains(t, logs.String(), "provider=antigravity-subscription models=gemini-3.8-flash-high removed=gemini-old")
	assert.Contains(t, logs.String(), "event=gateway.vendor_models_added provider=codex-subscription models=gpt-6.1-terra")
	assert.Contains(t, logs.String(), "event=gateway.vendor_models_failed provider=claude-subscription")
}

// Scenario (review of #832, round 5): a Codex model the provider lists is left out of two answered readings in a
// row. After the first it stays, as one reading can miss it; after the second it is dropped, so that /v1/models
// lists no model every account's list leaves out (issue #814).
func TestACodexModelLeftOutTwiceIsDropped(t *testing.T) {
	providers := &fakeProviders{
		list: []orchestration.Provider{{
			Name: "codex-subscription", Kind: orchestration.ProviderCodexSubscription,
			Models: []string{"gpt-6.1-terra", "gpt-gone"},
		}},
		asked: map[string][]string{},
	}
	v := &vendorModels{
		subs: fakeServed{"codex": {"gpt-6.1-terra", "gpt-6.2-sol"}}, providers: providers,
		logger: slog.New(slog.DiscardHandler),
	}

	v.update(t.Context())
	assert.Equal(t, []string{"gpt-6.1-terra", "gpt-6.2-sol", "gpt-gone"}, providers.asked["set codex-subscription"])

	v.update(t.Context())
	assert.Equal(t, []string{"gpt-6.1-terra", "gpt-6.2-sol"}, providers.asked["set codex-subscription"])
}
