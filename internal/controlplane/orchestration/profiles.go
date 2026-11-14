package orchestration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/storeutil"
)

// Profiles (issue #42), after Omnara's agent configs and profiles: a profile
// names the runtime, provider, model, effort and instruction an agent runs
// with. Applying a changed source makes a new immutable version; the name
// points at the latest. Providers and the models they offer are their own
// records, so a profile changes nothing when a provider moves (9router today,
// the model gateway later).

// DefaultProfile is the profile of a Task that names none.
const DefaultProfile = "implementer"

// MaxDescription bounds a profile's description.
const MaxDescription = 500

// MaxFallbacks bounds a profile's backups, as OpenMausBot keeps five for a
// bot (src/components/bot-panel/BackupAis.tsx, MAX_BACKUPS).
const MaxFallbacks = 5

// Provider kinds.
const (
	ProviderAnthropic          = "anthropic"
	ProviderOpenAICompatible   = "openai_compatible"
	ProviderClaudeSubscription = "claude_subscription"
	ProviderCodexSubscription  = "codex_subscription"
	// ProviderAntigravitySubscription is served by the model gateway only: no
	// worker's CLI runs under an Antigravity account.
	ProviderAntigravitySubscription = "antigravity_subscription"
	// ProviderModelGateway is the control plane's own model gateway: a worker
	// under it calls the gateway with a key made for its Dispatch, and which
	// account or API serves the model is the gateway's business (issue #107).
	// It has no base URL and no key of its own.
	ProviderModelGateway = "model_gateway"
)

// GatewayAccountRef stands where an account's id does for a Conversation
// whose model the gateway serves: no account on the node runs it.
const GatewayAccountRef = "gateway"

// runtimeProviders lists, per runtime, the provider kinds it can run against.
// A Runtime is code on the node; a profile only names one.
var runtimeProviders = map[string][]string{
	// Claude Code runs only through the gateway's /v1/messages, with its Dispatch's key: the subscription token
	// never leaves the control plane (issue #381).
	"claude": {ProviderModelGateway},
	"codex":  {ProviderCodexSubscription, ProviderOpenAICompatible, ProviderModelGateway},
	// pi reaches its model through the gateway as omp and opencode do (issue #204).
	"pi": {ProviderAnthropic, ProviderOpenAICompatible, ProviderModelGateway},
	// omp and opencode are workers whose model the gateway serves (issue #107).
	"omp":      {ProviderModelGateway},
	"opencode": {ProviderModelGateway},
}

// gatewayOnly reports whether a worker of the runtime runs only through the
// model gateway, never under an account a node holds: Claude Code (issue #381).
// omp and opencode name only the gateway too; their placement stays as it
// was, since issue #381 moved Claude Code only.
func gatewayOnly(runtime string) bool { return runtime == TokenRuntime }

// needsGatewayMessage says why a Claude Code worker cannot run elsewhere.
const needsGatewayMessage = "a Claude Code worker runs through the model gateway; its profile must name a " +
	"provider of kind " + ProviderModelGateway

// Efforts lists the reasoning efforts a profile or a Task may name.
var Efforts = []string{"low", "medium", "high", "xhigh", "max"}

