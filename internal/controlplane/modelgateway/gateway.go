// Package modelgateway is the control plane's model gateway (issue #20): an
// OpenAI-compatible API over a model table the owner declares, for the
// coordinator, workers that are not a vendor's CLI and the owner's own tools.
// It is the only reader and writer of the gateway_* tables.
//
// The model table follows LiteLLM's model_list and new-api's abilities: a
// public name with its targets (a provider and the model's name there) in the
// order tried; a name that is not declared is refused. Keys follow Bifrost's
// virtual keys: stored as a hash, each with the models it may use. Vendor CLIs
// (Claude Code, Codex) do not come through here; they talk to their vendor.
//
// It serves chat (POST /v1/chat/completions) from OpenAI-compatible providers
// and records what it served; subscriptions and other capabilities are later
// steps of the issue.
package modelgateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway/internal/store"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

// Errors callers match with errors.Is.
var (
	// ErrNotFound: no model or key has this name or id.
	ErrNotFound = errors.New("not found")
	// ErrConflict: the name is taken, or the key is already revoked.
	ErrConflict = errors.New("conflict")
	// ErrInvalid: the model's document or the key's request is wrong.
	ErrInvalid = errors.New("invalid request")
	// ErrUnauthorized: the key is malformed, unknown or revoked.
	ErrUnauthorized = errors.New("unknown gateway key")
	// ErrUnavailable: a gateway operation requires an unavailable service.
	ErrUnavailable = errors.New("unavailable")
	// ErrForbidden: the key may not use the model.
	ErrForbidden = errors.New("the key may not use this model")
)

// Capability is what a model does. Each has its own endpoint and adapter
// interface, so a new one touches no existing adapter.
type Capability string

// CapabilityChat is text in, text out: /v1/chat/completions.
const CapabilityChat Capability = "chat"

// AllModels in a key's list lets it use every model, as Bifrost's "*".
const AllModels = "*"

// servedKinds are the provider kinds the gateway has an adapter for.
var servedKinds = slices.Sorted(maps.Keys(chatAdapters))

