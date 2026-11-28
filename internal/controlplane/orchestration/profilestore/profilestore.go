// Package profilestore keeps the profiles (issue #42), their versions, the
// role routing (issue #1141) and the providers and models they name, a domain
// store of the orchestration database reached through the root package's
// Profiles(), as routinestore is (issue #1241). It imports the generated
// store and never the root package; it only reads the tables of other
// domains (Tasks, Dispatches, Bots, skills), through the shared queries. A
// write that spans another domain, such as a profile changed from the main
// Bot's chat with what undoes it, stays in the root and calls the methods and
// functions here that end in Tx with its own queries. Which profile version a
// Task's Dispatch runs, and where, is the scheduler's in the root.
//
// Profiles, after Omnara's agent configs and profiles: a profile names the
// runtime, provider, model, effort and instruction an agent runs with.
// Applying a changed source makes a new immutable version; the name points at
// the latest. Providers and the models they offer are their own records, so a
// profile changes nothing when a provider moves (9router today, the model
// gateway later).
package profilestore

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
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/storeerr"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

// DefaultProfile is the profile of a Task that names none.
const DefaultProfile = "implementer"

// MaxDescription bounds a profile's description.
const MaxDescription = 500

// MaxFallbacks bounds an agent's fallbacks, a Bot's or a profile's, as OpenMausBot keeps five backups for a bot
// (src/components/bot-panel/BackupAis.tsx, MAX_BACKUPS).
const MaxFallbacks = 5

// BotRuntime is what every Bot runs: pi, started by the control plane. No node has it and no Task is placed under it
// (issue #169).
const BotRuntime = "pi"

// Efforts lists the reasoning efforts a profile or a Task may name.
var Efforts = []string{"low", "medium", "high", "xhigh", "max"}

var namePattern = storeutil.NamePattern

// AgentSpec is what a Bot and a Profile have in common (issue #1336), embedded in each, as OpenClaw keeps one
// agent entry for its main agent and its sub-agents (src/config/zod-schema.agent-entry-base.ts) and Paperclip one
// agents table with the runtime as a column (packages/db/src/schema/agents.ts). What only a Bot has (its
// Connections, workers, main chat) and what only a Profile has (its runtime, requests, versions' source) stay on
// their own type.
type AgentSpec struct {
	// Instructions are what the agent is and how it works, written by the owner; they follow the runtime's own.
	// A Bot's were its persona and a Profile's its instruction until 2026-11-28.
	Instructions string
	Provider     string
	Model        string
	Effort       string
	// Fallbacks are models, each provider/model, in the owner's order, tried in turn when the agent's own model
	// cannot be served now, as OpenClaw's model fallbacks (src/config/zod-schema.agent-model.ts). A Profile's
	// named backup Profiles until 2026-11-28 (issue #171).
	Fallbacks []string
	// Skills are the skills the agent gets beside those given to everyone; SkillsOff the ones given to everyone
	// it does not get (issue #21).
	Skills    []string
	SkillsOff []string
	Preset    string
	Tools     []string
	ToolsOff  []string
}

// ToolSwitches are the agent's tool switches.
func (a AgentSpec) ToolSwitches() agenttool.ToolSwitches {
	return agenttool.ToolSwitches{Preset: a.Preset, On: a.Tools, Off: a.ToolsOff}
}

