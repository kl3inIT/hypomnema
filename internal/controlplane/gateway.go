package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/subscription"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
)

// The model gateway's commands (issue #20): the model table, the keys and
// the usage. The web client has screens for them; these are commands of the
// owner on the server, as nodes and tokens are, which open the database
// directly: they work from a shell or a script there, without the client API
// or a login.

const gatewayUsage = "gateway models apply FILE | gateway models list | gateway models rm <name> | " +
	"gateway keys add <name> [--models a,b] [--endpoints search,fetch,images,audio|none] | gateway keys list | gateway keys revoke <key id> | " +
	"gateway antigravity-rewrites list | gateway antigravity-rewrites set FILE | " +
	"gateway usage [--limit N] | gateway usage --by provider|account [--period 24h|7d|30d|90d|12m]"

// gatewaySecrets gives the gateway a provider's key from the vault. A
// provider served by the gateway keeps its key there and names it as
// vault:<name>; an environment variable or a file is for a CLI on its own
// machine, not for the control plane.
func gatewaySecrets(v *vault.Service) modelgateway.Secrets {
	return func(ctx context.Context, ref string) (string, error) {
		name, ok := vault.NameFromRef(ref)
		if !ok {
			return "", fmt.Errorf("%w: api_key_ref %q is not vault:<name>", modelgateway.ErrInvalid, ref)
		}

		opened, err := v.Open(ctx, name)
		if err != nil {
			return "", err
		}

		return opened.Payload[vault.KeyValue], nil
	}
}

// gatewayAccounts gives the gateway's built-in providers the accounts logged
// in on the control plane.
type gatewayAccounts struct{ subs *subscription.Service }

func (g gatewayAccounts) Token(
	ctx context.Context, runtime, session string, skip ...string,
) (modelgateway.AccountToken, error) {
	return accountToken(g.subs.Token(ctx, runtime, session, skip...))
}

// TokenFor is Token for a request for a model: only the accounts whose vendor lists it, when one does (issue #814).
func (g gatewayAccounts) TokenFor(
	ctx context.Context, runtime, model, session string, skip ...string,
) (modelgateway.AccountToken, error) {
	return accountToken(g.subs.TokenFor(ctx, runtime, model, session, skip...))
}

func (g gatewayAccounts) Quota(ctx context.Context, accountID string) (map[string]float64, error) {
	return g.subs.Quota(ctx, accountID)
}

// UsedUpUntil says until when a window of a model's Antigravity group is used up (issue #576).
func (g gatewayAccounts) UsedUpUntil(ctx context.Context, accountID, model string) time.Time {
	return g.subs.UsedUpUntil(ctx, accountID, model)
}

func (g gatewayAccounts) NoteQuota(accountID string, left float64) {
	g.subs.NoteQuota(accountID, left)
}

func (g gatewayAccounts) NoteAnswer(runtime, accountID string, h http.Header) {
	g.subs.NoteAnswer(runtime, accountID, h)
}

// TokenAfterRefusalFor is TokenAfterRefusal for a request for a model (issue #814).
func (g gatewayAccounts) TokenAfterRefusalFor(
	ctx context.Context, refused modelgateway.AccountToken, model string,
) (modelgateway.AccountToken, error) {
	return accountToken(g.subs.TokenAfterRefusalFor(ctx, subscription.Token{
		AccountID: refused.AccountID, ExternalID: refused.ExternalID, Version: refused.Version,
	}, model))
}

func (g gatewayAccounts) TokenAfterRefusal(
	ctx context.Context, refused modelgateway.AccountToken,
) (modelgateway.AccountToken, error) {
	return accountToken(g.subs.TokenAfterRefusal(ctx, subscription.Token{
		AccountID: refused.AccountID, ExternalID: refused.ExternalID, Version: refused.Version,
	}))
}

func (g gatewayAccounts) CoolDown(
	ctx context.Context, runtime, accountID string, until time.Time, reason string,
) error {
	return g.subs.CoolDown(ctx, runtime, accountID, until, reason)
}

