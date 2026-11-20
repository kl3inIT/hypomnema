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
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/storeutil"
	"github.com/kl3inIT/hypomnema/internal/shared/agentconfig"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
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
	// ProviderOpenCodeGo is OpenCode Go (issue #854), served by the model gateway only under the Go keys the
	// owner pastes (OpenCodeGoRuntime).
	ProviderOpenCodeGo = "opencode_go"
	// ProviderOpenCodeZen is OpenCode Zen (issue #965), OpenCode's models paid from the workspace's balance, served
	// by the model gateway only under the Console keys the owner pastes (OpenCodeZenRuntime).
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

// throughGateway reports whether a provider of the kind is reached through the model gateway: the gateway itself,
// for a name made in its table, or a subscription it serves.
func throughGateway(kind string) bool {
	return kind == ProviderModelGateway || slices.Contains(subscriptionKinds, kind)
}

// subscriptionAccounts is the runtime of the accounts logged in on the control plane that each subscription runs
// under, as the gateway's adapters ask for them (modelgateway codexRuntime, antigravityRuntime; TokenRuntime).
var subscriptionAccounts = map[string]string{
	ProviderClaudeSubscription:      TokenRuntime,
	ProviderCodexSubscription:       "codex",
	ProviderAntigravitySubscription: "antigravity",
	ProviderOpenCodeGo:              OpenCodeGoRuntime,
	ProviderOpenCodeZen:             OpenCodeZenRuntime,
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

// PiModel is a chat's model as pi names it: the gateway's name under PiProvider when the gateway serves it, as it
// was otherwise.
func (s *Service) PiModel(ctx context.Context, m ChatModel) (ChatModel, error) {
	if m.Model == "" || m.Provider == "" {
		return m, nil
	}

	row, err := s.read().GetProvider(ctx, m.Provider)
	if err != nil {
		return ChatModel{}, notFound(err, "provider", m.Provider)
	}

	if throughGateway(row.Kind) {
		m.Model, m.Provider = GatewayModelName(row.Kind, m.Provider, m.Model), PiProvider
	}

	return m, nil
}

// GatewayWires is the wire a worker speaks to the gateway for a subscription's model, where it can choose
// (issue #549); its one home is agentconfig.Wires, which the Connect a tool page's files use too (issue #907).
var GatewayWires = agentconfig.Wires

// modelAPI is the wire for a Dispatch's model: its subscription's, when the gateway serves it under
// "<provider>/<model>".
func modelAPI(ctx context.Context, q *store.Queries, accountRef, model string) (string, error) {
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

// gatewayOnly reports whether a worker of the runtime runs only through the
// model gateway, never under an account a node holds: Claude Code (issue #381).
// omp and opencode name only the gateway too; their placement stays as it
// was, since issue #381 moved Claude Code only.
func gatewayOnly(runtime string) bool { return runtime == TokenRuntime }

// needsGatewayMessage says why a Claude Code worker cannot run elsewhere.
const needsGatewayMessage = "a Claude Code worker runs through the model gateway; its profile must name a " +
	"subscription or a provider of kind " + ProviderModelGateway

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
	Preset    string
	Tools     []string
	ToolsOff  []string
	// CPURequestMillis and MemoryRequestBytes are what a worker of the profile
	// asks of a node, from the YAML's requests (issue #731); 0 is the default
	// for building work, DefaultCPURequestMillis or DefaultMemoryRequest.
	CPURequestMillis   int64
	MemoryRequestBytes int64
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
	Preset      string   `yaml:"preset"`
	Tools       []string `yaml:"tools"`
	ToolsOff    []string `yaml:"tools_off"`
	// Requests is what a worker asks of a node (issue #731; requests.go).
	Requests *requestsSource `yaml:"requests"`
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
		Instruction: in.Instruction, Fallbacks: in.Fallbacks, Skills: in.Skills, SkillsOff: in.SkillsOff, Tools: in.Tools,
		Preset: agenttool.DefaultPreset(in.Preset), ToolsOff: in.ToolsOff, Source: source,
	}
	if in.Model != nil {
		p.Provider, p.Model, p.Effort = in.Model.Provider, in.Model.Name, in.Model.Effort
	}

	if in.Requests != nil {
		var err error
		if p.CPURequestMillis, p.MemoryRequestBytes, err = in.Requests.parse(); err != nil {
			return Profile{}, false, err
		}
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

		provider := ""
		if p.Runtime == "" && slices.Contains(roles, p.Name) {
			if in.Model != nil || len(p.Fallbacks) != 0 {
				return fmt.Errorf("%w: role %s uses role routing, not a profile model or fallbacks", ErrInvalid, p.Name)
			}
		} else {
			var err error
			provider, err = checkModel(ctx, q, p.Runtime, p.Provider, p.Model, p.Effort)
			if err != nil {
				return fmt.Errorf("profile %s: %w", p.Name, err)
			}
		}

		if err := checkFallbacks(ctx, q, p); err != nil {
			return fmt.Errorf("profile %s: %w", p.Name, err)
		}

		if err := checkSkills(ctx, q, p.Skills, p.SkillsOff); err != nil {
			return fmt.Errorf("profile %s: %w", p.Name, err)
		}

		if err := checkTools(p.Preset, p.Tools, p.ToolsOff); err != nil {
			return fmt.Errorf("profile %s: %w", p.Name, err)
		}

		p.Provider = provider
		digest := sourceDigest(source)

		// A deleted profile's name applied again brings it back as a new version, even from the same source, as
		// Onyx's mark_persona_as_not_deleted (backend/onyx/db/persona.py) (issue #485).
		current, err := currentVersionEvenDeleted(ctx, q, p.Name)

		switch {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return fmt.Errorf("reading profile %s: %w", p.Name, err)
		case current.Digest == digest && !current.deleted:
			out = profileFromRow(current.ProfileVersion)
			out.Source = editableProfileSource(out)

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
			Runtime: or(p.Runtime, roleRuntime), Provider: nullString(p.Provider), Model: nullString(p.Model),
			Effort: nullString(p.Effort), Instruction: nullString(p.Instruction),
			Fallbacks: strings.Join(p.Fallbacks, ","), Skills: strings.Join(p.Skills, ","),
			SkillsOff: strings.Join(p.SkillsOff, ","), Tools: strings.Join(p.Tools, ","),
			Preset: p.Preset, ToolsOff: strings.Join(p.ToolsOff, ","), CpuRequestMillis: positive(p.CPURequestMillis),
			MemoryRequestBytes: positive(p.MemoryRequestBytes), Now: now,
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

// versionOfAny is a profile's current version and whether the profile is
// deleted.
type versionOfAny struct {
	store.ProfileVersion

	deleted bool
}

// currentVersionEvenDeleted reads a profile's current version, a deleted
// profile's too; sql.ErrNoRows when there is no profile of the name.
func currentVersionEvenDeleted(ctx context.Context, q *store.Queries, name string) (versionOfAny, error) {
	row, err := q.GetProfileEvenDeleted(ctx, name)
	if err != nil {
		return versionOfAny{}, err
	}

	v, err := q.GetProfileVersion(ctx, row.CurrentVersionID)
	if err != nil {
		return versionOfAny{}, fmt.Errorf("reading the current version of profile %s: %w", name, err)
	}

	return versionOfAny{ProfileVersion: v, deleted: row.DeletedAt.Valid}, nil
}

// missingProfile is the error for a profile that is not there to use: none of
// the name, or a deleted one, which reads as not found, as Coder reads a
// deleted template (coderd/httpmw/templateparam.go), saying it was deleted.
func missingProfile(ctx context.Context, q *store.Queries, err error, name string) error {
	if !errors.Is(err, sql.ErrNoRows) {
		return notFound(err, "profile", name)
	}

	if row, rerr := q.GetProfileEvenDeleted(ctx, name); rerr == nil && row.DeletedAt.Valid {
		return fmt.Errorf("%w: profile %s was deleted; apply it again to bring it back", ErrNotFound, name)
	}

	return notFound(err, "profile", name)
}

// DeleteProfile deletes a profile (issue #485). One never used, by no Task
// and no Dispatch of any version, goes with its versions, as Omnigent removes
// an agent no session uses (server/routes/builtin_agents.py, remove_agent).
// One used before is marked deleted and kept with its versions for the
// Dispatches that ran them, as Coder marks a template deleted
// (coderd/templates.go, deleteTemplate) and Onyx a persona: it is hidden and
// no new Task names it. Refused for the starting profile (Hermes's default
// profile, Onyx's built-ins), and while an unfinished Task names it, another
// profile names it as a backup, or a Bot's workers list names it (Coder's
// workspaces, Omnigent's agent_in_use): those are changed first.
func (s *Service) DeleteProfile(ctx context.Context, name string) error {
	if name == DefaultProfile {
		return fmt.Errorf("%w: profile %s is the one a task that names none runs under; it is always there",
			ErrConflict, DefaultProfile)
	}

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		if _, err := q.GetProfile(ctx, name); err != nil {
			return missingProfile(ctx, q, err, name)
		}

		if err := profileDeletable(ctx, q, name); err != nil {
			return err
		}

		tasks, err := q.CountTasksOfProfileSince(ctx, store.CountTasksOfProfileSinceParams{Profile: name, Since: 0})
		if err != nil {
			return fmt.Errorf("reading the tasks of profile %s: %w", name, err)
		}

		ran, err := q.CountDispatchesOfProfile(ctx, name)
		if err != nil {
			return fmt.Errorf("reading the dispatches of profile %s: %w", name, err)
		}

		if tasks > 0 || ran > 0 {
			return q.MarkProfileDeleted(ctx, store.MarkProfileDeletedParams{Name: name, Now: now})
		}

		if err := q.DeleteProfileVersions(ctx, name); err != nil {
			return fmt.Errorf("removing the versions of profile %s: %w", name, err)
		}

		return q.DeleteProfile(ctx, name)
	})
	if err != nil {
		return fmt.Errorf("deleting profile %s: %w", name, err)
	}

	return nil
}

// profileDeletable refuses to delete a profile that unfinished Tasks, other
// profiles' backups or Bots' workers lists still name.
func profileDeletable(ctx context.Context, q *store.Queries, name string) error {
	unfinished, err := q.CountUnfinishedTasksOfProfile(ctx, name)
	if err != nil {
		return fmt.Errorf("reading the tasks of profile %s: %w", name, err)
	}

	named, err := q.CountFallbacksNaming(ctx, name)
	if err != nil {
		return fmt.Errorf("reading the backups naming profile %s: %w", name, err)
	}

	bots, err := q.BotsNamingWorker(ctx, nullString(name))
	if err != nil {
		return fmt.Errorf("reading the bots that give work to profile %s: %w", name, err)
	}

	switch {
	case unfinished > 0:
		return fmt.Errorf("%w: %d unfinished tasks name profile %s; let them end or cancel them first", ErrConflict,
			unfinished, name)
	case named > 0:
		return fmt.Errorf("%w: %d profiles name %s as a backup; change them first", ErrConflict, named, name)
	case len(bots) > 0:
		return fmt.Errorf("%w: the workers list of bots %s names profile %s; change them first", ErrConflict,
			strings.Join(bots, ", "), name)
	}

	return nil
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
			return fmt.Errorf("fallback: %w", missingProfile(ctx, q, err, name))
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
		// A bare model name never picks OpenCode Zen, which spends the workspace's balance and lists the models
		// the subscriptions and Go already serve: Zen is named (provider opencode-zen), as 9router and omniroute
		// address a model by its provider's alias (issue #965).
		if provider == "" && m.Kind == ProviderOpenCodeZen {
			continue
		}

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
	q := s.read()

	row, err := q.CurrentProfileVersion(ctx, name)
	if err != nil {
		return Profile{}, missingProfile(ctx, q, err, name)
	}

	out := []Profile{profileFromRow(row)}
	out[0].Source = editableProfileSource(out[0])
	if err := s.withAvailability(ctx, out); err != nil {
		return Profile{}, err
	}

	return out[0], nil
}

// editableProfileSource projects a migrated tool policy into the editor without rewriting the version's history.
func editableProfileSource(p Profile) string {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(p.Source), &doc); err != nil || len(doc.Content) != 1 {
		return p.Source
	}

	root := doc.Content[0]
	var source profileSource
	if root.Kind != yaml.MappingNode || root.Decode(&source) != nil ||
		agenttool.DefaultPreset(source.Preset) == p.Preset && slices.Equal(source.Tools, p.Tools) &&
			slices.Equal(source.ToolsOff, p.ToolsOff) {
		return p.Source
	}

	var policy yaml.Node
	if err := policy.Encode(struct {
		Preset string   `yaml:"preset"`
		On     []string `yaml:"tools,omitempty"`
		Off    []string `yaml:"tools_off,omitempty"`
	}{p.Preset, p.Tools, p.ToolsOff}); err != nil {
		return p.Source
	}

	keys := make([]*yaml.Node, 0, len(root.Content)+len(policy.Content))
	for index := 0; index < len(root.Content); index += 2 {
		switch root.Content[index].Value {
		case "preset", "tools", "tools_off":
		default:
			keys = append(keys, root.Content[index:index+2]...)
		}
	}

	root.Content = append(keys, policy.Content...)
	raw, err := yaml.Marshal(&doc)
	if err != nil {
		return p.Source
	}

	return string(raw)
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
		if pr.Runtime == "" {
			continue
		}
		gateway, accounts := false, ""

		if pr.Provider != "" {
			row, err := q.GetProvider(ctx, pr.Provider)
			if err != nil {
				return notFound(err, "provider", pr.Provider)
			}

			gateway, accounts = throughGateway(row.Kind), subscriptionAccounts[row.Kind]
		}

		a := p.availability(pr.Runtime, gateway, accounts)
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
		profile := profileFromRow(r)
		profile.Source = editableProfileSource(profile)
		out = append(out, profile)
	}

	return out, nil
}