// A public model name may carry a vendor-style path, as "cx/gpt-6.1-sol".
var modelNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,99}$`)

var keyNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// Target is one place a model is served from.
type Target struct {
	// Provider names a provider the control plane knows; UpstreamModel is the
	// model's name there.
	Provider      string
	UpstreamModel string
}

// Model is a model the gateway serves.
type Model struct {
	// Name is what clients call it.
	Name       string
	Capability Capability
	// Targets are tried in order.
	Targets []Target
	// Attributes are what the YAML sets of the model's attributes; Resolve
	// gives them with models.dev's for the rest.
	Attributes Attributes
	// Source is the YAML as applied.
	Source    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Key is a gateway key without its secret.
type Key struct {
	ID   string
	Name string
	// Models are the public names the key may use; AllModels is every one.
	Models []string
	// Endpoints are the kinds the key may use besides chat (issue #664); nil is every kind.
	Endpoints []Endpoint
	// DispatchID is set on a key made for one Dispatch.
	DispatchID string
	// ChatID is set on a key made for one chat's pi.
	ChatID string
	botID  string
	// Prefix is the key's start, shown to tell keys apart; empty for a key made
	// before it was kept.
	Prefix      string
	MaskedValue string
	CreatedAt   time.Time
	RevokedAt   *time.Time
	// RevokeReason says why a revoked key stopped (issue #996); empty for a live one or one revoked before reasons
	// were kept.
	RevokeReason bearertoken.RevokeReason
	// LastUsedAt is when the key last authenticated, written at most once an hour, or when the gateway last served
	// a request with it, whichever is later; nil for one never used. ListKeys reads the requests.
	LastUsedAt *time.Time
}

// OwnerMade reports whether this key belongs to the owner rather than a worker.
func (k Key) OwnerMade() bool {
	return k.DispatchID == "" && k.ChatID == "" && k.botID == ""
}

// keyPrefixLength is how much of a key is kept in the clear: its kind and six
// characters of its secret, as Coder shows a key's prefix.
const keyPrefixLength = len("hyp_gateway_v1_") + 6

// Allows reports whether the key may use a model.
func (k Key) Allows(model string) bool {
	return slices.Contains(k.Models, AllModels) || slices.Contains(k.Models, model)
}

// Endpoint is a kind of request a key may make besides chat (issue #664), as OmniRoute's
// endpoint categories (src/shared/constants/endpointCategories.ts) group its routes.
type Endpoint string

// The kinds: a web search (/v1/search and Codex's /v1/alpha/search), a page read (/v1/web/fetch), an image made
// or edited (/v1/images/*), and speech heard or made with the voices listed (/v1/audio/*, issue #694, as OmniRoute's
// audio category is /v1/audio: src/shared/constants/endpointCategories.ts:52).
const (
	EndpointSearch Endpoint = "search"
	EndpointFetch  Endpoint = "fetch"
	EndpointImages Endpoint = "images"
	EndpointAudio  Endpoint = "audio"
)

// Endpoints are every kind, in the order shown.
var Endpoints = []Endpoint{EndpointSearch, EndpointFetch, EndpointImages, EndpointAudio}

// Reaches reports whether the key may make a request of a kind: every key may when it lists none, as OmniRoute's
// empty allowed_endpoints allows all (src/shared/utils/apiKeyPolicy.ts validateEndpointAccess).
func (k Key) Reaches(e Endpoint) bool {
	return k.Endpoints == nil || slices.Contains(k.Endpoints, e)
}

// ProviderInfo is what the gateway needs to know of a provider.
type ProviderInfo struct {
	Name      string
	Kind      string
	BaseURL   string
	APIKeyRef string
	// Models are the names the provider offers.
	Models []string
	// Admits says whether the provider takes a model its vendor lists, past its version floor (min_versions, issue
	// #642); nil takes every model.
	Admits func(model string) bool
}

// Providers looks a provider up; the control plane's wiring gives the
// orchestration service's. An unknown name is an error wrapping ErrNotFound.
type Providers func(ctx context.Context, name string) (ProviderInfo, error)

// Service performs the gateway's operations.
type Service struct {
	db        *sql.DB
	clock     platform.Clock
	newID     func(prefix string) string
	providers Providers
	secrets   Secrets
	keyVault  *vault.Service
	// allProviders lists every provider, for the models of the subscriptions (issue #549).
	allProviders func(ctx context.Context) ([]ProviderInfo, error)
	// served gives the window and largest output a subscription's vendor serves a model with (issue #587).
	served func(ctx context.Context, kind, upstream string) (window, output int64, ok bool)
	// wire gives the API a subscription's vendor serves a model on, for a vendor that serves several (issue #854).
	wire func(ctx context.Context, kind, upstream string) (string, bool)
}

// New returns a Service over an opened, migrated database. providers looks
// providers up and secrets gives the key a provider's api_key_ref names.
func New(
	db *sql.DB, clock platform.Clock, newID func(prefix string) string, providers Providers, secrets Secrets,
) *Service {
	return &Service{db: db, clock: clock, newID: newID, providers: providers, secrets: secrets}
}

func (s *Service) write(ctx context.Context, fn func(q *store.Queries, now int64) error) error {
	return s.writeTx(ctx, func(tx *sql.Tx, now int64) error { return fn(store.New(tx), now) })
}

func (s *Service) writeTx(ctx context.Context, fn func(tx *sql.Tx, now int64) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning a transaction: %w", err)
	}

	if err := fn(tx, s.clock.Now().UnixMilli()); err != nil {
		return errors.Join(err, tx.Rollback())
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing: %w", err)
	}

	return nil
}

type modelSource struct {
	Name       string `yaml:"name"`
	Capability string `yaml:"capability"`
	Targets    []struct {
		Provider string `yaml:"provider"`
		Model    string `yaml:"model"`
	} `yaml:"targets"`
	Attributes `yaml:",inline"`
}

// ApplyModel stores a model's YAML: its public name, what it does, where
// it is served from and the attributes it sets. Applying a name again replaces the model. Every target
// must name a provider the gateway has an adapter for and a model that
// provider offers, so a model that cannot be served is refused here and not
// at the first request.
func (s *Service) ApplyModel(ctx context.Context, source string) (Model, error) {
	var in modelSource

	dec := yaml.NewDecoder(strings.NewReader(source))
	dec.KnownFields(true)

	if err := dec.Decode(&in); err != nil {
		return Model{}, fmt.Errorf("%w: not a valid document: %w", ErrInvalid, err)
	}

	if in.Capability == "" {
		in.Capability = string(CapabilityChat)
	}

	switch {
	case !modelNamePattern.MatchString(in.Name):
		return Model{}, fmt.Errorf("%w: a model's name is letters, digits and . _ : / -", ErrInvalid)
	case in.Capability != string(CapabilityChat):
		return Model{}, fmt.Errorf("%w: capability is %s", ErrInvalid, CapabilityChat)
	case len(in.Targets) == 0:
		return Model{}, fmt.Errorf("%w: a model needs at least one target", ErrInvalid)
	}

	for i, t := range in.Targets {
		kind, err := s.checkTarget(ctx, t.Provider, t.Model)
		if err != nil {
			return Model{}, fmt.Errorf("target %d: %w", i+1, err)
		}

		if i == 0 {
			if err := checkAttributes(in.Attributes, kind, t.Model); err != nil {
				return Model{}, err
			}
		}
	}

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		if err := q.UpsertGatewayModel(ctx, store.UpsertGatewayModelParams{
			Name: in.Name, Capability: in.Capability, Source: source, Now: now,
		}); err != nil {
			return fmt.Errorf("storing model %q: %w", in.Name, err)
		}

		if err := q.DeleteGatewayModelTargets(ctx, in.Name); err != nil {
			return fmt.Errorf("replacing the targets of model %q: %w", in.Name, err)
		}

		for i, t := range in.Targets {
			if err := q.InsertGatewayModelTarget(ctx, store.InsertGatewayModelTargetParams{
				Model: in.Name, Position: int64(i), Provider: t.Provider, UpstreamModel: t.Model,
			}); err != nil {
				return fmt.Errorf("storing target %d of model %q: %w", i+1, in.Name, err)
			}
		}

		return nil
	})
	if err != nil {
		return Model{}, fmt.Errorf("applying a model: %w", err)
	}

	return s.GetModel(ctx, in.Name)
}

// checkTarget returns the kind of a target's provider, or why it cannot be
// served.
func (s *Service) checkTarget(ctx context.Context, provider, model string) (string, error) {
	if provider == "" || model == "" {
		return "", fmt.Errorf("%w: a target names a provider and a model", ErrInvalid)
	}

	p, err := s.providers(ctx, provider)
	if errors.Is(err, ErrNotFound) {
		return "", fmt.Errorf("%w: no provider %q; apply it first", ErrInvalid, provider)
	}

	if err != nil {
		return "", fmt.Errorf("reading provider %q: %w", provider, err)
	}

	switch {
	case !slices.Contains(servedKinds, p.Kind):
		return "", fmt.Errorf("%w: the gateway does not serve providers of kind %s yet (it serves %s)",
			ErrInvalid, p.Kind, strings.Join(servedKinds, ", "))
	case !slices.Contains(p.Models, model):
		return "", fmt.Errorf("%w: provider %q does not offer %q", ErrInvalid, provider, model)
	}

	return p.Kind, nil
}

// subscriptionKinds are the providers whose accounts are logged in on the control plane: the gateway serves each
// one's models under "<provider>/<model>" without a row in its table (issue #549), as 9router-go lists the models
// of every active connection (internal/handlers/chat/models_list.go) and CLIProxyAPI's registry fills from its
// logged-in accounts (internal/registry/model_registry.go). The table is left for a name made on purpose.
var subscriptionKinds = []string{
	"claude_subscription", "codex_subscription", "antigravity_subscription", openCodeGoKind,
}

// subscriptionModel is the model a subscription offers under "<provider>/<model>", or ok false when the name is
// not one.
func (s *Service) subscriptionModel(ctx context.Context, name string) (Model, bool, error) {
	provider, upstream, found := strings.Cut(name, "/")
	if !found || upstream == "" {
		return Model{}, false, nil
	}

	p, err := s.providers(ctx, provider)
	if errors.Is(err, ErrNotFound) {
		return Model{}, false, nil
	}

	if err != nil {
		return Model{}, false, fmt.Errorf("reading provider %q: %w", provider, err)
	}

	if !slices.Contains(subscriptionKinds, p.Kind) || !slices.Contains(p.Models, upstream) {
		return Model{}, false, nil
	}

	return subscriptionModelOf(p, upstream), true, nil
}

// listingKinds are the subscriptions whose accounts each have their own list of models, which a request is routed by
// (issue #814).
var listingKinds = []string{"codex_subscription", "antigravity_subscription"}

// unlistedModel is the model of a name "<provider>/<model>" of a subscription whose accounts each list their models
// (Codex, Antigravity) when the provider's list, the union of theirs, does not have it, such as one the hourly reading
// took off. It is served as a listed one is, by the accounts' own lists (Subscriptions' ModelTokens): the request is
// answered alike whether or not the model is still published (issue #814). ok is false for another name. A model below
// the provider's version floor is never served, whatever the accounts' lists say: the error is ErrModelNotServed.
// A provider that cannot be read is an error.
func (s *Service) unlistedModel(ctx context.Context, name string) (Model, bool, error) {
	provider, upstream, found := strings.Cut(name, "/")
	if !found || upstream == "" {
		return Model{}, false, nil
	}

	p, err := s.providers(ctx, provider)
	if errors.Is(err, ErrNotFound) {
		return Model{}, false, nil
	}

	if err != nil {
		return Model{}, false, fmt.Errorf("reading provider %q: %w", provider, err)
	}

	if !slices.Contains(listingKinds, p.Kind) || slices.Contains(p.Models, upstream) {
		return Model{}, false, nil
	}

	if p.Admits != nil && !p.Admits(upstream) {
		return Model{}, false, fmt.Errorf("%w: %s is below provider %s's min_versions", ErrModelNotServed, upstream, p.Name)
	}

	return subscriptionModelOf(p, upstream), true, nil
}

// notServedMessage is what the gateway says of a model no account's list has.
func notServedMessage(model string) string {
	return fmt.Sprintf("model %q is not served by any account", model)
}

func subscriptionModelOf(p ProviderInfo, upstream string) Model {
	return Model{
		Name: p.Name + "/" + upstream, Capability: CapabilityChat,
		Targets: []Target{{Provider: p.Name, UpstreamModel: upstream}},
	}
}

// GetModel returns one model with its targets: a row of the gateway's table, or a model of a subscription.
func (s *Service) GetModel(ctx context.Context, name string) (Model, error) {
	q := store.New(s.db)

	row, err := q.GetGatewayModel(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		m, ok, err := s.subscriptionModel(ctx, name)
		if err != nil {
			return Model{}, err
		}

		if ok {
			return m, nil
		}

		return Model{}, fmt.Errorf("%w: model %q", ErrNotFound, name)
	}

	if err != nil {
		return Model{}, fmt.Errorf("reading model %q: %w", name, err)
	}

	targets, err := q.ListGatewayModelTargetsOf(ctx, name)
	if err != nil {
		return Model{}, fmt.Errorf("reading the targets of model %q: %w", name, err)
	}

	return modelFromRow(row, targets), nil
}

// ListModels returns every model, by name: the gateway's table and, when the gateway was given its list of
// providers, every model of every subscription (issue #549).
func (s *Service) ListModels(ctx context.Context) ([]Model, error) {
	out, err := s.tableModels(ctx)
	if err != nil {
		return nil, err
	}

	if s.allProviders == nil {
		return out, nil
	}

	providers, err := s.allProviders(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing providers: %w", err)
	}

	// A name in the table is served from its row (GetModel), so the subscription's own is not listed again.
	inTable := map[string]bool{}
	for _, m := range out {
		inTable[m.Name] = true
	}

	for _, p := range providers {
		if !slices.Contains(subscriptionKinds, p.Kind) {
			continue
		}

		for _, m := range p.Models {
			if sub := subscriptionModelOf(p, m); !inTable[sub.Name] {
				out = append(out, sub)
			}
		}
	}

	slices.SortFunc(out, func(a, b Model) int { return strings.Compare(a.Name, b.Name) })

	return out, nil
}

// WithProviderList gives the gateway the providers it serves, so that it lists every subscription's models.
func (s *Service) WithProviderList(list func(ctx context.Context) ([]ProviderInfo, error)) *Service {
	s.allProviders = list

	return s
}

// WithServedLimits gives the gateway what a subscription's vendor says of each model's window and largest output.
func (s *Service) WithServedLimits(
	served func(ctx context.Context, kind, upstream string) (window, output int64, ok bool),
) *Service {
	s.served = served

	return s
}

// WithServedWire gives the gateway the API a subscription's vendor serves each model on (OpenCode Go, issue #854).
func (s *Service) WithServedWire(wire func(ctx context.Context, kind, upstream string) (string, bool)) *Service {
	s.wire = wire

	return s
}

func (s *Service) tableModels(ctx context.Context) ([]Model, error) {
	q := store.New(s.db)

	rows, err := q.ListGatewayModels(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing models: %w", err)
	}

	targets, err := q.ListGatewayModelTargets(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing targets: %w", err)
	}

	out := make([]Model, 0, len(rows))

	for _, row := range rows {
		own := slices.DeleteFunc(slices.Clone(targets), func(t store.GatewayModelTarget) bool {
			return t.Model != row.Name
		})
		out = append(out, modelFromRow(row, own))
	}

	return out, nil
}

// DeleteModel stops serving a model.
func (s *Service) DeleteModel(ctx context.Context, name string) error {
	return s.write(ctx, func(q *store.Queries, _ int64) error {
		n, err := q.DeleteGatewayModel(ctx, name)
		if err != nil {
			return fmt.Errorf("deleting model %q: %w", name, err)
		}

		if n == 0 {
			return fmt.Errorf("%w: model %q", ErrNotFound, name)
		}

		return nil
	})
}

func modelFromRow(row store.GatewayModel, targets []store.GatewayModelTarget) Model {
	m := Model{
		Name: row.Name, Capability: Capability(row.Capability), Source: row.Source,
		CreatedAt: time.UnixMilli(row.CreatedAt).UTC(), UpdatedAt: time.UnixMilli(row.UpdatedAt).UTC(),
		Targets: make([]Target, 0, len(targets)),
	}

	for _, t := range targets {
		m.Targets = append(m.Targets, Target{Provider: t.Provider, UpstreamModel: t.UpstreamModel})
	}

	// The YAML was checked as it was applied; it is the one home of the
	// attributes.
	var in modelSource
	if yaml.Unmarshal([]byte(row.Source), &in) == nil {
		m.Attributes = in.Attributes
	}

	return m
}

// WithKeyVault connects the vault that keeps the owner's gateway key values.
func (s *Service) WithKeyVault(secrets *vault.Service) *Service {
	s.keyVault = secrets

	return s
}

// AddKey makes an owner key and returns its value, stored encrypted when the vault is configured.
// models lists the public names it may use; empty means every model.
// endpoints lists the kinds it may use besides chat; nil means every kind, and
// an empty list none.
func (s *Service) AddKey(ctx context.Context, name string, models []string, endpoints []Endpoint) (Key, string, error) {
	if !keyNamePattern.MatchString(name) {
		return Key{}, "", fmt.Errorf("%w: a key's name is lowercase letters, digits and dashes", ErrInvalid)
	}

	if err := validateKeyAccess(models, endpoints); err != nil {
		return Key{}, "", err
	}

	if len(models) == 0 {
		models = []string{AllModels}
	}

	return s.addKey(ctx, name, models, keyOwner{endpoints: endpoints, keepValue: true})
}

func validateKeyAccess(models []string, endpoints []Endpoint) error {
	for _, m := range models {
		if m != AllModels && !modelNamePattern.MatchString(m) {
			return fmt.Errorf("%w: %q is not a model name", ErrInvalid, m)
		}
	}

	for _, e := range endpoints {
		if !slices.Contains(Endpoints, e) {
			return fmt.Errorf("%w: %q is not search, fetch, images or audio", ErrInvalid, e)
		}
	}

	return nil
}

func keyEndpointsJSON(endpoints []Endpoint) (sql.NullString, error) {
	if endpoints == nil {
		return sql.NullString{}, nil
	}

	ordered := slices.DeleteFunc(slices.Clone(Endpoints), func(e Endpoint) bool { return !slices.Contains(endpoints, e) })
	encoded, err := json.Marshal(ordered)
	if err != nil {
		return sql.NullString{}, fmt.Errorf("encoding the key's endpoints: %w", err)
	}

	return sql.NullString{String: string(encoded), Valid: true}, nil
}

// AddDispatchKey makes the key a Dispatch's worker calls the gateway with,
// good for one model, and returns its secret. A key made earlier for the
// Dispatch stops working: an offer sent again carries a new one.
func (s *Service) AddDispatchKey(ctx context.Context, dispatchID, model string) (string, error) {
	if dispatchID == "" || !modelNamePattern.MatchString(model) {
		return "", fmt.Errorf("%w: a dispatch's key needs the dispatch and its model", ErrInvalid)
	}

	// The name is only for the owner's list; the dispatch is what finds the key.
	name := strings.ToLower(strings.ReplaceAll(s.newID("dispatch"), "_", "-"))

	_, secret, err := s.addKey(ctx, name, []string{model}, keyOwner{dispatchID: dispatchID})

	return secret, err
}

// AddChatKey makes the key a chat's pi calls the gateway with and returns
// it with its secret (issue #204). botID is the chat's Bot, which its usage
// counts under (issue #626); empty when not known. It is good for model, or for every model
// when model is empty: a Bot that names no model uses pi's own default. A
// key made earlier for the chat stops working: a pi started again gets a
// new one.
func (s *Service) AddChatKey(ctx context.Context, chatID, botID, model string) (Key, string, error) {
	return s.AddChatKeyUnder(ctx, "", chatID, botID, model)
}

// AddChatKeyUnder is AddChatKey for a pi that runs under the coordinator token coordinatorTokenID: revoking that
// token ends the key (issue #996), as it ends the pi's Bot token.
func (s *Service) AddChatKeyUnder(ctx context.Context, coordinatorTokenID, chatID, botID, model string,
) (Key, string, error) {
	if chatID == "" || (model != "" && !modelNamePattern.MatchString(model)) {
		return Key{}, "", fmt.Errorf("%w: a chat's key needs the chat, and a model name when it names one", ErrInvalid)
	}

	models := []string{AllModels}
	if model != "" {
		models = []string{model}
	}

	// The name is only for the owner's list; the chat is what finds the key.
	name := strings.ToLower(strings.ReplaceAll(s.newID("chat"), "_", "-"))

	return s.addKey(ctx, name, models, keyOwner{chatID: chatID, botID: botID, coordinatorTokenID: coordinatorTokenID})
}

// RevokeAllChatKeys stops the key of every chat from working. The control
// plane calls it as it starts: no pi outlives it, so none is left to use one.
func (s *Service) RevokeAllChatKeys(ctx context.Context) error {
	return s.write(ctx, func(q *store.Queries, now int64) error {
		if _, err := q.RevokeGatewayChatKeys(ctx, store.RevokeGatewayChatKeysParams{
			Now: at(now), Reason: reasonOf(bearertoken.RevokedRestarted),
		}); err != nil {
			return fmt.Errorf("revoking the keys of chats: %w", err)
		}

		return nil
	})
}

// RevokeDeletedChatKeysTx revokes the keys of a chat being deleted, in the
// transaction that deletes it (issue #405): the gateway's table is written
// only here, and the chat and its keys end in one commit, as Omnara's
// orglifecycle has secretstore's CreateTx and executionstore's …InUnit write
// their own tables in its transaction and Coder removes a deleted user's
// api_keys in the transaction that marks it. A key already revoked keeps its
// time; every key loses the chat, as the chat's Runs do (issue #484). It
// begins no transaction: the caller's holds the write lock.
func RevokeDeletedChatKeysTx(ctx context.Context, tx *sql.Tx, chatID string, now int64) error {
	if err := store.New(tx).RevokeGatewayKeysOfDeletedChat(ctx, store.RevokeGatewayKeysOfDeletedChatParams{
		Now: at(now), Reason: reasonOf(bearertoken.RevokedChatDeleted), ChatID: chatID,
	}); err != nil {
		return fmt.Errorf("revoking the keys of chat %s: %w", chatID, err)
	}

	return nil
}

// Revocations revoke the gateway's keys in the transaction of what ends them, for orchestration, which owns that
// transaction (issue #996): a revoked coordinator token, a removed node, a deleted chat or Bot ends its keys in the
// commit that ends it, as RevokeDeletedChatKeysTx does for a chat (issue #405). The gateway's table is still written
// only by its own queries. Each begins no transaction: the caller's holds the write lock.
type Revocations struct{}

// RevokeDeletedChatKeysTx is RevokeDeletedChatKeysTx.
func (Revocations) RevokeDeletedChatKeysTx(ctx context.Context, tx *sql.Tx, chatID string, now int64) error {
	return RevokeDeletedChatKeysTx(ctx, tx, chatID, now)
}

// RevokeCoordinatorKeysTx ends the keys of a coordinator token being revoked: those of the chats whose pi runs under
// it, and those of the Dispatches of the Runs it created, as Orca revokes a device's children with it
// (runtime-rpc-mobile-devices.ts).
func (Revocations) RevokeCoordinatorKeysTx(ctx context.Context, tx *sql.Tx, tokenID string, now int64) error {
	if err := store.New(tx).RevokeGatewayKeysOfCoordinatorToken(ctx, store.RevokeGatewayKeysOfCoordinatorTokenParams{
		Now: at(now), Reason: reasonOf(bearertoken.RevokedParentRevoked), TokenID: tokenID,
	}); err != nil {
		return fmt.Errorf("revoking the keys under token %s: %w", tokenID, err)
	}

	return nil
}

// RevokeNodeKeysTx ends the keys of the Dispatches of a node being removed, as Omnara revokes a machine's daemon
// tokens with it (executionstore/machine_lifecycle_store.go).
func (Revocations) RevokeNodeKeysTx(ctx context.Context, tx *sql.Tx, nodeID string, now int64) error {
	if err := store.New(tx).RevokeGatewayKeysOfNode(ctx, store.RevokeGatewayKeysOfNodeParams{
		Now: at(now), Reason: reasonOf(bearertoken.RevokedNodeRemoved), NodeID: nodeID,
	}); err != nil {
		return fmt.Errorf("revoking the keys of node %s: %w", nodeID, err)
	}

	return nil
}

// RevokeBotKeysTx ends the keys of a Bot being deleted; they are kept for the usage they name (issue #626).
func (Revocations) RevokeBotKeysTx(ctx context.Context, tx *sql.Tx, botID string, now int64) error {
	if err := store.New(tx).RevokeGatewayKeysOfBot(ctx, store.RevokeGatewayKeysOfBotParams{
		Now: at(now), Reason: reasonOf(bearertoken.RevokedBotDeleted), BotID: botID,
	}); err != nil {
		return fmt.Errorf("revoking the keys of bot %s: %w", botID, err)
	}

	return nil
}

// RevokeDispatchKeys stops every key of a Dispatch that settled from working; none is no error.
func (s *Service) RevokeDispatchKeys(ctx context.Context, dispatchID string) error {
	return s.write(ctx, func(q *store.Queries, now int64) error {
		_, err := q.RevokeGatewayKeysOfDispatch(ctx, store.RevokeGatewayKeysOfDispatchParams{
			Now: at(now), Reason: reasonOf(bearertoken.RevokedDispatchEnded),
			DispatchID: sql.NullString{String: dispatchID, Valid: true},
		})
		if err != nil {
			return fmt.Errorf("revoking the keys of dispatch %s: %w", dispatchID, err)
		}

		return nil
	})
}

// DispatchesWithKeys lists the Dispatches that have a key that still works.
func (s *Service) DispatchesWithKeys(ctx context.Context) ([]string, error) {
	rows, err := store.New(s.db).ListDispatchesWithGatewayKeys(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing the dispatches with keys: %w", err)
	}

	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.String)
	}

	return out, nil
}

// keyOwner is what a key was made for, when it is not the owner's own: one
// Dispatch or one chat. Its earlier keys end as the new one is made.
type keyOwner struct {
	dispatchID string
	chatID     string
	// botID is the Bot of the chat, kept on the key so that usage is told by Bot after the chat is gone (#626).
	botID string
	// endpoints are the kinds an owner's key may use besides chat; nil is every kind.
	endpoints []Endpoint
	keepValue bool
	// coordinatorTokenID is the coordinator token a chat's pi runs under: revoking it ends the key (issue #996).
	coordinatorTokenID string
}

func (s *Service) addKey(ctx context.Context, name string, models []string, owner keyOwner) (Key, string, error) {
	secret, err := bearertoken.Generate(bearertoken.Gateway)
	if err != nil {
		return Key{}, "", err
	}

	encoded, err := json.Marshal(models)
	if err != nil {
		return Key{}, "", fmt.Errorf("encoding the key's models: %w", err)
	}

	kinds, err := keyEndpointsJSON(owner.endpoints)
	if err != nil {
		return Key{}, "", err
	}

	id := s.newID("gwk")

	err = s.writeTx(ctx, func(tx *sql.Tx, now int64) error {
		q := store.New(tx)
		var secretVersionID sql.NullString
		var maskedValue string

		if owner.keepValue && s.keyVault != nil {
			versionID, err := s.keyVault.CreateTx(ctx, tx, vault.NewSecret{
				Name: strings.ReplaceAll(id, "_", "-"), Kind: vault.KindGeneric,
				Payload: vault.Payload{vault.KeyValue: secret},
			}, now)
			switch {
			case err == nil:
				secretVersionID = sql.NullString{String: versionID, Valid: true}
				maskedValue = secret[:6] + strings.Repeat("•", len(secret)-10) + secret[len(secret)-4:]
			case errors.Is(err, vault.ErrNotConfigured):
			default:
				return keyVaultError(err)
			}
		}

		if owner.dispatchID != "" {
			if _, err := q.RevokeGatewayKeysOfDispatch(ctx, store.RevokeGatewayKeysOfDispatchParams{
				Now: at(now), Reason: reasonOf(bearertoken.RevokedReplaced),
				DispatchID: sql.NullString{String: owner.dispatchID, Valid: true},
			}); err != nil {
				return fmt.Errorf("revoking the earlier keys of dispatch %s: %w", owner.dispatchID, err)
			}
		}

		if owner.chatID != "" {
			if _, err := q.RevokeGatewayKeysOfChat(ctx, store.RevokeGatewayKeysOfChatParams{
				Now: at(now), Reason: reasonOf(bearertoken.RevokedReplaced),
				ChatID: sql.NullString{String: owner.chatID, Valid: true},
			}); err != nil {
				return fmt.Errorf("revoking the earlier keys of chat %s: %w", owner.chatID, err)
			}
		}

		if err := q.InsertGatewayKey(ctx, store.InsertGatewayKeyParams{
			ID: id, Name: name, KeyHash: bearertoken.Hash(secret), KeyPrefix: secret[:keyPrefixLength],
			Models: string(encoded), Endpoints: kinds,
			DispatchID: sql.NullString{String: owner.dispatchID, Valid: owner.dispatchID != ""},
			ChatID:     sql.NullString{String: owner.chatID, Valid: owner.chatID != ""},
			BotID:      sql.NullString{String: owner.botID, Valid: owner.botID != ""}, Now: now,
			SecretVersionID: secretVersionID, MaskedValue: maskedValue,
			CoordinatorTokenID: sql.NullString{String: owner.coordinatorTokenID, Valid: owner.coordinatorTokenID != ""},
		}); err != nil {
			return fmt.Errorf("%w: storing key %q: %w", ErrConflict, name, err)
		}

		return nil
	})
	if err != nil {
		return Key{}, "", fmt.Errorf("adding a key: %w", err)
	}

	k, err := s.getKey(ctx, id)

	return k, secret, err
}

func (s *Service) getKey(ctx context.Context, id string) (Key, error) {
	row, err := store.New(s.db).GetGatewayKey(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Key{}, fmt.Errorf("%w: key %s", ErrNotFound, id)
	}

	if err != nil {
		return Key{}, fmt.Errorf("reading key %s: %w", id, err)
	}

	return keyFromRow(row)
}

// ListKeys returns every key, revoked ones included, by name.
func (s *Service) ListKeys(ctx context.Context) ([]Key, error) {
	q := store.New(s.db)

	rows, err := q.ListGatewayKeys(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing keys: %w", err)
	}

	used, err := q.GatewayKeysLastUsed(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading when keys were last used: %w", err)
	}

	lastUsed := make(map[string]time.Time, len(used))
	for _, u := range used {
		lastUsed[u.KeyID] = time.UnixMilli(u.LastUsed).UTC()
	}

	out := make([]Key, 0, len(rows))

	for _, r := range rows {
		k, err := keyFromRow(r)
		if err != nil {
			return nil, err
		}

		if t, ok := lastUsed[k.ID]; ok {
			k.usedLater(t)
		}

		out = append(out, k)
	}

	return out, nil
}

// RevokeKey stops a key from working, as the owner revokes it. Revoking twice is a conflict.
func (s *Service) RevokeKey(ctx context.Context, id string) error {
	return s.RevokeKeyFor(ctx, id, bearertoken.RevokedByOwner)
}

// RevokeKeyFor is RevokeKey with the reason the key stops (issue #996), such as its pi's end.
func (s *Service) RevokeKeyFor(ctx context.Context, id string, reason bearertoken.RevokeReason) error {
	return s.writeTx(ctx, func(tx *sql.Tx, now int64) error {
		q := store.New(tx)
		row, err := q.GetGatewayKey(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: key %s", ErrNotFound, id)
		}

		if err != nil {
			return fmt.Errorf("reading key %s: %w", id, err)
		}

		if row.RevokedAt.Valid {
			return fmt.Errorf("%w: key %s is already revoked", ErrConflict, id)
		}

		if _, err := q.RevokeGatewayKey(ctx, store.RevokeGatewayKeyParams{
			Now: at(now), Reason: reasonOf(reason), ID: id,
		}); err != nil {
			return fmt.Errorf("revoking key %s: %w", id, err)
		}

		if row.SecretVersionID.Valid {
			return vault.DeleteByVersionTx(ctx, tx, row.SecretVersionID.String)
		}

		return nil
	})
}

// Authenticate returns the key a secret belongs to. A malformed, unknown or
// revoked secret is ErrUnauthorized; the form is checked before the database
// is read, as the client API's tokens are.
func (s *Service) Authenticate(ctx context.Context, secret string) (Key, error) {
	if kind, err := bearertoken.Parse(secret); err != nil || kind != bearertoken.Gateway {
		return Key{}, fmt.Errorf("%w: not a gateway key", ErrUnauthorized)
	}

	row, err := store.New(s.db).GetGatewayKeyByHash(ctx, bearertoken.Hash(secret))
	if errors.Is(err, sql.ErrNoRows) {
		return Key{}, ErrUnauthorized
	}

	if err != nil {
		return Key{}, fmt.Errorf("authenticating a key: %w", err)
	}

	if row.RevokedAt.Valid {
		return Key{}, fmt.Errorf("%w: key %s is revoked", ErrUnauthorized, row.ID)
	}

	before := s.clock.Now().Add(-bearertoken.TouchEvery).UnixMilli()
	if !row.LastUsedAt.Valid || row.LastUsedAt.Int64 <= before {
		err := s.write(ctx, func(q *store.Queries, now int64) error {
			return q.TouchGatewayKey(ctx, store.TouchGatewayKeyParams{Now: now, ID: row.ID, Before: before})
		})
		if err != nil {
			return Key{}, fmt.Errorf("recording the use of key %s: %w", row.ID, err)
		}
	}

	return keyFromRow(row)
}

// ModelsFor returns the models a key may use, by name: what GET /v1/models
// answers.
func (s *Service) ModelsFor(ctx context.Context, k Key) ([]Model, error) {
	all, err := s.ListModels(ctx)
	if err != nil {
		return nil, err
	}

	return slices.DeleteFunc(all, func(m Model) bool { return !k.Allows(m.Name) }), nil
}

func keyFromRow(r store.GatewayKey) (Key, error) {
	var models []string
	if err := json.Unmarshal([]byte(r.Models), &models); err != nil {
		return Key{}, fmt.Errorf("decoding the models of key %s: %w", r.ID, err)
	}

	k := Key{
		ID: r.ID, Name: r.Name, Models: models, DispatchID: r.DispatchID.String, ChatID: r.ChatID.String,
		botID: r.BotID.String, MaskedValue: r.MaskedValue,
		Prefix: r.KeyPrefix, CreatedAt: time.UnixMilli(r.CreatedAt).UTC(),
	}

	if r.Endpoints.Valid {
		k.Endpoints = []Endpoint{}
		if err := json.Unmarshal([]byte(r.Endpoints.String), &k.Endpoints); err != nil {
			return Key{}, fmt.Errorf("decoding the endpoints of key %s: %w", r.ID, err)
		}
	}

	if r.RevokedAt.Valid {
		t := time.UnixMilli(r.RevokedAt.Int64).UTC()
		k.RevokedAt = &t
		k.RevokeReason = bearertoken.RevokeReason(r.RevokeReason.String)
	}

	if r.LastUsedAt.Valid {
		t := time.UnixMilli(r.LastUsedAt.Int64).UTC()
		k.LastUsedAt = &t
	}

	return k, nil
}

// usedLater sets a key's last use to the later of its touch and its newest request.
func (k *Key) usedLater(t time.Time) {
	if k.LastUsedAt == nil || t.After(*k.LastUsedAt) {
		k.LastUsedAt = &t
	}
}

func at(now int64) sql.NullInt64 { return sql.NullInt64{Int64: now, Valid: true} }

func reasonOf(r bearertoken.RevokeReason) sql.NullString {
	return sql.NullString{String: string(r), Valid: true}
}
