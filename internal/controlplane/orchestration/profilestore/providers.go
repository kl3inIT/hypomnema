package profilestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/storeutil"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/storeerr"
	"github.com/kl3inIT/hypomnema/internal/shared/agentconfig"
)

// Provider kinds.
const (
	ProviderAnthropic          = "anthropic"
	ProviderOpenAICompatible   = "openai_compatible"
	ProviderClaudeSubscription = "claude_subscription"
	ProviderCodexSubscription  = "codex_subscription"
	// ProviderAntigravitySubscription is served by the model gateway only: no
	// worker's CLI runs under an Antigravity account.
	ProviderAntigravitySubscription = "antigravity_subscription"
	// ProviderOpenCodeGo is OpenCode Go (issue #854), served by the model gateway only under the Go keys the
	// owner pastes (the root's OpenCodeGoRuntime).
	ProviderOpenCodeGo = "opencode_go"
	// ProviderOpenCodeZen is OpenCode Zen (issue #965), OpenCode's models paid from the workspace's balance, served
	// by the model gateway only under the Console keys the owner pastes (the root's OpenCodeZenRuntime).
	ProviderOpenCodeZen = "opencode_zen"
	// ProviderModelGateway is the control plane's own model gateway: a worker
	// under it calls the gateway with a key made for its Dispatch, and which
	// account or API serves the model is the gateway's business (issue #107).
	// It has no base URL and no key of its own.
	ProviderModelGateway = "model_gateway"
)

// GatewayAccountRef stands where an account's id does for a Conversation
// whose model the gateway serves: no account on the node runs it.
const GatewayAccountRef = "gateway"

// subscriptionKinds are the providers whose accounts are logged in on the control plane. The gateway serves each
// one's models under "<provider>/<model>" with no table row (issue #549, as 9router-go lists the models of every
// active connection and CLIProxyAPI's registry fills from its logged-in accounts), so a model chosen under a
// subscription runs through the gateway: no subscription token leaves the control plane (issue #381).
var subscriptionKinds = []string{
	ProviderClaudeSubscription, ProviderCodexSubscription, ProviderAntigravitySubscription, ProviderOpenCodeGo,
	ProviderOpenCodeZen,
}

// ThroughGateway reports whether a provider of the kind is reached through the model gateway: the gateway itself,
// for a name made in its table, or a subscription it serves.
func ThroughGateway(kind string) bool {
	return kind == ProviderModelGateway || slices.Contains(subscriptionKinds, kind)
}

// GatewayModelName is the name the gateway serves a provider's model under: "<provider>/<model>" for a
// subscription (issue #549), the model's own name for the gateway's table.
func GatewayModelName(kind, provider, model string) string {
	if slices.Contains(subscriptionKinds, kind) {
		return provider + "/" + model
	}

	return model
}

// PiProvider is the default provider of pi's models.json (clientcli pi-models): every model the gateway serves, each
// under its gateway name with the wire of its subscription (issue #549).
const PiProvider = "hypomnema"

const PiFallbackProvider = "hypomnema-fallback"

// GatewayWires is the wire a worker speaks to the gateway for a subscription's model, where it can choose
// (issue #549); its one home is agentconfig.Wires, which the Connect a tool page's files use too (issue #907).
var GatewayWires = agentconfig.Wires

// ModelAPITx is the wire for a Dispatch's model: its subscription's, when the gateway serves it under
// "<provider>/<model>".
func ModelAPITx(ctx context.Context, q *store.Queries, accountRef, model string) (string, error) {
	provider, _, found := strings.Cut(model, "/")
	if accountRef != GatewayAccountRef || !found {
		return "", nil
	}

	row, err := q.GetProvider(ctx, provider)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}

	if err != nil {
		return "", fmt.Errorf("reading provider %s: %w", provider, err)
	}

	return GatewayWires[row.Kind], nil
}

// gatewayKinds are the gateway and every subscription it serves.
var gatewayKinds = append([]string{ProviderModelGateway}, subscriptionKinds...)

