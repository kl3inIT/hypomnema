package subscription

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// The subscription providers' models: each is given the models its vendor serves (issues #623, #639, #814, #854,
// #965), once soon after the start, then every hour and when an account's first list of models is read.

// providerRuntimes is the runtime of the accounts of each kind of subscription provider.
var providerRuntimes = map[string]string{
	orchestration.ProviderCodexSubscription:       "codex",
	orchestration.ProviderClaudeSubscription:      "claude",
	orchestration.ProviderAntigravitySubscription: "antigravity",
	orchestration.ProviderOpenCodeGo:              orchestration.OpenCodeGoRuntime,
	orchestration.ProviderOpenCodeZen:             orchestration.OpenCodeZenRuntime,
}

// ProviderRuntime is the runtime of the accounts of a subscription provider of kind; ok is false for a kind that is
// no subscription.
func ProviderRuntime(kind string) (runtime string, ok bool) {
	runtime, ok = providerRuntimes[kind]

	return runtime, ok
}

// vendorModelsEvery is how often the subscription providers are given the models their vendors serve, and
// vendorModelsFirst how long after the start the first time is: the accounts' tokens are made fresh first. The
// vendor is asked at most once an hour anyway (ServedNames).
const (
	vendorModelsEvery = time.Hour
	vendorModelsFirst = 2 * time.Minute
)

// servedNames is what a runtime's accounts are served (Service.ServedNames).
type servedNames interface {
	// ServedList is what they are served, with when the latest list it was made from was read.
	ServedList(ctx context.Context, runtime string) ([]string, time.Time, bool)
}

// providerModels lists the providers and adds models to one (orchestration.Service).
type providerModels interface {
	ListProviders(ctx context.Context) ([]orchestration.Provider, error)
	AddProviderModels(ctx context.Context, name string, models []string) ([]string, error)
	SetProviderModels(ctx context.Context, name string, models []string) (added, removed []string, err error)
	RemoveProviderModel(ctx context.Context, name, model string) (bool, error)
}

// rosterRuntimes are the runtimes whose vendor's list is the provider's list, as omp takes Antigravity's roster as
// authoritative (packages/catalog/src/discovery/antigravity.ts; issue #639), and as OpenClaw, Hermes and pi take
// OpenCode Go's live /v1/models (issue #854). The others only gain models: Anthropic's list does not name every model
// it serves (OpenClaw, Hermes), and Codex's and OpenCode Zen's are read per account (droppingRuntimes).
var rosterRuntimes = map[string]bool{"antigravity": true, orchestration.OpenCodeGoRuntime: true}

// VendorModels gives the subscription providers the models their vendors serve, one update at a time: every hour
// (Loop) and when an account's first list of models is read (issue #814).
type VendorModels struct {
	mu        sync.Mutex
	subs      servedNames
	providers providerModels
	logger    *slog.Logger
	drops     *drops
}

// NewVendorModels gives the providers what subs' accounts are served (Service.ServedList); providers is
// orchestration.Service.
func NewVendorModels(subs servedNames, providers providerModels, logger *slog.Logger) *VendorModels {
	return &VendorModels{subs: subs, providers: providers, logger: logger}
}

// Update gives each subscription provider what its vendor serves now.
func (v *VendorModels) Update(ctx context.Context) {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.drops == nil {
		v.drops = &drops{misses: map[string]map[string]int{}, reads: map[string]time.Time{}}
	}

	addVendorModels(ctx, v.subs, v.providers, v.logger, v.drops)
}

// dropAfter is how many readings in a row must leave a model out before a Codex provider drops it (review of #832):
// one is not enough, as an account can be read without it once.
const dropAfter = 2

// droppingRuntimes are the runtimes whose providers drop, after dropAfter readings, a model their vendor's lists
// leave out: Codex, whose requests go only to the accounts whose list has the model (issue #814), so a model no list
// names would be listed by /v1/models and refused, and OpenCode Zen, whose keys are routed the same way by their
// workspace's Console list (issue #965). Each model is dropped on its own, so one a Bot or profile still uses keeps
// only itself and the new ones are added all the same (review of PR #1000). Claude's list does not name every model
// it serves.
var droppingRuntimes = map[string]bool{"codex": true, orchestration.OpenCodeZenRuntime: true}