var (
	namePattern   = storeutil.NamePattern
	keyRefPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_./:-]{0,99}$`)
)

// Profile is the current version of a named profile, or one version of it.
type Profile struct {
	Name string
	// Description says what the profile is for, so a coordinator chooses by it.
	Description string
	VersionID   string
	Version     int
	Runtime     string
	Provider    string
	Model       string
	Effort      string
	Instruction string
	// Fallbacks names, in the owner's order, the profiles a Task of this one
	// runs under while every account of its runtime waits for a limit to
	// reset (issue #171). Empty: the Task waits.
	Fallbacks []string
	// Skills are the skills the profile's workers get beside those given to
	// everyone; SkillsOff the ones given to everyone they do not get
	// (issue #21).
	Skills    []string
	SkillsOff []string
	// Source is the YAML as applied.
	Source    string
	CreatedAt time.Time
	// Availability says whether a worker can start under the profile now. It
	// is set on a profile's current version only.
	Availability *Availability
}

// Availability is whether a worker can start now, and when not, why: no
// connected, active node has the CLI (ReasonNoNode), the runtime has no
// active account on a node (ReasonNoAccount), every one cools down
// (ReasonAccountsCooling, with the time the first is free), or a Claude Code
// worker would run without the model gateway (ReasonNeedsGateway).
type Availability struct {
	Available bool
	Reason    string
	At        *time.Time
}

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

type profileSource struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Runtime     string `yaml:"runtime"`
	Model       *struct {
		Provider string `yaml:"provider"`
		Name     string `yaml:"name"`
		Effort   string `yaml:"effort"`
	} `yaml:"model"`
	Instruction string   `yaml:"instruction"`
	Fallbacks   []string `yaml:"fallbacks"`
	Skills      []string `yaml:"skills"`
	SkillsOff   []string `yaml:"skills_off"`
}

type providerSource struct {
	Name      string   `yaml:"name"`
	Kind      string   `yaml:"kind"`
	BaseURL   string   `yaml:"base_url"`
	APIKeyRef string   `yaml:"api_key_ref"`
	Models    []string `yaml:"models"`
}

// decodeStrict reads one YAML document, refusing unknown fields.
func decodeStrict(source string, v any) error {
	dec := yaml.NewDecoder(strings.NewReader(source))
	dec.KnownFields(true)

	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w: not a valid document: %w", ErrInvalid, err)
	}

	return nil
}

// sourceDigest is the SHA-256 of a source with line endings normalized.
func sourceDigest(source string) string {
	sum := sha256.Sum256(bytes.ReplaceAll([]byte(source), []byte("\r\n"), []byte("\n")))

	return hex.EncodeToString(sum[:])
}

// ApplyProfile stores a profile's YAML. A source that differs from the
// current version makes a new version and points the name at it; the same
// source returns the current version. created reports a new version. A
// non-empty name must equal the source's.
func (s *Service) ApplyProfile(ctx context.Context, name, source string) (Profile, bool, error) {
	return s.applyProfile(ctx, "", name, source)
}

// applyProfile is ApplyProfile; with a chat, a new version is the Bot
// coordinator's from that chat and is recorded with what undoes it
// (setupchanges.go).
func (s *Service) applyProfile(ctx context.Context, chat, name, source string) (Profile, bool, error) {
	var in profileSource
	if err := decodeStrict(source, &in); err != nil {
		return Profile{}, false, err
	}

	p := Profile{
		Name: in.Name, Description: strings.TrimSpace(in.Description), Runtime: in.Runtime,
		Instruction: in.Instruction, Fallbacks: in.Fallbacks, Skills: in.Skills, SkillsOff: in.SkillsOff, Source: source,
	}
	if in.Model != nil {
		p.Provider, p.Model, p.Effort = in.Model.Provider, in.Model.Name, in.Model.Effort
	}

	if !namePattern.MatchString(p.Name) {
		return Profile{}, false, fmt.Errorf("%w: a profile name is lowercase letters, digits and dashes", ErrInvalid)
	}

	// pi is run by the control plane for a Bot; no node has it and no Task is placed under it (issue #169).
	if p.Runtime == botRuntime {
		return Profile{}, false, fmt.Errorf("%w: runtime %s is a bot's, not a worker's; see `hypomnema bots`",
			ErrInvalid, botRuntime)
	}

	if name != "" && name != p.Name {
		return Profile{}, false, fmt.Errorf("%w: the source names profile %q, not %q", ErrInvalid, p.Name, name)
	}

	if p.Description == "" || len(p.Description) > MaxDescription {
		return Profile{}, false, fmt.Errorf("%w: a profile needs a description of at most %d characters: what it is for",
			ErrInvalid, MaxDescription)
	}

	var (
		out     Profile
		created bool
	)

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		if err := changeFromChat(ctx, q, chat); err != nil {
			return err
		}

		provider, err := checkModel(ctx, q, p.Runtime, p.Provider, p.Model, p.Effort)
		if err != nil {
			return fmt.Errorf("profile %s: %w", p.Name, err)
		}

		if err := checkFallbacks(ctx, q, p); err != nil {
			return fmt.Errorf("profile %s: %w", p.Name, err)
		}

		if err := checkSkills(ctx, q, p.Skills, p.SkillsOff); err != nil {
			return fmt.Errorf("profile %s: %w", p.Name, err)
		}

		p.Provider = provider
		digest := sourceDigest(source)

		current, err := q.CurrentProfileVersion(ctx, p.Name)

		switch {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return fmt.Errorf("reading profile %s: %w", p.Name, err)
		case current.Digest == digest:
			out = profileFromRow(current)

			return nil
		}

		id := s.newID("pfv")

		// The profile row comes first: its versions reference it.
		if current.ID == "" {
			err = q.InsertProfile(ctx, store.InsertProfileParams{Name: p.Name, VersionID: id, Now: now})
		} else {
			err = q.SetProfileVersion(ctx, store.SetProfileVersionParams{Name: p.Name, VersionID: id, Now: now})
		}

		if err != nil {
			return fmt.Errorf("pointing profile %s at its new version: %w", p.Name, err)
		}

		if err := q.InsertProfileVersion(ctx, store.InsertProfileVersionParams{
			ID: id, Profile: p.Name, Version: current.Version + 1, Source: source, Digest: digest, Description: p.Description,
			Runtime: p.Runtime, Provider: nullString(p.Provider), Model: nullString(p.Model),
			Effort: nullString(p.Effort), Instruction: nullString(p.Instruction),
			Fallbacks: strings.Join(p.Fallbacks, ","), Skills: strings.Join(p.Skills, ","),
			SkillsOff: strings.Join(p.SkillsOff, ","), Now: now,
		}); err != nil {
			return fmt.Errorf("inserting a version of profile %s: %w", p.Name, err)
		}

		row, err := q.GetProfileVersion(ctx, id)
		if err != nil {
			return fmt.Errorf("reading the new version of profile %s: %w", p.Name, err)
		}

		out, created = profileFromRow(row), true

		switch {
		case chat == "":
			return nil
		case current.ID == "":
			return s.recordChange(ctx, q, now, chat, changeProfile, p.Name, actionMade, nil, id)
		default:
			return s.recordChange(ctx, q, now, chat, changeProfile, p.Name, actionChanged, current.ID, id)
		}
	})
	if err != nil {
		return Profile{}, false, fmt.Errorf("applying a profile: %w", err)
	}

	return out, created, nil
}

// checkFallbacks checks the backups a profile names: each an existing
// worker's profile, named once, and not the profile itself.
func checkFallbacks(ctx context.Context, q *store.Queries, p Profile) error {
	if len(p.Fallbacks) > MaxFallbacks {
		return fmt.Errorf("%w: a profile names at most %d fallbacks", ErrInvalid, MaxFallbacks)
	}

	for i, name := range p.Fallbacks {
		switch {
		case name == p.Name:
			return fmt.Errorf("%w: a profile is not its own fallback", ErrInvalid)
		case slices.Contains(p.Fallbacks[:i], name):
			return fmt.Errorf("%w: fallback %q is named twice", ErrInvalid, name)
		}

		if _, err := q.CurrentProfileVersion(ctx, name); err != nil {
			return fmt.Errorf("fallback: %w", notFound(err, "profile", name))
		}
	}

	return nil
}

// checkModel checks a runtime, provider, model and effort together and
// returns the provider, found from the model when none is named: the runtime
// must be known, the provider able to serve it, and the model offered by it.
func checkModel(ctx context.Context, q *store.Queries, runtime, provider, model, effort string) (string, error) {
	kinds, ok := runtimeProviders[runtime]
	if !ok {
		return "", fmt.Errorf("%w: unknown runtime %q; known: %s", ErrInvalid, runtime, knownRuntimes())
	}

	if effort != "" && !slices.Contains(Efforts, effort) {
		return "", fmt.Errorf("%w: unknown effort %q; known: %s", ErrInvalid, effort, strings.Join(Efforts, ", "))
	}

	if provider != "" {
		row, err := q.GetProvider(ctx, provider)
		if err != nil {
			return "", notFound(err, "provider", provider)
		}

		switch {
		case !slices.Contains(kinds, row.Kind) && gatewayOnly(runtime):
			return "", fmt.Errorf("%w: provider %s is of kind %s: %s", ErrInvalid, provider, row.Kind,
				needsGatewayMessage)
		case !slices.Contains(kinds, row.Kind):
			return "", fmt.Errorf("%w: runtime %s cannot run against provider %s (%s)", ErrInvalid, runtime, provider,
				row.Kind)
		}
	}

	if model == "" {
		return provider, nil
	}

	offers, err := q.ModelsNamed(ctx, model)
	if err != nil {
		return "", fmt.Errorf("reading the providers of model %s: %w", model, err)
	}

	var fits []string

	for _, m := range offers {
		if slices.Contains(kinds, m.Kind) && (provider == "" || m.Provider == provider) {
			fits = append(fits, m.Provider)
		}
	}

	switch {
	case len(fits) == 0 && provider != "":
		return "", fmt.Errorf("%w: provider %s does not offer model %q", ErrInvalid, provider, model)
	case len(fits) == 0 && gatewayOnly(runtime):
		return "", fmt.Errorf("%w: no provider of kind %s offers model %q; %s", ErrInvalid, ProviderModelGateway,
			model, needsGatewayMessage)
	case len(fits) == 0:
		return "", fmt.Errorf("%w: no provider runtime %s can use offers model %q", ErrInvalid, runtime, model)
	case len(fits) > 1:
		return "", fmt.Errorf("%w: model %q is offered by %s; name the provider", ErrInvalid, model,
			strings.Join(fits, ", "))
	default:
		return fits[0], nil
	}
}

func knownRuntimes() string {
	names := make([]string, 0, len(runtimeProviders))
	for r := range runtimeProviders {
		names = append(names, r)
	}

	slices.Sort(names)

	return strings.Join(names, ", ")
}

// GetProfile returns a profile's current version.
func (s *Service) GetProfile(ctx context.Context, name string) (Profile, error) {
	row, err := s.read().CurrentProfileVersion(ctx, name)
	if err != nil {
		return Profile{}, notFound(err, "profile", name)
	}

	out := []Profile{profileFromRow(row)}
	if err := s.withAvailability(ctx, out); err != nil {
		return Profile{}, err
	}

	return out[0], nil
}

// withAvailability says of each profile whether a worker can start under it
// now, from the scheduler's own view of the nodes and accounts (Omnigent's
// agent list carries available_on_host and unavailable_reason the same way).
func (s *Service) withAvailability(ctx context.Context, profiles []Profile) error {
	q := s.read()

	p, err := loadPlacement(ctx, q, s.clock.Now().UnixMilli())
	if err != nil {
		return err
	}

	for i, pr := range profiles {
		gateway := false

		if pr.Provider != "" {
			row, err := q.GetProvider(ctx, pr.Provider)
			if err != nil {
				return notFound(err, "provider", pr.Provider)
			}

			gateway = row.Kind == ProviderModelGateway
		}

		a := p.availability(pr.Runtime, gateway)
		profiles[i].Availability = &a
	}

	return nil
}

// ListProfiles returns every profile's current version, by name.
func (s *Service) ListProfiles(ctx context.Context) ([]Profile, error) {
	rows, err := s.read().ListCurrentProfileVersions(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing profiles: %w", err)
	}

	out := make([]Profile, 0, len(rows))
	for _, r := range rows {
		out = append(out, profileFromRow(r))
	}

	if err := s.withAvailability(ctx, out); err != nil {
		return nil, err
	}

	return out, nil
}

// ListProfileVersions returns a profile's versions, newest first.
func (s *Service) ListProfileVersions(ctx context.Context, name string) ([]Profile, error) {
	if _, err := s.GetProfile(ctx, name); err != nil {
		return nil, err
	}

	rows, err := s.read().ListProfileVersions(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("listing the versions of profile %s: %w", name, err)
	}

	out := make([]Profile, 0, len(rows))
	for _, r := range rows {
		out = append(out, profileFromRow(r))
	}

	return out, nil
}

func profileFromRow(v store.ProfileVersion) Profile {
	return Profile{
		Name: v.Profile, Description: v.Description, VersionID: v.ID, Version: int(v.Version), Runtime: v.Runtime,
		Provider: v.Provider.String,
		Model:    v.Model.String, Effort: v.Effort.String, Instruction: v.Instruction.String, Source: v.Source,
		Fallbacks: splitNames(v.Fallbacks), Skills: splitNames(v.Skills), SkillsOff: splitNames(v.SkillsOff),
		CreatedAt: millis(v.CreatedAt),
	}
}

// splitNames reads names stored in order, separated by commas.
func splitNames(stored string) []string {
	if stored == "" {
		return nil
	}

	return strings.Split(stored, ",")
}

// ApplyProvider stores a provider's YAML and the models it offers. It refuses
// a change that would break a profile's current version: a model it uses
// removed, or a kind its runtime cannot use. A non-empty name must equal the
// source's.
func (s *Service) ApplyProvider(ctx context.Context, name, source string) (Provider, error) {
	var in providerSource
	if err := decodeStrict(source, &in); err != nil {
		return Provider{}, err
	}

	if err := validProvider(in); err != nil {
		return Provider{}, err
	}

	if name != "" && name != in.Name {
		return Provider{}, fmt.Errorf("%w: the source names provider %q, not %q", ErrInvalid, in.Name, name)
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

func validProvider(in providerSource) error {
	kinds := []string{
		ProviderAnthropic, ProviderOpenAICompatible, ProviderClaudeSubscription, ProviderCodexSubscription,
		ProviderAntigravitySubscription, ProviderModelGateway,
	}

	switch {
	case !namePattern.MatchString(in.Name):
		return fmt.Errorf("%w: a provider name is lowercase letters, digits and dashes", ErrInvalid)
	case !slices.Contains(kinds, in.Kind):
		return fmt.Errorf("%w: kind is one of %s", ErrInvalid, strings.Join(kinds, ", "))
	case in.APIKeyRef != "" && (!keyRefPattern.MatchString(in.APIKeyRef) || looksLikeSecret(in.APIKeyRef)):
		return fmt.Errorf("%w: api_key_ref names where the key is kept (such as an environment variable), "+
			"never the key itself", ErrInvalid)
	case in.BaseURL != "" && !strings.HasPrefix(in.BaseURL, "https://") && !strings.HasPrefix(in.BaseURL, "http://"):
		return fmt.Errorf("%w: base_url is an http or https URL", ErrInvalid)
	}

	seen := map[string]bool{}

	for _, m := range in.Models {
		if strings.TrimSpace(m) == "" || seen[m] {
			return fmt.Errorf("%w: model names are non-empty and listed once", ErrInvalid)
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
	bots, err := q.ListBots(ctx)
	if err != nil {
		return fmt.Errorf("listing bots: %w", err)
	}

	for _, b := range bots {
		if b.Provider.String != in.Name {
			continue
		}

		if !slices.Contains(runtimeProviders[botRuntime], in.Kind) {
			return fmt.Errorf("%w: bot %s runs %s on provider %s, which kind %s cannot serve", ErrConflict,
				b.Name, botRuntime, in.Name, in.Kind)
		}

		if b.Model.Valid && !slices.Contains(in.Models, b.Model.String) {
			return fmt.Errorf("%w: bot %s uses model %s of provider %s", ErrConflict, b.Name, b.Model.String, in.Name)
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
			return fmt.Errorf("%w: profile %s runs %s on provider %s, which kind %s cannot serve", ErrConflict,
				v.Profile, v.Runtime, in.Name, in.Kind)
		}

		if v.Model.Valid && !slices.Contains(in.Models, v.Model.String) {
			return fmt.Errorf("%w: profile %s uses model %s of provider %s", ErrConflict, v.Profile, v.Model.String,
				in.Name)
		}
	}

	return nil
}

// GetProvider returns a provider and its models.
func (s *Service) GetProvider(ctx context.Context, name string) (Provider, error) {
	q := s.read()

	row, err := q.GetProvider(ctx, name)
	if err != nil {
		return Provider{}, notFound(err, "provider", name)
	}

	models, err := s.ListModels(ctx)
	if err != nil {
		return Provider{}, err
	}

	return providerFromRow(row, models), nil
}

// ProvidersUsingKeyRef names the providers whose api_key_ref is ref, so the
// vault can refuse to delete a secret a provider still names.
func (s *Service) ProvidersUsingKeyRef(ctx context.Context, ref string) ([]string, error) {
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
func (s *Service) ListProviders(ctx context.Context) ([]Provider, error) {
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
		UpdatedAt: millis(r.UpdatedAt), Models: []string{},
	}

	for _, m := range models {
		if m.Provider == r.Name {
			p.Models = append(p.Models, m.Name)
		}
	}

	return p
}

// ListModels returns every model with its provider and the provider's kind.
func (s *Service) ListModels(ctx context.Context) ([]Model, error) {
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

// checkTaskProfile checks a new Task's profile and its overrides together.
func checkTaskProfile(ctx context.Context, q *store.Queries, nt NewTask) error {
	v, err := q.CurrentProfileVersion(ctx, nt.Profile)
	if err != nil {
		return fmt.Errorf("task %q: %w", nt.Ref, notFound(err, "profile", nt.Profile))
	}

	if nt.Runtime == "" && nt.Model == "" && nt.Effort == "" {
		return nil
	}

	runtime := or(nt.Runtime, v.Runtime)

	provider := v.Provider.String
	if nt.Model != "" || nt.Runtime != "" {
		provider = "" // an override picks the provider again from the model
	}

	if _, err := checkModel(ctx, q, runtime, provider, nt.Model, nt.Effort); err != nil {
		return fmt.Errorf("task %q: %w", nt.Ref, err)
	}

	return nil
}

// resolved is what a Dispatch runs with: the Task's own values over its
// profile's current version.
type resolved struct {
	runtime, model, effort string
	// profile and versionID are the profile version the values come from.
	profile, versionID string
	// gateway says the model gateway serves the model, so no account on a
	// node is needed.
	gateway bool
	// fallbackFor is the Task's own profile when these are a backup's values,
	// and fallbackUntil when that profile's accounts are free again.
	fallbackFor   string
	fallbackUntil int64
	// handoff says the backup takes over work another worker began.
	handoff bool
}

func resolve(ctx context.Context, q *store.Queries, task store.Task) (resolved, error) {
	v, err := q.CurrentProfileVersion(ctx, task.Profile)
	if err != nil {
		return resolved{}, notFound(err, "profile", task.Profile)
	}

	// A Task that runs under a backup of its profile keeps that backup for
	// the rest of its open Conversation: a later Dispatch continues a session
	// of the backup's CLI, with the backup's model and instruction.
	last, err := q.LastDispatchOfTask(ctx, task.ID)

	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return resolved{}, fmt.Errorf("reading the last dispatch of task %s: %w", task.ID, err)
	case last.FallbackFor.Valid && last.ProfileVersionID.Valid:
		conv, open, err := openConversation(ctx, q, task.ID)
		if err != nil {
			return resolved{}, err
		}

		if open && conv.ID == last.ConversationID {
			b, err := q.GetProfileVersion(ctx, last.ProfileVersionID.String)
			if err != nil {
				return resolved{}, notFound(err, "profile version", last.ProfileVersionID.String)
			}

			r, err := resolveVersion(ctx, q, task, b)
			r.fallbackFor, r.fallbackUntil = last.FallbackFor.String, last.FallbackUntil.Int64

			return r, err
		}
	}

	return resolveVersion(ctx, q, task, v)
}

// resolveVersion is the Task's own values over one profile version.
func resolveVersion(ctx context.Context, q *store.Queries, task store.Task, v store.ProfileVersion,
) (resolved, error) {
	model, provider := v.Model.String, v.Provider.String
	if task.Runtime.String != "" && task.Runtime.String != v.Runtime {
		model, provider = "", "" // the profile's model and provider belong to its runtime
	}

	gateway := false

	if provider != "" {
		row, err := q.GetProvider(ctx, provider)
		if err != nil {
			return resolved{}, notFound(err, "provider", provider)
		}

		gateway = row.Kind == ProviderModelGateway
	}

	return resolved{
		runtime: or(task.Runtime.String, v.Runtime), model: or(task.Model.String, model),
		effort: or(task.Effort.String, v.Effort.String), profile: v.Profile, versionID: v.ID, gateway: gateway,
	}, nil
}