// accountToken says a subscription's answer in the gateway's words.
func accountToken(t subscription.Token, err error) (modelgateway.AccountToken, error) {
	var cooling *subscription.CoolingError

	switch {
	case errors.As(err, &cooling):
		return modelgateway.AccountToken{}, &modelgateway.CoolingError{Until: cooling.Until}
	case errors.Is(err, subscription.ErrNoAccount):
		return modelgateway.AccountToken{}, fmt.Errorf("%w: %w", modelgateway.ErrNoAccount, err)
	case errors.Is(err, subscription.ErrModelNotServed):
		return modelgateway.AccountToken{}, fmt.Errorf("%w: %w", modelgateway.ErrModelNotServed, err)
	case errors.Is(err, subscription.ErrNoModel):
		return modelgateway.AccountToken{}, fmt.Errorf("%w: %w", modelgateway.ErrNoModel, err)
	case err != nil:
		return modelgateway.AccountToken{}, err
	}

	return modelgateway.AccountToken{
		AccountID: t.AccountID, ExternalID: t.ExternalID, AccessToken: t.AccessToken, Project: t.Project,
		Plan: t.Plan, Version: t.Version,
	}, nil
}

// gatewayServedLimits gives the gateway what an Antigravity account (issue #587) or an OpenCode Go key (issue #854)
// is served each model with.
func gatewayServedLimits(
	subs *subscription.Service,
) func(ctx context.Context, kind, upstream string) (int64, int64, bool) {
	return func(ctx context.Context, kind, upstream string) (int64, int64, bool) {
		switch kind {
		case orchestration.ProviderAntigravitySubscription, orchestration.ProviderOpenCodeGo:
			return subs.ServedLimits(ctx, subscriptionRuntimes[kind], upstream)
		default:
			return 0, 0, false
		}
	}
}

// gatewayServedWire gives the gateway the wire OpenCode Go serves each model on, from its model list (issue #854).
func gatewayServedWire(subs *subscription.Service) func(ctx context.Context, kind, upstream string) (string, bool) {
	return func(ctx context.Context, kind, upstream string) (string, bool) {
		if kind != orchestration.ProviderOpenCodeGo {
			return "", false
		}

		return subs.ServedWire(ctx, orchestration.OpenCodeGoRuntime, upstream)
	}
}

// gatewayProviderList lists every provider for the gateway, which serves each subscription's models (issue #549).
func gatewayProviderList(svc *orchestration.Service) func(ctx context.Context) ([]modelgateway.ProviderInfo, error) {
	return func(ctx context.Context) ([]modelgateway.ProviderInfo, error) {
		ps, err := svc.ListProviders(ctx)
		if err != nil {
			return nil, err
		}

		out := make([]modelgateway.ProviderInfo, 0, len(ps))
		for _, p := range ps {
			out = append(out, modelgateway.ProviderInfo{
				Name: p.Name, Kind: p.Kind, BaseURL: p.BaseURL, APIKeyRef: p.APIKeyRef, Models: p.Models,
			})
		}

		return out, nil
	}
}

// gatewayProviders gives the gateway the providers orchestration keeps.
func gatewayProviders(svc *orchestration.Service) modelgateway.Providers {
	return func(ctx context.Context, name string) (modelgateway.ProviderInfo, error) {
		p, err := svc.GetProvider(ctx, name)
		if errors.Is(err, orchestration.ErrNotFound) {
			return modelgateway.ProviderInfo{}, fmt.Errorf("%w: provider %q", modelgateway.ErrNotFound, name)
		}

		if err != nil {
			return modelgateway.ProviderInfo{}, err
		}

		return modelgateway.ProviderInfo{
			Name: p.Name, Kind: p.Kind, BaseURL: p.BaseURL, APIKeyRef: p.APIKeyRef, Models: p.Models,
		}, nil
	}
}