// drops counts, by provider and model, the readings in a row that left a model out; a reading is counted once, by
// when it was read, however many updates see it.
type drops struct {
	misses map[string]map[string]int
	reads  map[string]time.Time
}

// due are the models a provider lists that dropAfter readings in a row, the one read at read the last, left out.
func (d *drops) due(provider string, listed, served []string, read time.Time) []string {
	if !read.After(d.reads[provider]) {
		return nil
	}

	d.reads[provider] = read

	misses := d.misses[provider]
	if misses == nil {
		misses = map[string]int{}
		d.misses[provider] = misses
	}

	for name := range misses {
		if slices.Contains(served, name) || !slices.Contains(listed, name) {
			delete(misses, name)
		}
	}

	var out []string

	for _, name := range listed {
		if slices.Contains(served, name) {
			continue
		}

		if misses[name]++; misses[name] >= dropAfter {
			out = append(out, name)
		}
	}

	return out
}

// drop takes each due model off a provider's list on its own, so that one a profile uses, which is refused, keeps
// none of the others.
func drop(
	ctx context.Context, providers providerModels, logger *slog.Logger, provider string, due []string,
) (removed []string) {
	for _, name := range due {
		ok, err := providers.RemoveProviderModel(ctx, provider, name)
		if err != nil {
			logger.WarnContext(ctx, "a model its vendor no longer serves was kept",
				slog.String("event", "gateway.vendor_models_kept"), slog.String("provider", provider),
				slog.String("model", name), slog.String("error", err.Error()))

			continue
		}

		if ok {
			removed = append(removed, name)
		}
	}

	return removed
}

// Loop gives each subscription provider the models its vendor serves (issue #623), once soon after the start and
// then every hour, until ctx ends.
func (v *VendorModels) Loop(ctx context.Context) {
	timer := time.NewTimer(vendorModelsFirst)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}

		v.Update(ctx)
		timer.Reset(vendorModelsEvery)
	}
}

// addVendorModels adds to each subscription provider the models its vendor serves that it does not list, as Codex,
// OpenClaw, omp and Hermes take a subscription's models from its vendor. A vendor that does not answer, or a runtime
// with no account logged in, leaves the provider as it is: its list is the fallback.
func addVendorModels(ctx context.Context, subs servedNames, providers providerModels, logger *slog.Logger, d *drops) {
	list, err := providers.ListProviders(ctx)
	if err != nil {
		logger.WarnContext(ctx, "the providers could not be read for their vendors' models",
			slog.String("event", "gateway.vendor_models_failed"), slog.String("error", err.Error()))

		return
	}

	for _, p := range list {
		runtime, ok := providerRuntimes[p.Kind]
		if !ok {
			continue
		}

		names, read, ok := subs.ServedList(ctx, runtime)
		if !ok {
			continue
		}

		var (
			added, removed []string
			err            error
		)

		switch {
		case rosterRuntimes[runtime]:
			added, removed, err = providers.SetProviderModels(ctx, p.Name, names)
		case droppingRuntimes[runtime] && d != nil:
			added, err = providers.AddProviderModels(ctx, p.Name, names)
			if err == nil {
				removed = drop(ctx, providers, logger, p.Name, d.due(p.Name, p.Models, names, read))
			}
		default:
			added, err = providers.AddProviderModels(ctx, p.Name, names)
		}

		if err != nil {
			logger.WarnContext(ctx, "a provider was not given its vendor's models",
				slog.String("event", "gateway.vendor_models_failed"), slog.String("provider", p.Name),
				slog.String("error", err.Error()))

			continue
		}

		if len(added) > 0 || len(removed) > 0 {
			logger.InfoContext(ctx, "a provider was given the models its vendor serves",
				slog.String("event", "gateway.vendor_models_added"), slog.String("provider", p.Name),
				slog.String("models", strings.Join(added, ",")), slog.String("removed", strings.Join(removed, ",")))
		}
	}
}