func profileFromRow(v store.ProfileVersion) Profile {
	runtime := v.Runtime
	if runtime == roleRuntime {
		runtime = ""
	}
	return Profile{
		Name: v.Profile, Description: v.Description, VersionID: v.ID, Version: int(v.Version), Runtime: runtime,
		Provider: v.Provider.String,
		Model:    v.Model.String, Effort: v.Effort.String, Instruction: v.Instruction.String, Source: v.Source,
		Fallbacks: splitNames(v.Fallbacks), Skills: splitNames(v.Skills), SkillsOff: splitNames(v.SkillsOff),
		Preset: v.Preset, Tools: splitNames(v.Tools), ToolsOff: splitNames(v.ToolsOff), CreatedAt: millis(v.CreatedAt),
		CPURequestMillis: v.CpuRequestMillis.Int64, MemoryRequestBytes: v.MemoryRequestBytes.Int64,
	}
}

// positive is a number stored as NULL when it is 0, left out.
func positive(n int64) sql.NullInt64 { return sql.NullInt64{Int64: n, Valid: n > 0} }

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

// AddProviderModels adds to a provider the models it does not list yet, as a subscription's vendor names what it
// serves (issue #623): Codex reads its catalog from its backend and keeps a bundled one (codex-rs/models-manager),
// as OpenClaw, omp and Hermes do. Nothing is removed, so a key or a Bot that names a model the vendor stopped listing
// keeps it until the owner changes the provider. The source is written again with the models added. added are the
// ones that were not there.
func (s *Service) AddProviderModels(ctx context.Context, name string, models []string) (added []string, err error) {
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
func (s *Service) SetProviderModels(
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
func (s *Service) RemoveProviderModel(ctx context.Context, name, model string) (removed bool, err error) {
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
		return fmt.Errorf("%w: a provider name is lowercase letters, digits and dashes", ErrInvalid)
	case !slices.Contains(kinds, in.Kind):
		return fmt.Errorf("%w: kind is one of %s", ErrInvalid, strings.Join(kinds, ", "))
	case in.APIKeyRef != "" && (!keyRefPattern.MatchString(in.APIKeyRef) || looksLikeSecret(in.APIKeyRef)):
		return fmt.Errorf("%w: api_key_ref names where the key is kept (such as an environment variable), "+
			"never the key itself", ErrInvalid)
	case in.BaseURL != "" && !strings.HasPrefix(in.BaseURL, "https://") && !strings.HasPrefix(in.BaseURL, "http://"):
		return fmt.Errorf("%w: base_url is an http or https URL", ErrInvalid)
	}

	for family, v := range in.MinVersions {
		if _, ok := parseVersion(v); !ok || family == "" {
			return fmt.Errorf("%w: min_versions names a family and a version such as 6 or 3.8", ErrInvalid)
		}
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
	bots, err := q.ListBotModels(ctx)
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

// DeleteProvider removes a provider and its models, when nothing names it. A provider that a bot, a chat, a profile
// version or a gateway model's target still uses is kept, with what uses it named in the error, so no reference is
// orphaned (issue #610).
func (s *Service) DeleteProvider(ctx context.Context, name string) error {
	return s.write(ctx, func(q *store.Queries, _ int64) error {
		if _, err := q.GetProvider(ctx, name); err != nil {
			return notFound(err, "provider", name)
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
			return fmt.Errorf("%w: provider %s is used by %s", ErrConflict, name, strings.Join(used, ", "))
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
func checkTaskProfile(ctx context.Context, q *store.Queries, nt NewTask, vendor string) error {
	v, err := q.CurrentProfileVersion(ctx, nt.Profile)
	if err != nil {
		return fmt.Errorf("task %q: %w", nt.Ref, missingProfile(ctx, q, err, nt.Profile))
	}
	if v.Runtime == roleRuntime || vendor != "" {
		if nt.Profile == "reviewer" && nt.ReviewOf == "" {
			return fmt.Errorf("%w: reviewer Task %q needs review_of", ErrInvalid, nt.Ref)
		}
		if nt.Effort != "" && !slices.Contains(Efforts, nt.Effort) {
			return fmt.Errorf("%w: unknown effort %q", ErrInvalid, nt.Effort)
		}
		if nt.Runtime != "" || nt.Model != "" {
			task := store.Task{Profile: nt.Profile, Kind: nt.Kind, AuthorVendor: vendor,
				Runtime: nullString(nt.Runtime), Model: nullString(nt.Model), Effort: nullString(nt.Effort)}
			chain, err := chosenRoleChain(ctx, q, task)
			if err != nil {
				return err
			}
			if len(chain) == 0 {
				return fmt.Errorf("%w: requested runtime %s is not in the Task's role chain", ErrInvalid, nt.Runtime)
			}
		}
		return nil
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
	// accounts is the runtime of the control plane's accounts the gateway serves the model under, for a
	// subscription's: without an active one, nothing can serve it.
	accounts string
	// fallbackFor is the Task's own profile when these are a backup's values,
	// and fallbackUntil when that profile's accounts are free again.
	fallbackFor   string
	fallbackUntil int64
	// handoff says the backup takes over work another worker began.
	handoff bool
	// req is what a worker of the profile version asks of a node (issue #731).
	req          request
	chain        []roleEntry
	chainIndex   int
	gatewayUntil int64
	gatewayRoute string
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
	case last.RoleChain != "":
		chain, err := dispatchRoleChain(last)
		if err != nil {
			return resolved{}, err
		}
		version, err := q.GetProfileVersion(ctx, last.ProfileVersionID.String)
		if err != nil {
			return resolved{}, notFound(err, "profile version", last.ProfileVersionID.String)
		}
		r, err := resolveRoleEntry(ctx, q, version, task, chain, int(last.ChainEntry.Int64)-1)
		r.fallbackFor, r.fallbackUntil = last.FallbackFor.String, last.FallbackUntil.Int64
		if FailureClass(last.FailureClass.String) == FailureRateLimited {
			r.gatewayUntil = last.ResetsAt.Int64
		}
		return r, err
	case v.Runtime == roleRuntime && last.ProfileVersionID.Valid:
		version, err := q.GetProfileVersion(ctx, last.ProfileVersionID.String)
		if err != nil {
			return resolved{}, notFound(err, "profile version", last.ProfileVersionID.String)
		}
		return resolveVersion(ctx, q, task, version)
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
	if v.Runtime == roleRuntime || task.AuthorVendor != "" {
		chain, err := chosenRoleChain(ctx, q, task)
		if err != nil {
			return resolved{}, err
		}
		if len(chain) == 0 {
			return resolved{}, fmt.Errorf("%w: no entry for %s/%s matches the Task", errRoleEntry, task.Profile, task.Kind)
		}
		return resolveRoleEntry(ctx, q, v, task, chain, 0)
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

	runtime := or(task.Runtime.String, v.Runtime)

	// A Task's own model picks its provider again, as checkTaskProfile checked it: the profile's provider may not
	// offer it.
	if task.Model.String != "" {
		model = task.Model.String

		p, err := checkModel(ctx, q, runtime, "", model, "")
		if err != nil {
			return resolved{}, err
		}

		provider = p
	}

	gateway, accounts := false, ""

	if provider != "" {
		row, err := q.GetProvider(ctx, provider)
		if err != nil {
			return resolved{}, notFound(err, "provider", provider)
		}

		gateway, accounts = throughGateway(row.Kind), subscriptionAccounts[row.Kind]
		if model != "" {
			model = GatewayModelName(row.Kind, provider, model)
		}
	}
	var route string
	if gateway {
		var err error
		route, err = gatewayRoute(ctx, q, runtime, model)
		if err != nil {
			return resolved{}, err
		}
	}

	return resolved{
		runtime: runtime, model: model,
		effort: or(task.Effort.String, v.Effort.String), profile: v.Profile, versionID: v.ID, gateway: gateway,
		accounts: accounts, req: requestOf(v), gatewayRoute: route,
	}, nil
}