func (o *owner) gatewayCommand(ctx context.Context, args []string) error {
	usage := fmt.Errorf("%w: %s", ErrUsage, gatewayUsage)

	if len(args) >= 1 && args[0] == "usage" {
		return o.gatewayUsageList(ctx, args[1:])
	}

	if len(args) >= 1 && args[0] == "antigravity-rewrites" {
		return o.gatewayRewrites(ctx, args[1:])
	}

	if len(args) < 2 {
		return usage
	}

	rest := args[2:]

	switch args[0] + " " + args[1] {
	case "models apply":
		if len(rest) != 1 {
			return usage
		}

		return o.gatewayModelsApply(ctx, rest[0])
	case "models list":
		if len(rest) != 0 {
			return usage
		}

		return o.gatewayModelsList(ctx)
	case "models rm":
		if len(rest) != 1 {
			return usage
		}

		if err := o.gateway.DeleteModel(ctx, rest[0]); err != nil {
			return err
		}

		o.printf("model %s removed\n", rest[0])

		return nil
	case "keys add":
		if len(rest) == 0 {
			return usage
		}

		return o.gatewayKeysAdd(ctx, rest[0], rest[1:])
	case "keys list":
		if len(rest) != 0 {
			return usage
		}

		return o.gatewayKeysList(ctx)
	case "keys revoke":
		if len(rest) != 1 {
			return usage
		}

		if err := o.gateway.RevokeKey(ctx, rest[0]); err != nil {
			return err
		}

		o.printf("key %s revoked\n", rest[0])

		return nil
	default:
		return usage
	}
}

// gatewayRewrites lists or sets the owner's find→replace rules for the Antigravity system instruction (issue
// #591). "set" reads a JSON array [{"find": "...", "with": "..."}] from a file or - for standard input, and
// replaces every rule; an empty array clears them.
func (o *owner) gatewayRewrites(ctx context.Context, args []string) error {
	usage := fmt.Errorf("%w: %s", ErrUsage, gatewayUsage)

	switch {
	case len(args) == 1 && args[0] == "list":
		rules, err := o.gateway.AntigravityRewrites(ctx)
		if err != nil {
			return err
		}

		if len(rules) == 0 {
			o.printf("no Antigravity rewrites; the system prompt is sent unchanged\n")

			return nil
		}

		for i, r := range rules {
			o.printf("%d\t%q\t%q\n", i+1, r.Find, r.With)
		}

		return nil
	case len(args) == 2 && args[0] == "set":
		source, err := readOwnerInput(o, args[1])
		if err != nil {
			return fmt.Errorf("reading the rewrites: %w", err)
		}

		var rules []modelgateway.Rewrite
		if err := json.Unmarshal(source, &rules); err != nil {
			return fmt.Errorf("the rewrites are not a JSON array of {find, with}: %w", err)
		}

		if err := o.gateway.SetAntigravityRewrites(ctx, rules); err != nil {
			return err
		}

		o.printf("%d Antigravity rewrite(s) set\n", len(rules))

		return nil
	default:
		return usage
	}
}

// readOwnerInput reads the owner's input from a file, or standard input for "-".
func readOwnerInput(o *owner, name string) ([]byte, error) {
	if name == "-" {
		return io.ReadAll(o.in)
	}

	return os.ReadFile(name) //nolint:gosec // the owner names the file
}

func (o *owner) gatewayModelsApply(ctx context.Context, file string) error {
	source, err := os.ReadFile(file) //nolint:gosec // the owner names the file holding the model
	if err != nil {
		return fmt.Errorf("reading the model: %w", err)
	}

	m, err := o.gateway.ApplyModel(ctx, string(source))
	if err != nil {
		return err
	}

	o.printf("model %s (%s) served from %s\n", m.Name, m.Capability, targetList(m.Targets))

	return nil
}