// Profile is the current version of a named profile, or one version of it.
type Profile struct {
	AgentSpec

	Name string
	// Description says what the profile is for, so a coordinator chooses by it.
	Description string
	VersionID   string
	Version     int
	Runtime     string
	// CPURequestMillis and MemoryRequestBytes are what a worker of the profile
	// asks of a node, from the YAML's requests (issue #731); 0 is the default
	// for building work, DefaultCPURequestMillis or DefaultMemoryRequest.
	CPURequestMillis   int64
	MemoryRequestBytes int64
	// MemoryLimitBytes is each worker's own memory ceiling, from the YAML's limits.memory (issue #1313; limits.go):
	// the MemoryMax of its scope on the node. 0: none.
	MemoryLimitBytes int64
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

// Placement says whether a worker can start under each profile now. It is the
// scheduler's, which the root gives (WithPlacement), as Omnigent's agent list
// carries available_on_host and unavailable_reason.
type Placement interface {
	// SetAvailability sets each profile's Availability; one with no runtime,
	// a role's, keeps none.
	SetAvailability(ctx context.Context, profiles []Profile) error
}

// Store keeps the profiles, their versions, the role routing, and the
// providers and models they name.
type Store struct {
	db    *sql.DB
	clock platform.Clock
	newID func(prefix string) string
	// signal wakes the orchestration's event readers after each commit.
	signal func()
	// placement says whether a worker can start under a profile; nil says
	// nothing.
	placement Placement
}

// New returns the Store over the orchestration database. newID and signal
// are the root's: ids are made and commits signaled as for every other
// write.
func New(db *sql.DB, clock platform.Clock, newID func(prefix string) string, signal func()) *Store {
	return &Store{db: db, clock: clock, newID: newID, signal: signal}
}

// WithPlacement lets the store say of each profile it reads whether a worker
// can start under it now; without it, a profile carries no Availability.
func (s *Store) WithPlacement(p Placement) *Store {
	s.placement = p

	return s
}

func (s *Store) write(ctx context.Context, fn func(q *store.Queries, now int64) error) error {
	return storeutil.Write(ctx, s.db, s.clock, s.signal, fn)
}

func (s *Store) read() *store.Queries {
	return store.New(s.db)
}

func nullString(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

func or(a, b string) string {
	if a != "" {
		return a
	}

	return b
}

// Source is the YAML document a profile is written in.
type Source struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Runtime     string `yaml:"runtime"`
	Model       *struct {
		Provider string `yaml:"provider"`
		Name     string `yaml:"name"`
		Effort   string `yaml:"effort"`
	} `yaml:"model"`
	Instructions string `yaml:"instructions"`
	// Instruction is Instructions under its name until 2026-11-28 (issue #1336), read for one release.
	Instruction string   `yaml:"instruction"`
	Fallbacks   []string `yaml:"fallbacks"`
	Skills      []string `yaml:"skills"`
	SkillsOff   []string `yaml:"skills_off"`
	Preset      string   `yaml:"preset"`
	Tools       []string `yaml:"tools"`
	ToolsOff    []string `yaml:"tools_off"`
	// Requests is what a worker asks of a node (issue #731; requests.go).
	Requests *requestsSource `yaml:"requests"`
	// Limits is the most memory a worker may use (issue #1313; limits.go).
	Limits *limitsSource `yaml:"limits"`
}

// DecodeSource reads a profile's YAML, refusing unknown fields.
func DecodeSource(source string) (Source, error) {
	var in Source
	err := decodeStrict(source, &in)

	return in, err
}

// decodeStrict reads one YAML document, refusing unknown fields.
func decodeStrict(source string, v any) error {
	dec := yaml.NewDecoder(strings.NewReader(source))
	dec.KnownFields(true)

	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w: not a valid document: %w", storeerr.ErrInvalid, decodeError{err})
	}

	return nil
}

// legacyTypeNames are the names the YAML decoder's errors gave the types a document is read into while they were
// orchestration's (issue #1241). The client API returns such an error's text as it is, so a moved type keeps its old
// name there.
var legacyTypeNames = map[string]string{
	"profilestore.Source":         "orchestration.profileSource",
	"profilestore.requestsSource": "orchestration.requestsSource",
	"profilestore.providerSource": "orchestration.providerSource",
	"profilestore.roleRouting":    "orchestration.roleRouting",
	"profilestore.RoleEntry":      "orchestration.roleEntry",
}

// qualifiedName is a package-qualified Go type name inside a type, such as profilestore.RoleEntry in
// map[string][]profilestore.RoleEntry.
var qualifiedName = regexp.MustCompile(`\bprofilestore\.\w+`)

// decodeError is the YAML decoder's error, naming each type it was decoding into as legacyTypeNames does. Only the
// type the decoder writes at the end of each of its lines is renamed: what the document said, a field's name or a
// value, which comes before it, is kept as written.
type decodeError struct{ err error }

func (e decodeError) Error() string {
	var te *yaml.TypeError
	if !errors.As(e.err, &te) {
		return e.err.Error()
	}

	lines := make([]string, len(te.Errors))
	for i, line := range te.Errors {
		lines[i] = legacyTypeOf(line)
	}

	return (&yaml.TypeError{Errors: lines}).Error()
}

