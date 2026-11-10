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
	Models    []string
	CreatedAt time.Time
	RevokedAt *time.Time
}

// Allows reports whether the key may use a model.
func (k Key) Allows(model string) bool {
	return slices.Contains(k.Models, AllModels) || slices.Contains(k.Models, model)
}

// ProviderInfo is what the gateway needs to know of a provider.
type ProviderInfo struct {
	Name      string
	Kind      string
	BaseURL   string
	APIKeyRef string
	// Models are the names the provider offers.
	Models []string
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
}

// New returns a Service over an opened, migrated database. providers looks
// providers up and secrets gives the key a provider's api_key_ref names.
func New(
	db *sql.DB, clock platform.Clock, newID func(prefix string) string, providers Providers, secrets Secrets,
) *Service {
	return &Service{db: db, clock: clock, newID: newID, providers: providers, secrets: secrets}
}

func (s *Service) write(ctx context.Context, fn func(q *store.Queries, now int64) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning a transaction: %w", err)
	}

	if err := fn(store.New(tx), s.clock.Now().UnixMilli()); err != nil {
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
}

// ApplyModel stores a model's YAML: its public name, what it does and where
// it is served from. Applying a name again replaces the model. Every target
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
		if err := s.checkTarget(ctx, t.Provider, t.Model); err != nil {
			return Model{}, fmt.Errorf("target %d: %w", i+1, err)
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

func (s *Service) checkTarget(ctx context.Context, provider, model string) error {
	if provider == "" || model == "" {
		return fmt.Errorf("%w: a target names a provider and a model", ErrInvalid)
	}

	p, err := s.providers(ctx, provider)
	if errors.Is(err, ErrNotFound) {
		return fmt.Errorf("%w: no provider %q; apply it first", ErrInvalid, provider)
	}

	if err != nil {
		return fmt.Errorf("reading provider %q: %w", provider, err)
	}

	switch {
	case !slices.Contains(servedKinds, p.Kind):
		return fmt.Errorf("%w: the gateway does not serve providers of kind %s yet (it serves %s)",
			ErrInvalid, p.Kind, strings.Join(servedKinds, ", "))
	case !slices.Contains(p.Models, model):
		return fmt.Errorf("%w: provider %q does not offer %q", ErrInvalid, provider, model)
	}

	return nil
}

// GetModel returns one model with its targets.
func (s *Service) GetModel(ctx context.Context, name string) (Model, error) {
	q := store.New(s.db)

	row, err := q.GetGatewayModel(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
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

// ListModels returns every model, by name.
func (s *Service) ListModels(ctx context.Context) ([]Model, error) {
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

	return m
}

// AddKey makes a gateway key and returns it with its secret, shown only
// here. models lists the public names it may use; empty means every model.
func (s *Service) AddKey(ctx context.Context, name string, models []string) (Key, string, error) {
	if !keyNamePattern.MatchString(name) {
		return Key{}, "", fmt.Errorf("%w: a key's name is lowercase letters, digits and dashes", ErrInvalid)
	}

	if len(models) == 0 {
		models = []string{AllModels}
	}

	for _, m := range models {
		if m != AllModels && !modelNamePattern.MatchString(m) {
			return Key{}, "", fmt.Errorf("%w: %q is not a model name", ErrInvalid, m)
		}
	}

	secret, err := bearertoken.Generate(bearertoken.Gateway)
	if err != nil {
		return Key{}, "", err
	}

	encoded, err := json.Marshal(models)
	if err != nil {
		return Key{}, "", fmt.Errorf("encoding the key's models: %w", err)
	}

	id := s.newID("gwk")

	err = s.write(ctx, func(q *store.Queries, now int64) error {
		if err := q.InsertGatewayKey(ctx, store.InsertGatewayKeyParams{
			ID: id, Name: name, KeyHash: bearertoken.Hash(secret), Models: string(encoded), Now: now,
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
	rows, err := store.New(s.db).ListGatewayKeys(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing keys: %w", err)
	}

	out := make([]Key, 0, len(rows))

	for _, r := range rows {
		k, err := keyFromRow(r)
		if err != nil {
			return nil, err
		}

		out = append(out, k)
	}

	return out, nil
}

// RevokeKey stops a key from working. Revoking twice is a conflict.
func (s *Service) RevokeKey(ctx context.Context, id string) error {
	return s.write(ctx, func(q *store.Queries, now int64) error {
		n, err := q.RevokeGatewayKey(ctx, store.RevokeGatewayKeyParams{
			Now: sql.NullInt64{Int64: now, Valid: true}, ID: id,
		})
		if err != nil {
			return fmt.Errorf("revoking key %s: %w", id, err)
		}

		if n == 1 {
			return nil
		}

		if _, err := q.GetGatewayKey(ctx, id); errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: key %s", ErrNotFound, id)
		}

		return fmt.Errorf("%w: key %s is already revoked", ErrConflict, id)
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

	k := Key{ID: r.ID, Name: r.Name, Models: models, CreatedAt: time.UnixMilli(r.CreatedAt).UTC()}

	if r.RevokedAt.Valid {
		t := time.UnixMilli(r.RevokedAt.Int64).UTC()
		k.RevokedAt = &t
	}

	return k, nil
}