func (o *owner) gatewayModelsList(ctx context.Context) error {
	models, err := o.gateway.ListModels(ctx)
	if err != nil {
		return err
	}

	for _, m := range models {
		o.printf("%s\t%s\t%s\n", m.Name, m.Capability, targetList(m.Targets))
	}

	return nil
}

func targetList(ts []modelgateway.Target) string {
	out := make([]string, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.Provider+":"+t.UpstreamModel)
	}

	return strings.Join(out, ", ")
}

func (o *owner) gatewayKeysAdd(ctx context.Context, name string, args []string) error {
	fs := flag.NewFlagSet("gateway keys add", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	models := fs.String("models", "", "the models the key may use, separated by commas (default: every model)")
	endpoints := fs.String("endpoints", "*",
		"what the key may use besides chat: search, fetch, images, audio, separated by commas, or none (default: "+
			"every kind)")

	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return fmt.Errorf("%w: %s", ErrUsage, gatewayUsage)
	}

	var allowed []string
	if *models != "" {
		allowed = strings.Split(*models, ",")
	}

	var kinds []modelgateway.Endpoint

	switch *endpoints {
	case "*":
	case "none":
		kinds = []modelgateway.Endpoint{}
	default:
		for e := range strings.SplitSeq(*endpoints, ",") {
			kinds = append(kinds, modelgateway.Endpoint(strings.TrimSpace(e)))
		}
	}

	k, secret, err := o.gateway.AddKey(ctx, name, allowed, kinds)
	if err != nil {
		return err
	}

	o.printf("key %s (%s) for %s; %s\n", k.Name, k.ID, keyModels(k), keyEndpoints(k))
	o.printf("shown only now; give it to the client as its API key and keep it nowhere else:\n%s\n", secret)

	return nil
}

func (o *owner) gatewayKeysList(ctx context.Context) error {
	keys, err := o.gateway.ListKeys(ctx)
	if err != nil {
		return err
	}

	for _, k := range keys {
		state := "active"
		if k.RevokedAt != nil {
			state = "revoked " + k.RevokedAt.Format(time.RFC3339)
		}

		o.printf("%s\t%s\t%s\t%s\t%s\n", k.ID, k.Name, state, keyModels(k), keyEndpoints(k))
	}

	return nil
}

func keyModels(k modelgateway.Key) string {
	if k.Allows(modelgateway.AllModels) {
		return "every model"
	}

	return strings.Join(k.Models, ", ")
}

// keyEndpoints says what a key may use besides chat.
func keyEndpoints(k modelgateway.Key) string {
	if k.Endpoints == nil {
		return "search, fetch, images"
	}

	if len(k.Endpoints) == 0 {
		return "no search, fetch or images"
	}

	out := make([]string, 0, len(k.Endpoints))
	for _, e := range k.Endpoints {
		out = append(out, string(e))
	}

	return strings.Join(out, ", ")
}

// gatewayUsageList prints the latest requests the gateway served.
func (o *owner) gatewayUsageList(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("gateway usage", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	limit := fs.Int("limit", 50, "how many of the latest requests to show")
	by := fs.String("by", "", "provider or account: the text models' speed and reliability, as the Usage page")
	period := fs.String("period", "7d", "with --by: 24h, 7d, 30d, 90d or 12m")

	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return fmt.Errorf("%w: %s", ErrUsage, gatewayUsage)
	}

	if *by != "" {
		return o.gatewayPerformance(ctx, *by, *period)
	}

	rows, err := o.gateway.ListUsage(ctx, *limit)
	if err != nil {
		return err
	}

	for _, u := range rows {
		tokens := fmt.Sprintf("in %d (cached %d) out %d (reasoning %d)",
			u.Tokens.Prompt, u.Tokens.Cached, u.Tokens.Completion, u.Tokens.Reasoning)
		if u.Tokens.Estimated {
			tokens += " estimated"
		}

		o.printf("%s\t%s\t%s\t%s:%s\t%s %d\t%s\t%s\n", u.CreatedAt.Format(time.RFC3339), u.KeyName, u.Model,
			u.Provider, u.UpstreamModel, u.Outcome, u.Status, tokens, u.Duration.Round(time.Millisecond))
	}

	return nil
}