func (e decodeError) Unwrap() error { return e.err }

// legacyTypeOf renames the moved types in the type a decoder's line ends with: "… into <type>" or "… in type <type>"
// (go.yaml.in/yaml/v3 decode.go, d.terrors), where the type, out.Type(), is the line's last word.
func legacyTypeOf(line string) string {
	at := strings.LastIndex(line, " ") + 1
	head, typ := line[:at], line[at:]

	if !strings.HasSuffix(head, " into ") && !strings.HasSuffix(head, " in type ") {
		return line
	}

	return head + qualifiedName.ReplaceAllStringFunc(typ, func(name string) string {
		if old, ok := legacyTypeNames[name]; ok {
			return old
		}

		return name
	})
}

// SourceDigest is the SHA-256 of a source with line endings normalized, as a version stores it.
func SourceDigest(source string) string {
	sum := sha256.Sum256(bytes.ReplaceAll([]byte(source), []byte("\r\n"), []byte("\n")))

	return hex.EncodeToString(sum[:])
}

// Draft is a profile's YAML read and checked by itself, before it is checked
// against the database and stored (ApplyTx).
type Draft struct {
	profile Profile
	// hasModel says the source names a model, even an empty one.
	hasModel bool
}

// ParseProfile reads a profile's YAML and checks what needs no database: its
// name, runtime, description and requests. A non-empty name must equal the
// source's.
func ParseProfile(name, source string) (Draft, error) {
	var in Source
	if err := decodeStrict(source, &in); err != nil {
		return Draft{}, err
	}

	if in.Instructions != "" && in.Instruction != "" {
		return Draft{}, fmt.Errorf("%w: give instructions, not instructions and instruction, its name until "+
			"2026-11-28", storeerr.ErrInvalid)
	}

	p := Profile{
		Name: in.Name, Description: strings.TrimSpace(in.Description), Runtime: in.Runtime, Source: source,
		AgentSpec: AgentSpec{
			Instructions: or(in.Instructions, in.Instruction), Fallbacks: in.Fallbacks, Skills: in.Skills,
			SkillsOff: in.SkillsOff, Tools: in.Tools, Preset: agenttool.DefaultPreset(in.Preset), ToolsOff: in.ToolsOff,
		},
	}
	if in.Model != nil {
		p.Provider, p.Model, p.Effort = in.Model.Provider, in.Model.Name, in.Model.Effort
	}

	if in.Requests != nil {
		var err error
		if p.CPURequestMillis, p.MemoryRequestBytes, err = in.Requests.parse(); err != nil {
			return Draft{}, err
		}
	}

	if in.Limits != nil {
		var err error
		if p.MemoryRequestBytes, p.MemoryLimitBytes, err = in.Limits.parse(p.MemoryRequestBytes); err != nil {
			return Draft{}, err
		}
	}

	if !namePattern.MatchString(p.Name) {
		return Draft{}, fmt.Errorf("%w: a profile name is lowercase letters, digits and dashes", storeerr.ErrInvalid)
	}

	// pi is run by the control plane for a Bot; no node has it and no Task is placed under it (issue #169).
	if p.Runtime == BotRuntime {
		return Draft{}, fmt.Errorf("%w: runtime %s is a bot's, not a worker's; see `hypomnema bots`",
			storeerr.ErrInvalid, BotRuntime)
	}

	if name != "" && name != p.Name {
		return Draft{}, fmt.Errorf("%w: the source names profile %q, not %q", storeerr.ErrInvalid, p.Name, name)
	}

	if p.Description == "" || len(p.Description) > MaxDescription {
		return Draft{}, fmt.Errorf("%w: a profile needs a description of at most %d characters: what it is for",
			storeerr.ErrInvalid, MaxDescription)
	}

	return Draft{profile: p, hasModel: in.Model != nil}, nil
}

// ApplyProfile stores a profile's YAML. A source that differs from the
// current version makes a new version and points the name at it; the same
// source returns the current version. created reports a new version. A
// non-empty name must equal the source's. One the main Bot applies from its
// chat is the root's, which records what undoes it (setupchanges.go).
func (s *Store) ApplyProfile(ctx context.Context, name, source string) (Profile, bool, error) {
	d, err := ParseProfile(name, source)
	if err != nil {
		return Profile{}, false, err
	}

	var (
		out     Profile
		created bool
	)

	err = s.write(ctx, func(q *store.Queries, now int64) error {
		var err error
		out, created, _, err = s.ApplyTx(ctx, q, now, d)

		return err
	})
	if err != nil {
		return Profile{}, false, fmt.Errorf("applying a profile: %w", err)
	}

	return out, created, nil
}