// runtimeProviders lists, per runtime, the provider kinds it can run against.
// A Runtime is code on the node; a profile only names one.
var runtimeProviders = map[string][]string{
	// Claude Code runs only through the gateway's /v1/messages, with its Dispatch's key: the subscription token
	// never leaves the control plane (issue #381).
	"claude": gatewayKinds,
	// A Codex worker under a Codex subscription runs through the gateway too (issue #549); one under no provider
	// logs in with an account its node holds. Codex speaks only /v1/responses, which the gateway passes on to a
	// Codex subscription alone (modelgateway serveResponses), so no other subscription is offered to it.
	"codex": {ProviderOpenAICompatible, ProviderModelGateway, ProviderCodexSubscription},
	// pi reaches its model through the gateway as omp and opencode do (issue #204).
	"pi": append([]string{ProviderAnthropic, ProviderOpenAICompatible}, gatewayKinds...),
	// omp and opencode are workers whose model the gateway serves (issue #107).
	"omp":      gatewayKinds,
	"opencode": gatewayKinds,
}

// GatewayOnly reports whether a worker of the runtime runs only through the
// model gateway, never under an account a node holds: Claude Code, the root's
// TokenRuntime (issue #381). omp and opencode name only the gateway too; their
// placement stays as it was, since issue #381 moved Claude Code only.
func GatewayOnly(runtime string) bool { return runtime == "claude" }

// NeedsGatewayMessage says why a Claude Code worker cannot run elsewhere.
const NeedsGatewayMessage = "a Claude Code worker runs through the model gateway; its profile must name a " +
	"subscription or a provider of kind " + ProviderModelGateway

var keyRefPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_./:-]{0,99}$`)

// Provider is a source of models, such as a subscription or an API.
type Provider struct {
	Name string
	Kind string
	// BaseURL and APIKeyRef serve API providers. APIKeyRef names where the key
	// is kept; the key itself is never stored here.
	BaseURL   string
	APIKeyRef string
	Models    []string
	Source    string
	UpdatedAt time.Time
}

// Model is one model a provider offers.
type Model struct {
	Provider string
	Name     string
	Kind     string
}

type providerSource struct {
	Name      string   `yaml:"name"`
	Kind      string   `yaml:"kind"`
	BaseURL   string   `yaml:"base_url,omitempty"`
	APIKeyRef string   `yaml:"api_key_ref,omitempty"`
	Models    []string `yaml:"models,flow"`
	// MinVersions are the oldest version of each family of models the vendor's list adds, by the name's first word:
	// {gpt: "6", gemini: "3.8"} (owner, 2026-11-16: "Gemini 3.8 and up, Codex 6 and up"; issue #642).
	MinVersions map[string]string `yaml:"min_versions,omitempty"`
}

// BelowFloor says a model is older than the provider's min_versions keeps for its family, so the vendor's list does
// not add it (issue #642). A provider whose source cannot be read has no floor.
func (p Provider) BelowFloor(model string) bool {
	var in providerSource
	if err := decodeStrict(p.Source, &in); err != nil {
		return false
	}

	return belowFloor(model, in.MinVersions)
}

// ApplyProvider stores a provider's YAML and the models it offers. It refuses
// a change that would break a profile's current version: a model it uses
// removed, or a kind its runtime cannot use. A non-empty name must equal the
// source's.
func (s *Store) ApplyProvider(ctx context.Context, name, source string) (Provider, error) {
	var in providerSource
	if err := decodeStrict(source, &in); err != nil {
		return Provider{}, err
	}

	if err := validProvider(in); err != nil {
		return Provider{}, err
	}

	if name != "" && name != in.Name {
		return Provider{}, fmt.Errorf("%w: the source names provider %q, not %q", storeerr.ErrInvalid, in.Name, name)
	}

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		if err := providerStillServes(ctx, q, in); err != nil {
			return err
		}

		if err := q.UpsertProvider(ctx, store.UpsertProviderParams{
			Name: in.Name, Kind: in.Kind, BaseUrl: nullString(in.BaseURL), ApiKeyRef: nullString(in.APIKeyRef),
			Source: source, Now: now,
		}); err != nil {
			return fmt.Errorf("storing provider %s: %w", in.Name, err)
		}

		if err := q.DeleteModelsOfProvider(ctx, in.Name); err != nil {
			return fmt.Errorf("replacing the models of %s: %w", in.Name, err)
		}

		for _, m := range in.Models {
			if err := q.InsertModel(ctx, store.InsertModelParams{Provider: in.Name, Name: m}); err != nil {
				return fmt.Errorf("storing model %s of %s: %w", m, in.Name, err)
			}
		}

		return nil
	})
	if err != nil {
		return Provider{}, fmt.Errorf("applying a provider: %w", err)
	}

	return s.GetProvider(ctx, in.Name)
}

// AddProviderModels adds to a provider the models it does not list yet, as a subscription's vendor names what it
// serves (issue #623): Codex reads its catalog from its backend and keeps a bundled one (codex-rs/models-manager),
// as OpenClaw, omp and Hermes do. Nothing is removed, so a key or a Bot that names a model the vendor stopped listing
// keeps it until the owner changes the provider. The source is written again with the models added. added are the
// ones that were not there.
func (s *Store) AddProviderModels(ctx context.Context, name string, models []string) (added []string, err error) {
	p, err := s.GetProvider(ctx, name)
	if err != nil {
		return nil, err
	}

	var in providerSource
	if err := decodeStrict(p.Source, &in); err != nil {
		return nil, err
	}

	for _, m := range models {
		if m = strings.TrimSpace(m); m != "" && !slices.Contains(in.Models, m) && !belowFloor(m, in.MinVersions) {
			in.Models = append(in.Models, m)
			added = append(added, m)
		}
	}

	if len(added) == 0 {
		return nil, nil
	}

	source, err := yaml.Marshal(in)
	if err != nil {
		return nil, fmt.Errorf("writing provider %s: %w", name, err)
	}

	if _, err := s.ApplyProvider(ctx, name, string(source)); err != nil {
		return nil, err
	}

	return added, nil
}

// SetProviderModels makes a provider's models the ones its vendor serves, for a vendor whose list is authoritative:
// omp drops an Antigravity model its roster no longer lists, which a request would get 404 for
// (packages/catalog/src/discovery/antigravity.ts; issue #639). The models kept keep their order and the new ones
// follow. A model a profile's current version uses is not removed: the change is refused as ApplyProvider refuses it.
func (s *Store) SetProviderModels(
	ctx context.Context, name string, models []string,
) (added, removed []string, err error) {
	p, err := s.GetProvider(ctx, name)
	if err != nil {
		return nil, nil, err
	}

	var in providerSource
	if err := decodeStrict(p.Source, &in); err != nil {
		return nil, nil, err
	}

	served := map[string]bool{}
	for _, m := range models {
		if m = strings.TrimSpace(m); m != "" && !belowFloor(m, in.MinVersions) {
			served[m] = true
		}
	}

	kept := make([]string, 0, len(models))

	for _, m := range in.Models {
		if served[m] {
			kept = append(kept, m)
		} else {
			removed = append(removed, m)
		}
	}

	for _, m := range models {
		if m = strings.TrimSpace(m); served[m] && !slices.Contains(kept, m) {
			kept = append(kept, m)
			added = append(added, m)
		}
	}

	if len(added) == 0 && len(removed) == 0 {
		return nil, nil, nil
	}

	in.Models = kept

	source, err := yaml.Marshal(in)
	if err != nil {
		return nil, nil, fmt.Errorf("writing provider %s: %w", name, err)
	}

	if _, err := s.ApplyProvider(ctx, name, string(source)); err != nil {
		return nil, nil, err
	}

	return added, removed, nil
}

// RemoveProviderModel takes one model off a provider's list: one its vendor no longer serves (issue #814). Nothing
// else on the list changes. A model a profile's current version uses is not removed: the change is refused as
// ApplyProvider refuses it. removed is false when the provider did not list the model.
func (s *Store) RemoveProviderModel(ctx context.Context, name, model string) (removed bool, err error) {
	p, err := s.GetProvider(ctx, name)
	if err != nil {
		return false, err
	}

	var in providerSource
	if err := decodeStrict(p.Source, &in); err != nil {
		return false, err
	}

	if !slices.Contains(in.Models, model) {
		return false, nil
	}

	in.Models = slices.DeleteFunc(in.Models, func(m string) bool { return m == model })

	source, err := yaml.Marshal(in)
	if err != nil {
		return false, fmt.Errorf("writing provider %s: %w", name, err)
	}

	if _, err := s.ApplyProvider(ctx, name, string(source)); err != nil {
		return false, err
	}

	return true, nil
}

// belowFloor says whether a model is of a family whose oldest version is named and is older than it (issue #642).
// A name with no version that can be read, such as gpt-oss-120b, is never below.
func belowFloor(model string, floors map[string]string) bool {
	family, version, ok := modelVersion(model)
	if !ok {
		return false
	}

	floor, named := floors[family]
	if !named {
		return false
	}

	least, _ := parseVersion(floor)

	return slices.Compare(padded(version, len(least)), padded(least, len(version))) < 0
}

// modelVersion reads a model's family and version from its name: the first word, and the first word that is a
// version. A second short number is its minor version, as Anthropic writes them: claude-opus-5-5 is claude 5.5,
// claude-haiku-4-5-20251001 claude 4.5, gemini-3.8-flash-high gemini 3.8, gpt-6.1-sol gpt 6.1.
func modelVersion(name string) (family string, version []int, ok bool) {
	parts := strings.Split(name, "-")

	for i := 1; i < len(parts); i++ {
		if parts[i] == "" || parts[i][0] < '0' || parts[i][0] > '9' {
			continue
		}

		version, ok = parseVersion(parts[i])
		if !ok {
			return "", nil, false
		}

		if len(version) == 1 && i+1 < len(parts) && len(parts[i+1]) <= 2 {
			if minor, ok := parseVersion(parts[i+1]); ok && len(minor) == 1 {
				version = append(version, minor[0])
			}
		}

		return parts[0], version, true
	}

	return "", nil, false
}

// parseVersion reads "6" or "3.8" as its numbers.
func parseVersion(v string) ([]int, bool) {
	var out []int

	for _, seg := range strings.Split(v, ".") {
		n, err := strconv.Atoi(seg)
		if err != nil || n < 0 {
			return nil, false
		}

		out = append(out, n)
	}

	return out, len(out) > 0
}

// padded gives v n numbers at least, the missing ones zero: 6 is 6.0 beside 6.1.
func padded(v []int, n int) []int {
	for len(v) < n {
		v = append(slices.Clone(v), 0)
	}

	return v
}

func validProvider(in providerSource) error {
	kinds := []string{
		ProviderAnthropic, ProviderOpenAICompatible, ProviderClaudeSubscription, ProviderCodexSubscription,
		ProviderAntigravitySubscription, ProviderOpenCodeGo, ProviderOpenCodeZen, ProviderModelGateway,
	}

	switch {
	case !namePattern.MatchString(in.Name):
		return fmt.Errorf("%w: a provider name is lowercase letters, digits and dashes", storeerr.ErrInvalid)
	case !slices.Contains(kinds, in.Kind):
		return fmt.Errorf("%w: kind is one of %s", storeerr.ErrInvalid, strings.Join(kinds, ", "))
	case in.APIKeyRef != "" && (!keyRefPattern.MatchString(in.APIKeyRef) || looksLikeSecret(in.APIKeyRef)):
		return fmt.Errorf("%w: api_key_ref names where the key is kept (such as an environment variable), "+
			"never the key itself", storeerr.ErrInvalid)
	case in.BaseURL != "" && !strings.HasPrefix(in.BaseURL, "https://") && !strings.HasPrefix(in.BaseURL, "http://"):
		return fmt.Errorf("%w: base_url is an http or https URL", storeerr.ErrInvalid)
	}

	for family, v := range in.MinVersions {
		if _, ok := parseVersion(v); !ok || family == "" {
			return fmt.Errorf("%w: min_versions names a family and a version such as 6 or 3.8", storeerr.ErrInvalid)
		}
	}

	seen := map[string]bool{}

	for _, m := range in.Models {
		if strings.TrimSpace(m) == "" || seen[m] {
			return fmt.Errorf("%w: model names are non-empty and listed once", storeerr.ErrInvalid)
		}

		seen[m] = true
	}

	return nil
}

// looksLikeSecret catches a key pasted where its name belongs.
func looksLikeSecret(s string) bool {
	for _, prefix := range []string{"sk-", "sk_", "hyp_", "ghp_", "gho_", "github_pat_", "xox", "AKIA"} {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}

	return false
}

// providerStillServes checks that the current profile versions and the Bots
// using a provider keep their model and runtime after the change.
func providerStillServes(ctx context.Context, q *store.Queries, in providerSource) error {
	bots, err := q.ListBotModels(ctx)
	if err != nil {
		return fmt.Errorf("listing bots: %w", err)
	}

	for _, b := range bots {
		if b.Provider.String != in.Name {
			continue
		}

		if !slices.Contains(runtimeProviders[BotRuntime], in.Kind) {
			return fmt.Errorf("%w: bot %s runs %s on provider %s, which kind %s cannot serve", storeerr.ErrConflict,
				b.Name, BotRuntime, in.Name, in.Kind)
		}

		if b.Model.Valid && !slices.Contains(in.Models, b.Model.String) {
			return fmt.Errorf("%w: bot %s uses model %s of provider %s", storeerr.ErrConflict, b.Name, b.Model.String, in.Name)
		}
	}

	versions, err := q.ListCurrentProfileVersions(ctx)
	if err != nil {
		return fmt.Errorf("listing profiles: %w", err)
	}

	for _, v := range versions {
		if v.Provider.String != in.Name {
			continue
		}

		if !slices.Contains(runtimeProviders[v.Runtime], in.Kind) {
			return fmt.Errorf("%w: profile %s runs %s on provider %s, which kind %s cannot serve", storeerr.ErrConflict,
				v.Profile, v.Runtime, in.Name, in.Kind)
		}

		if v.Model.Valid && !slices.Contains(in.Models, v.Model.String) {
			return fmt.Errorf("%w: profile %s uses model %s of provider %s", storeerr.ErrConflict, v.Profile, v.Model.String,
				in.Name)
		}
	}

	return nil
}

// DeleteProvider removes a provider and its models, when nothing names it. A provider that a bot, a chat, a profile
// version or a gateway model's target still uses is kept, with what uses it named in the error, so no reference is
// orphaned (issue #610).
func (s *Store) DeleteProvider(ctx context.Context, name string) error {
	return s.write(ctx, func(q *store.Queries, _ int64) error {
		if _, err := q.GetProvider(ctx, name); err != nil {
			return storeutil.NotFound(err, "provider", name)
		}

		uses, err := q.ProviderUses(ctx, sql.NullString{String: name, Valid: true})
		if err != nil {
			return fmt.Errorf("counting what uses provider %s: %w", name, err)
		}

		counts := []struct {
			n    int64
			what string
		}{
			{uses.Bots, "bot"}, {uses.Chats, "chat"},
			{uses.Versions, "profile version"}, {uses.Targets, "gateway model target"},
		}

		var used []string
		for _, u := range counts {
			if u.n > 0 {
				used = append(used, fmt.Sprintf("%d %s(s)", u.n, u.what))
			}
		}

		if len(used) > 0 {
			return fmt.Errorf("%w: provider %s is used by %s", storeerr.ErrConflict, name, strings.Join(used, ", "))
		}

		if err := q.DeleteModelsOfProvider(ctx, name); err != nil {
			return fmt.Errorf("removing the models of %s: %w", name, err)
		}

		if _, err := q.DeleteProvider(ctx, name); err != nil {
			return fmt.Errorf("deleting provider %s: %w", name, err)
		}

		return nil
	})
}

// GetProvider returns a provider and its models.
func (s *Store) GetProvider(ctx context.Context, name string) (Provider, error) {
	q := s.read()

	row, err := q.GetProvider(ctx, name)
	if err != nil {
		return Provider{}, storeutil.NotFound(err, "provider", name)
	}

	models, err := s.ListModels(ctx)
	if err != nil {
		return Provider{}, err
	}

	return providerFromRow(row, models), nil
}

// ProvidersUsingKeyRef names the providers whose api_key_ref is ref, so the
// vault can refuse to delete a secret a provider still names.
func (s *Store) ProvidersUsingKeyRef(ctx context.Context, ref string) ([]string, error) {
	names, err := s.read().ListProviderNamesByKeyRef(ctx, nullString(ref))
	if err != nil {
		return nil, fmt.Errorf("listing the providers that use %q: %w", ref, err)
	}

	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, "provider "+n)
	}

	return out, nil
}

// ListProviders returns every provider with its models, by name.
func (s *Store) ListProviders(ctx context.Context) ([]Provider, error) {
	rows, err := s.read().ListProviders(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing providers: %w", err)
	}

	models, err := s.ListModels(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]Provider, 0, len(rows))
	for _, r := range rows {
		out = append(out, providerFromRow(r, models))
	}

	return out, nil
}

func providerFromRow(r store.Provider, models []Model) Provider {
	p := Provider{
		Name: r.Name, Kind: r.Kind, BaseURL: r.BaseUrl.String, APIKeyRef: r.ApiKeyRef.String, Source: r.Source,
		UpdatedAt: storeutil.Millis(r.UpdatedAt), Models: []string{},
	}

	for _, m := range models {
		if m.Provider == r.Name {
			p.Models = append(p.Models, m.Name)
		}
	}

	return p
}

// ListModels returns every model with its provider and the provider's kind.
func (s *Store) ListModels(ctx context.Context) ([]Model, error) {
	rows, err := s.read().ListModels(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing models: %w", err)
	}

	out := make([]Model, 0, len(rows))
	for _, r := range rows {
		out = append(out, Model{Provider: r.Provider, Name: r.Name, Kind: r.Kind})
	}

	return out, nil
}