// gatewayPerformance prints the Usage page's Performance table (issue #856): each provider's model, or each
// subscription account, with its requests, success rate, time to first token, latency, tokens a second and cost.
// A period read from the daily ledger has averages in place of the percentiles.
func (o *owner) gatewayPerformance(ctx context.Context, by, period string) error {
	if by != "provider" && by != "account" {
		return fmt.Errorf("%w: %s", ErrUsage, gatewayUsage)
	}

	report, err := o.gateway.Usage(ctx, period, 0)
	if err != nil {
		return err
	}

	groups, head := report.ByProvider, "PROVIDER\tMODEL"
	if by == "account" {
		groups, head = report.SpeedByAccount, "PROVIDER\tACCOUNT"
	}

	if report.Ledger {
		o.printf("%s\tREQUESTS\tOK%%\tTTFT AVG\tLATENCY AVG\tTOK/S\tCOST\n", head)
	} else {
		o.printf("%s\tREQUESTS\tOK%%\tTTFT P50\tTTFT P95\tLATENCY P50\tLATENCY P95\tTOK/S P50\tCOST\n", head)
	}

	for _, g := range groups {
		name := g.Model
		if by == "account" {
			name = g.AccountID
		}

		cols := []string{g.Provider, name, fmt.Sprint(g.Requests), percent(g.Performance)}
		if report.Ledger {
			cols = append(cols, millis(g.TTFTAvg), millis(g.LatencyAvg))
		} else {
			cols = append(cols, millis(g.TTFTP50), millis(g.TTFTP95), millis(g.LatencyP50), millis(g.LatencyP95))
		}

		rate := "-"
		if g.TokensPerSecond != nil {
			rate = fmt.Sprintf("%.1f", *g.TokensPerSecond)
		}

		cols = append(cols, rate, fmt.Sprintf("$%.4f", float64(g.CostMicros)/1e6))
		o.printf("%s\n", strings.Join(cols, "\t"))
	}

	return nil
}

func percent(p modelgateway.Performance) string {
	rate, ok := p.SuccessRate()
	if !ok {
		return "-"
	}

	return fmt.Sprintf("%.1f", rate*100)
}

func millis(d *time.Duration) string {
	if d == nil {
		return "-"
	}

	return d.Round(time.Millisecond).String()
}

// subscriptionRuntimes is the runtime of the accounts of each kind of subscription provider.
var subscriptionRuntimes = map[string]string{
	orchestration.ProviderCodexSubscription:       "codex",
	orchestration.ProviderClaudeSubscription:      "claude",
	orchestration.ProviderAntigravitySubscription: "antigravity",
	orchestration.ProviderOpenCodeGo:              orchestration.OpenCodeGoRuntime,
}

// vendorModelsEvery is how often the subscription providers are given the models their vendors serve, and
// vendorModelsFirst how long after the start the first time is: the accounts' tokens are made fresh first. The
// vendor is asked at most once an hour anyway (subscription.ServedNames).
const (
	vendorModelsEvery = time.Hour
	vendorModelsFirst = 2 * time.Minute
)

// servedNames is what a runtime's accounts are served (subscription.Service.ServedNames).
type servedNames interface {
	ServedNames(ctx context.Context, runtime string) ([]string, bool)
}

// providerModels lists the providers and adds models to one (orchestration.Service).
type providerModels interface {
	ListProviders(ctx context.Context) ([]orchestration.Provider, error)
	AddProviderModels(ctx context.Context, name string, models []string) ([]string, error)
	SetProviderModels(ctx context.Context, name string, models []string) (added, removed []string, err error)
}