// ApplyTx checks a draft against the database and stores it in the caller's
// transaction: its model, backups, skills and tools. The same source as the
// current version changes nothing; created reports a new version, and before
// the version it replaced, "" for a profile made now.
func (s *Store) ApplyTx(ctx context.Context, q *store.Queries, now int64, d Draft,
) (out Profile, created bool, before string, err error) {
	p := d.profile

	provider := ""
	if p.Runtime == "" && slices.Contains(Roles, p.Name) {
		if d.hasModel || len(p.Fallbacks) != 0 {
			return Profile{}, false, "", fmt.Errorf("%w: role %s uses role routing, not a profile model or fallbacks",
				storeerr.ErrInvalid, p.Name)
		}
	} else {
		provider, err = CheckModelTx(ctx, q, p.Runtime, p.Provider, p.Model, p.Effort)
		if err != nil {
			return Profile{}, false, "", fmt.Errorf("profile %s: %w", p.Name, err)
		}
	}

	if err := CheckFallbacksTx(ctx, q, p.Runtime, provider, p.Model, p.Fallbacks); err != nil {
		return Profile{}, false, "", fmt.Errorf("profile %s: %w", p.Name, err)
	}

	if err := CheckSkillsTx(ctx, q, p.Skills, p.SkillsOff); err != nil {
		return Profile{}, false, "", fmt.Errorf("profile %s: %w", p.Name, err)
	}

	if err := CheckTools(p.Preset, p.Tools, p.ToolsOff); err != nil {
		return Profile{}, false, "", fmt.Errorf("profile %s: %w", p.Name, err)
	}

	p.Provider = provider
	digest := SourceDigest(p.Source)

	// A deleted profile's name applied again brings it back as a new version, even from the same source, as
	// Onyx's mark_persona_as_not_deleted (backend/onyx/db/persona.py) (issue #485).
	current, err := currentVersionEvenDeleted(ctx, q, p.Name)

	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return Profile{}, false, "", fmt.Errorf("reading profile %s: %w", p.Name, err)
	// The editor's text of the current version, with what a migration changed projected into it (issue #1336), is
	// the same source as the one stored.
	case (current.Digest == digest || EditableSource(current.ProfileVersion) == p.Source) && !current.deleted &&
		current.Provider.String == p.Provider:
		out = profileFromRow(current.ProfileVersion)
		out.Source = editableProfileSource(out)

		return out, false, "", nil
	}

	id := s.newID("pfv")

	// The profile row comes first: its versions reference it.
	if current.ID == "" {
		err = q.InsertProfile(ctx, store.InsertProfileParams{Name: p.Name, VersionID: id, Now: now})
	} else {
		err = q.SetProfileVersion(ctx, store.SetProfileVersionParams{Name: p.Name, VersionID: id, Now: now})
	}

	if err != nil {
		return Profile{}, false, "", fmt.Errorf("pointing profile %s at its new version: %w", p.Name, err)
	}

	if err := q.InsertProfileVersion(ctx, store.InsertProfileVersionParams{
		ID: id, Profile: p.Name, Version: current.Version + 1, Source: p.Source, Digest: digest,
		Description: p.Description, Runtime: or(p.Runtime, RoleRuntime), Provider: nullString(p.Provider),
		Model: nullString(p.Model), Effort: nullString(p.Effort), Instructions: nullString(p.Instructions),
		Fallbacks: strings.Join(p.Fallbacks, ","), Skills: strings.Join(p.Skills, ","),
		SkillsOff: strings.Join(p.SkillsOff, ","), Tools: strings.Join(p.Tools, ","),
		Preset: p.Preset, ToolsOff: strings.Join(p.ToolsOff, ","), CpuRequestMillis: positive(p.CPURequestMillis),
		MemoryRequestBytes: positive(p.MemoryRequestBytes), MemoryLimitBytes: positive(p.MemoryLimitBytes), Now: now,
	}); err != nil {
		return Profile{}, false, "", fmt.Errorf("inserting a version of profile %s: %w", p.Name, err)
	}

	row, err := q.GetProfileVersion(ctx, id)
	if err != nil {
		return Profile{}, false, "", fmt.Errorf("reading the new version of profile %s: %w", p.Name, err)
	}

	return profileFromRow(row), true, current.ID, nil
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

