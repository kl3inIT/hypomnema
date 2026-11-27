package subscription

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/profilestore"
)

type fakeServed map[string][]string

// readAt is when every list of a fakeServed was read.
var readAt = time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)

func (f fakeServed) ServedList(_ context.Context, runtime string) ([]string, time.Time, bool) {
	names, ok := f[runtime]

	return names, readAt, ok
}

// readings serves one list, read anew at each step.
type readings struct {
	names []string
	read  time.Time
}

func (r *readings) ServedList(_ context.Context, runtime string) ([]string, time.Time, bool) {
	return r.names, r.read, runtime == "codex"
}

type fakeProviders struct {
	list  []profilestore.Provider
	asked map[string][]string
	fail  string
	// pinned are models a profile uses, which are not removed; removed are the provider and model of each removal.
	pinned  []string
	removed []string
}

func (f *fakeProviders) ListProviders(context.Context) ([]profilestore.Provider, error) {
	return f.list, nil
}

func (f *fakeProviders) SetProviderModels(_ context.Context, name string, models []string) ([]string, []string, error) {
	f.asked["set "+name] = models

	return models, []string{"gemini-old"}, nil
}

func (f *fakeProviders) RemoveProviderModel(_ context.Context, name, model string) (bool, error) {
	if slices.Contains(f.pinned, model) {
		return false, errors.New("a profile uses " + model)
	}

	f.removed = append(f.removed, name+" "+model)

	return true, nil
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
		list: []profilestore.Provider{
			{Name: "codex-subscription", Kind: profilestore.ProviderCodexSubscription},
			{Name: "claude-subscription", Kind: profilestore.ProviderClaudeSubscription},
			{Name: "antigravity-subscription", Kind: profilestore.ProviderAntigravitySubscription},
			{Name: "router", Kind: profilestore.ProviderOpenAICompatible},
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

// Scenario (review of #832): a Codex model the provider lists is left out of two readings in a row. After the first
// it stays, as one reading can miss it; after the second it is dropped, so that /v1/models lists no model every
// account's list leaves out (issue #814). A reading is counted once however often it is seen, and a model a profile
// uses keeps only itself.
func TestACodexModelLeftOutTwiceIsDropped(t *testing.T) {
	providers := &fakeProviders{
		list: []profilestore.Provider{{
			Name: "codex-subscription", Kind: profilestore.ProviderCodexSubscription,
			Models: []string{"gpt-6.1-terra", "gpt-gone", "gpt-pinned"},
		}},
		asked:  map[string][]string{},
		pinned: []string{"gpt-pinned"},
	}
	served := &readings{names: []string{"gpt-6.1-terra"}, read: readAt}
	v := NewVendorModels(served, providers, slog.New(slog.DiscardHandler))

	v.Update(t.Context())
	v.Update(t.Context()) // the same reading seen again
	assert.Empty(t, providers.removed, "one reading so far")

	served.read = readAt.Add(time.Hour)
	v.Update(t.Context())
	assert.Equal(t, []string{"codex-subscription gpt-gone"}, providers.removed, "the pinned model keeps only itself")
}