// rosterRuntimes are the runtimes whose vendor's list is the provider's list, as omp takes Antigravity's roster as
// authoritative (packages/catalog/src/discovery/antigravity.ts; issue #639), and as OpenClaw, Hermes and pi take
// OpenCode Go's live /v1/models (issue #854). The others only gain models: Anthropic's list does not name every model
// it serves (OpenClaw, Hermes), and Codex's is read per account.
var rosterRuntimes = map[string]bool{"antigravity": true, orchestration.OpenCodeGoRuntime: true}

// vendorModels gives the subscription providers the models their vendors serve, one update at a time: every hour
// (vendorModelsLoop) and when an account's first list of models is read (issue #814).
type vendorModels struct {
	mu        sync.Mutex
	subs      servedNames
	providers providerModels
	logger    *slog.Logger
	// misses counts, by provider and model, the answered readings in a row that left the model out (dropAfter).
	misses map[string]map[string]int
}

func (v *vendorModels) update(ctx context.Context) {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.misses == nil {
		v.misses = map[string]map[string]int{}
	}

	addVendorModels(ctx, v.subs, v.providers, v.logger, v.misses)
}

// dropAfter is how many answered readings in a row must leave a model out before a Codex provider drops it (review
// of #832): one is not enough, as an account can be read without it once.
const dropAfter = 2

// droppingRuntimes are the runtimes whose providers drop, after dropAfter readings, a model their vendor's lists
// leave out: Codex, whose requests go only to the accounts whose list has the model (issue #814), so a model no list
// names would be listed by /v1/models and refused. Claude's list does not name every model it serves.
var droppingRuntimes = map[string]bool{"codex": true}

// kept are the names a provider is set to: what the vendor serves, and each model it lists that was left out of
// fewer than dropAfter answered readings in a row. misses is updated.
func kept(listed, served []string, misses map[string]int) []string {
	out := slices.Clone(served)

	for name := range misses {
		if slices.Contains(served, name) {
			delete(misses, name)
		}
	}

	for _, name := range listed {
		if slices.Contains(served, name) {
			continue
		}

		misses[name]++
		if misses[name] < dropAfter {
			out = append(out, name)
		}
	}

	return out
}

// vendorModelsLoop gives each subscription provider the models its vendor serves (issue #623), once soon after
// the start and then every hour, until ctx ends.
func vendorModelsLoop(ctx context.Context, v *vendorModels) {
	timer := time.NewTimer(vendorModelsFirst)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}

		v.update(ctx)
		timer.Reset(vendorModelsEvery)
	}
}

// addVendorModels adds to each subscription provider the models its vendor serves that it does not list, as Codex,
// OpenClaw, omp and Hermes take a subscription's models from its vendor. A vendor that does not answer, or a runtime
// with no account logged in, leaves the provider as it is: its list is the fallback.
func addVendorModels(
	ctx context.Context, subs servedNames, providers providerModels, logger *slog.Logger,
	misses map[string]map[string]int,
) {
	list, err := providers.ListProviders(ctx)
	if err != nil {
		logger.WarnContext(ctx, "the providers could not be read for their vendors' models",
			slog.String("event", "gateway.vendor_models_failed"), slog.String("error", err.Error()))

		return
	}

	for _, p := range list {
		runtime, ok := subscriptionRuntimes[p.Kind]
		if !ok {
			continue
		}

		names, ok := subs.ServedNames(ctx, runtime)
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
		case droppingRuntimes[runtime] && misses != nil:
			if misses[p.Name] == nil {
				misses[p.Name] = map[string]int{}
			}

			added, removed, err = providers.SetProviderModels(ctx, p.Name, kept(p.Models, names, misses[p.Name]))
			if err != nil {
				// A model a profile uses refuses the drop (SetProviderModels): the provider still gains the new ones.
				logger.WarnContext(ctx, "a provider's models its vendor no longer serves were not dropped",
					slog.String("event", "gateway.vendor_models_kept"), slog.String("provider", p.Name),
					slog.String("error", err.Error()))

				removed = nil
				added, err = providers.AddProviderModels(ctx, p.Name, names)
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