// MissingProfileTx is the error for a profile that is not there to use: none of
// the name, or a deleted one, which reads as not found, as Coder reads a
// deleted template (coderd/httpmw/templateparam.go), saying it was deleted.
func MissingProfileTx(ctx context.Context, q *store.Queries, err error, name string) error {
	if !errors.Is(err, sql.ErrNoRows) {
		return storeutil.NotFound(err, "profile", name)
	}

	if row, rerr := q.GetProfileEvenDeleted(ctx, name); rerr == nil && row.DeletedAt.Valid {
		return fmt.Errorf("%w: profile %s was deleted; apply it again to bring it back", storeerr.ErrNotFound, name)
	}

	return storeutil.NotFound(err, "profile", name)
}

// DeleteProfile deletes a profile (issue #485). One never used, by no Task
// and no Dispatch of any version, goes with its versions, as Omnigent removes
// an agent no session uses (server/routes/builtin_agents.py, remove_agent).
// One used before is marked deleted and kept with its versions for the
// Dispatches that ran them, as Coder marks a template deleted
// (coderd/templates.go, deleteTemplate) and Onyx a persona: it is hidden and
// no new Task names it. Refused for the starting profile (Hermes's default
// profile, Onyx's built-ins), and while an unfinished Task names it or a Bot's
// workers list names it (Coder's workspaces, Omnigent's agent_in_use): those
// are changed first.
func (s *Store) DeleteProfile(ctx context.Context, name string) error {
	if name == DefaultProfile {
		return fmt.Errorf("%w: profile %s is the one a task that names none runs under; it is always there",
			storeerr.ErrConflict, DefaultProfile)
	}

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		if _, err := q.GetProfile(ctx, name); err != nil {
			return MissingProfileTx(ctx, q, err, name)
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

// profileDeletable refuses to delete a profile that unfinished Tasks or Bots' workers lists still name.
func profileDeletable(ctx context.Context, q *store.Queries, name string) error {
	unfinished, err := q.CountUnfinishedTasksOfProfile(ctx, name)
	if err != nil {
		return fmt.Errorf("reading the tasks of profile %s: %w", name, err)
	}

	bots, err := q.BotsNamingWorker(ctx, nullString(name))
	if err != nil {
		return fmt.Errorf("reading the bots that give work to profile %s: %w", name, err)
	}

	switch {
	case unfinished > 0:
		return fmt.Errorf("%w: %d unfinished tasks name profile %s; let them end or cancel them first", storeerr.ErrConflict,
			unfinished, name)
	case len(bots) > 0:
		return fmt.Errorf("%w: the workers list of bots %s names profile %s; change them first", storeerr.ErrConflict,
			strings.Join(bots, ", "), name)
	}

	return nil
}

// CheckFallbacksTx checks an agent's fallbacks, a Bot's or a profile's (issue #1336): at most MaxFallbacks, each
// provider/model, served on the agent's runtime, named once and not the agent's own model, which they follow, as
// OpenClaw's model fallbacks follow its primary (src/config/zod-schema.agent-model.ts). provider is the agent's own,
// as CheckModelTx found it.
func CheckFallbacksTx(ctx context.Context, q *store.Queries, runtime, provider, model string, fallbacks []string,
) error {
	if len(fallbacks) == 0 {
		return nil
	}

	if len(fallbacks) > MaxFallbacks {
		return fmt.Errorf("%w: an agent has at most %d fallbacks", storeerr.ErrInvalid, MaxFallbacks)
	}

	if model == "" {
		return fmt.Errorf("%w: choose a primary model before adding fallbacks", storeerr.ErrInvalid)
	}

	seen := map[string]bool{provider + "/" + model: true}

	for _, ref := range fallbacks {
		fp, fm, err := ModelRef(ref)

		switch {
		case err != nil:
			return err
		case fp == "" || strings.ContainsAny(ref, ",\r\n") || strings.TrimSpace(ref) != ref:
			return fmt.Errorf("%w: fallback %q must be provider/model", storeerr.ErrInvalid, ref)
		case seen[ref]:
			return fmt.Errorf("%w: fallback %q repeats a model in the chain", storeerr.ErrInvalid, ref)
		}

		seen[ref] = true

		if _, err := CheckModelTx(ctx, q, runtime, fp, fm, ""); err != nil {
			return fmt.Errorf("fallback %s: %w", ref, err)
		}
	}

	return nil
}

// CheckModelTx checks a runtime, provider, model and effort together and
// returns the provider, found from the model when none is named: the runtime
// must be known, the provider able to serve it, and the model offered by it.
func CheckModelTx(ctx context.Context, q *store.Queries, runtime, provider, model, effort string) (string, error) {
	kinds, ok := runtimeProviders[runtime]
	if !ok {
		return "", fmt.Errorf("%w: unknown runtime %q; known: %s", storeerr.ErrInvalid, runtime, knownRuntimes())
	}

	if effort != "" && !slices.Contains(Efforts, effort) {
		return "", fmt.Errorf("%w: unknown effort %q; known: %s", storeerr.ErrInvalid, effort, strings.Join(Efforts, ", "))
	}

	if provider != "" {
		row, err := q.GetProvider(ctx, provider)
		if err != nil {
			return "", storeutil.NotFound(err, "provider", provider)
		}

		switch {
		case !slices.Contains(kinds, row.Kind) && GatewayOnly(runtime):
			return "", fmt.Errorf("%w: provider %s is of kind %s: %s", storeerr.ErrInvalid, provider, row.Kind,
				NeedsGatewayMessage)
		case !slices.Contains(kinds, row.Kind):
			return "", fmt.Errorf("%w: runtime %s cannot run against provider %s (%s)", storeerr.ErrInvalid, runtime, provider,
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
	case provider == "":
		// Every model is named with its provider (owner, 2026-11-20; issue #1002): a bare name is never resolved,
		// as omniroute refuses a bare name it cannot place ("Use provider/model prefix", open-sse/services/model.ts)
		// and LiteLLM one it does not know ("Pass model as E.g. huggingface/starcoder",
		// litellm_core_utils/get_llm_provider_logic.py), so a provider added later never changes what it means.
		return "", BareModel(runtime, model, fits)
	case len(fits) == 0:
		return "", fmt.Errorf("%w: provider %s does not offer model %q", storeerr.ErrInvalid, provider, model)
	default:
		return fits[0], nil
	}
}

// BareModel is the refusal of a model named without its provider: the form to write it in, with the names it
// could be written as, or why no provider would serve it.
func BareModel(runtime, model string, fits []string) error {
	hint := "name it <provider>/<model>"

	switch {
	case len(fits) > 0:
		named := make([]string, len(fits))
		for i, p := range fits {
			named[i] = p + "/" + model
		}

		hint += ", such as " + strings.Join(named, " or ")
	case GatewayOnly(runtime):
		hint += fmt.Sprintf("; no provider of kind %s offers it: %s", ProviderModelGateway, NeedsGatewayMessage)
	default:
		hint += fmt.Sprintf("; no provider runtime %s can use offers it", runtime)
	}

	return fmt.Errorf("%w: model %q is named without its provider; %s", storeerr.ErrInvalid, model, hint)
}

// ModelRef reads a model named with its provider, "<provider>/<model>" (issue #1002), as 9router and omniroute
// read one at its first slash (9router open-sse/services/model.js parseModel): a provider's name has no slash, a
// model's may. A name without a slash has no provider, which CheckModelTx refuses with the form to write it in; a
// name with an empty part ("opencode-go/", "/m", "/") is refused here, as omniroute refuses a name it cannot place.
func ModelRef(ref string) (provider, model string, err error) {
	provider, model, found := strings.Cut(ref, "/")
	if !found {
		return "", ref, nil
	}

	if provider == "" || model == "" {
		return "", "", fmt.Errorf("%w: model %q has an empty part; name it <provider>/<model>, such as "+
			"opencode-go/deepseek-v4-pro", storeerr.ErrInvalid, ref)
	}

	return provider, model, nil
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
func (s *Store) GetProfile(ctx context.Context, name string) (Profile, error) {
	q := s.read()

	row, err := q.CurrentProfileVersion(ctx, name)
	if err != nil {
		return Profile{}, MissingProfileTx(ctx, q, err, name)
	}

	out := []Profile{profileFromRow(row)}
	out[0].Source = editableProfileSource(out[0])
	if err := s.withAvailability(ctx, out); err != nil {
		return Profile{}, err
	}

	return out[0], nil
}

// EditableSource is a version's source as the editor shows it (editableProfileSource).
func EditableSource(v store.ProfileVersion) string {
	return editableProfileSource(profileFromRow(v))
}

// editableProfileSource projects what a migration changed into the editor without rewriting the version's history:
// the tool policy (00123), the instructions' name and the fallbacks converted from backup profiles to models (issue
// #1336). A source a migration left as it was is returned as written.
func editableProfileSource(p Profile) string {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(p.Source), &doc); err != nil || len(doc.Content) != 1 {
		return p.Source
	}

	root := doc.Content[0]
	var source Source
	if root.Kind != yaml.MappingNode || root.Decode(&source) != nil {
		return p.Source
	}

	policyKept := agenttool.DefaultPreset(source.Preset) == p.Preset && slices.Equal(source.Tools, p.Tools) &&
		slices.Equal(source.ToolsOff, p.ToolsOff)
	fallbacksKept := slices.Equal(source.Fallbacks, p.Fallbacks)

	if policyKept && fallbacksKept && source.Instruction == "" {
		return p.Source
	}

	keys := make([]*yaml.Node, 0, len(root.Content)+6)
	for index := 0; index < len(root.Content); index += 2 {
		key, value := root.Content[index], root.Content[index+1]

		switch {
		case !policyKept && slices.Contains([]string{"preset", "tools", "tools_off"}, key.Value):
		case !fallbacksKept && key.Value == "fallbacks":
			if len(p.Fallbacks) > 0 {
				keys = append(keys, key, sequence(p.Fallbacks))
			}
		case key.Value == "instruction":
			keys = append(keys, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "instructions"}, value)
		default:
			keys = append(keys, key, value)
		}
	}

	if !fallbacksKept && len(source.Fallbacks) == 0 && len(p.Fallbacks) > 0 {
		keys = append(keys, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "fallbacks"}, sequence(p.Fallbacks))
	}

	if !policyKept {
		var policy yaml.Node
		if err := policy.Encode(struct {
			Preset string   `yaml:"preset"`
			On     []string `yaml:"tools,omitempty"`
			Off    []string `yaml:"tools_off,omitempty"`
		}{p.Preset, p.Tools, p.ToolsOff}); err != nil {
			return p.Source
		}

		keys = append(keys, policy.Content...)
	}

	root.Content = keys
	raw, err := yaml.Marshal(&doc)
	if err != nil {
		return p.Source
	}

	return string(raw)
}

// sequence is a list of names as a YAML flow sequence, as the editor writes one.
func sequence(names []string) *yaml.Node {
	node := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
	for _, name := range names {
		node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name})
	}

	return node
}

// withAvailability says of each profile whether a worker can start under it
// now, from the scheduler's own view of the nodes and accounts (Placement).
func (s *Store) withAvailability(ctx context.Context, profiles []Profile) error {
	if s.placement == nil {
		return nil
	}

	return s.placement.SetAvailability(ctx, profiles)
}

// ListProfiles returns every profile's current version, by name.
func (s *Store) ListProfiles(ctx context.Context) ([]Profile, error) {
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
func (s *Store) ListProfileVersions(ctx context.Context, name string) ([]Profile, error) {
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
	if runtime == RoleRuntime {
		runtime = ""
	}
	return Profile{
		Name: v.Profile, Description: v.Description, VersionID: v.ID, Version: int(v.Version), Runtime: runtime,
		AgentSpec: AgentSpec{
			Provider: v.Provider.String, Model: v.Model.String, Effort: v.Effort.String,
			Instructions: v.Instructions.String, Fallbacks: splitNames(v.Fallbacks), Skills: splitNames(v.Skills),
			SkillsOff: splitNames(v.SkillsOff), Preset: v.Preset, Tools: splitNames(v.Tools),
			ToolsOff: splitNames(v.ToolsOff),
		},
		Source: v.Source, CreatedAt: storeutil.Millis(v.CreatedAt),
		CPURequestMillis: v.CpuRequestMillis.Int64, MemoryRequestBytes: v.MemoryRequestBytes.Int64,
		MemoryLimitBytes: v.MemoryLimitBytes.Int64,
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

// RestoreVersionTx points a profile at what one of its versions was, as a new
// version, in the caller's transaction: the undo of a change the main Bot made
// (setupchanges.go). Versions never change and their numbers only rise. What
// it was may no longer be served, its model or a fallback: that is refused
// rather than put back a profile that cannot run.
func (s *Store) RestoreVersionTx(ctx context.Context, q *store.Queries, now int64, name, beforeID string) error {
	before, err := q.GetProfileVersion(ctx, beforeID)
	if err != nil {
		return storeutil.NotFound(err, "profile version", beforeID)
	}

	current, err := q.CurrentProfileVersion(ctx, name)
	if err != nil {
		return storeutil.NotFound(err, "profile", name)
	}

	if _, err := CheckModelTx(ctx, q, before.Runtime, before.Provider.String, before.Model.String,
		before.Effort.String); err != nil {
		return fmt.Errorf("profile %s as it was: %w", name, err)
	}

	// A fallback it named may no longer be served (issue #1336).
	if before.Runtime != RoleRuntime {
		if err := CheckFallbacksTx(ctx, q, before.Runtime, before.Provider.String, before.Model.String,
			splitNames(before.Fallbacks)); err != nil {
			return fmt.Errorf("%w: profile %s as it was: %w", storeerr.ErrConflict, name, err)
		}
	}

	id := s.newID("pfv")

	if err := q.SetProfileVersion(ctx, store.SetProfileVersionParams{Name: name, VersionID: id, Now: now}); err != nil {
		return fmt.Errorf("pointing profile %s at what it was: %w", name, err)
	}

	return q.InsertProfileVersion(ctx, store.InsertProfileVersionParams{
		ID: id, Profile: name, Version: current.Version + 1, Source: before.Source, Digest: before.Digest,
		Description: before.Description, Runtime: before.Runtime, Provider: before.Provider, Model: before.Model,
		Effort: before.Effort, Instructions: before.Instructions, Fallbacks: before.Fallbacks, Skills: before.Skills,
		SkillsOff: before.SkillsOff, Preset: before.Preset, Tools: before.Tools, ToolsOff: before.ToolsOff,
		CpuRequestMillis: before.CpuRequestMillis, MemoryRequestBytes: before.MemoryRequestBytes,
		MemoryLimitBytes: before.MemoryLimitBytes, Now: now,
	})
}

// RemoveTx removes a profile with its versions in the caller's transaction: the undo of one the main Bot made
// (setupchanges.go), which checked first that nothing used it.
func RemoveTx(ctx context.Context, q *store.Queries, name string) error {
	if err := q.DeleteProfileVersions(ctx, name); err != nil {
		return fmt.Errorf("removing the versions of profile %s: %w", name, err)
	}

	return q.DeleteProfile(ctx, name)
}

// CheckSkillsTx checks the skills a profile or a Bot adds and turns off: each
// one that exists, named once, and not in both lists.
func CheckSkillsTx(ctx context.Context, q *store.Queries, add, off []string) error {
	seen := map[string]bool{}

	for _, name := range slices.Concat(add, off) {
		if seen[name] {
			return fmt.Errorf("%w: skill %q is named twice", storeerr.ErrInvalid, name)
		}

		seen[name] = true

		if _, err := q.GetSkill(ctx, name); err != nil {
			return fmt.Errorf("skills: %w", storeutil.NotFound(err, "skill", name))
		}
	}

	return nil
}

// CheckTools checks the tool switches of a profile or a Bot.
func CheckTools(preset string, on, off []string) error {
	switch agenttool.DefaultPreset(preset) {
	case agenttool.PresetFull, agenttool.PresetReport:
	default:
		return fmt.Errorf("%w: preset %q must be full or report", storeerr.ErrInvalid, preset)
	}

	if err := agenttool.CheckToolSwitches(on, off); err != nil {
		return fmt.Errorf("%w: tools: %w", storeerr.ErrInvalid, err)
	}

	return nil
}
