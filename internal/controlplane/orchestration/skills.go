package orchestration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
	"github.com/kl3inIT/hypomnema/internal/shared/githost"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
	"github.com/kl3inIT/hypomnema/internal/shared/skillpack"
)

// Skills (issue #21): a skill the owner adds is kept whole in revisions that
// never change, after Omnara's skill revisions; the same folder added again
// makes no new revision, as OpenClaw's identical saves.

// SkillUpload is where a skill given as an archive came from.
const SkillUpload = "upload"

// SkillBuiltin begins the source of a revision the control plane published
// from its own binary ("builtin:v0.9.42"), issue #492.
const SkillBuiltin = "builtin"

// builtinSource says whether a revision's source is a built-in one.
func builtinSource(source string) bool {
	return source == SkillBuiltin || strings.HasPrefix(source, SkillBuiltin+":")
}

// Skill is a skill at its newest revision; Revisions are every one, newest
// first, when the skill was read alone.
type Skill struct {
	Name string
	// Everyone gives it to every worker and Bot that does not turn it off.
	Everyone bool
	// BuiltIn is a skill the control plane's binary ships: its first revision
	// is a built-in one. It is switched like any other and never removed.
	BuiltIn   bool
	Latest    SkillRevision
	Revisions []SkillRevision
	CreatedAt time.Time
	UpdatedAt time.Time
}

// SkillRevision is one revision of a skill.
type SkillRevision struct {
	Revision    int
	Description string
	Digest      string
	Size        int64
	Files       int
	// Source is "upload" or "github:owner/repo[/path]@ref"; Commit is the
	// commit the ref was, for a repository.
	Source string
	Commit string
	// Requires is what an agent must have to be given this revision
	// ("integration:superset"), read from its SKILL.md (issue #536).
	Requires  []string
	CreatedAt time.Time
}

// SkillSource says where a skill came from.
type SkillSource struct {
	Source string
	Commit string
}

// AddSkill keeps a skill as its next revision, or, when its files are those
// of its newest one, keeps nothing and says so.
func (s *Service) AddSkill(ctx context.Context, sk skillpack.Skill, from SkillSource) (Skill, bool, error) {
	archive, err := sk.Archive()
	if err != nil {
		return Skill{}, false, fmt.Errorf("packing skill %s: %w", sk.Name, err)
	}

	added := false

	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		if err := q.InsertSkill(ctx, store.InsertSkillParams{Name: sk.Name, Now: now}); err != nil {
			return err
		}

		latest, err := q.LatestSkillRevision(ctx, sk.Name)

		switch {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return err
		case latest.Digest == sk.Digest:
			return nil
		}

		added = true

		if err := q.InsertSkillRevision(ctx, store.InsertSkillRevisionParams{
			Skill: sk.Name, Revision: latest.Revision + 1, Description: sk.Description, Digest: sk.Digest,
			Size: sk.Size(), Files: int64(len(sk.Files)), Source: from.Source, SourceCommit: nullString(from.Commit),
			Archive: archive, Requires: nullString(strings.Join(sk.Requires, ",")), Now: now,
		}); err != nil {
			return err
		}

		return q.TouchSkill(ctx, store.TouchSkillParams{Name: sk.Name, Now: now})
	}); err != nil {
		return Skill{}, false, fmt.Errorf("keeping skill %s: %w", sk.Name, err)
	}

	got, err := s.GetSkill(ctx, sk.Name)

	return got, added, err
}

// ListSkills returns every skill at its newest revision, by name.
func (s *Service) ListSkills(ctx context.Context) ([]Skill, error) {
	rows, err := s.read().ListSkills(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing skills: %w", err)
	}

	out := make([]Skill, len(rows))
	for i, r := range rows {
		out[i] = Skill{
			Name: r.Name, Everyone: r.Everyone == 1, BuiltIn: builtinSource(r.FirstSource),
			CreatedAt: millis(r.CreatedAt), UpdatedAt: millis(r.UpdatedAt),
			Latest: SkillRevision{
				Revision: int(r.Revision), Description: r.Description, Digest: r.Digest, Size: r.Size,
				Files: int(r.Files), Source: r.Source, Commit: r.SourceCommit.String,
				Requires: splitNames(r.Requires.String), CreatedAt: millis(r.RevisedAt),
			},
		}
	}

	return out, nil
}

// GetSkill returns a skill with every revision.
func (s *Service) GetSkill(ctx context.Context, name string) (Skill, error) {
	q := s.read()

	row, err := q.GetSkill(ctx, name)
	if err != nil {
		return Skill{}, notFound(err, "skill", name)
	}

	revs, err := q.ListSkillRevisions(ctx, name)
	if err != nil {
		return Skill{}, fmt.Errorf("listing the revisions of skill %s: %w", name, err)
	}

	out := Skill{
		Name: row.Name, Everyone: row.Everyone == 1, CreatedAt: millis(row.CreatedAt), UpdatedAt: millis(row.UpdatedAt),
		Revisions: make([]SkillRevision, len(revs)),
	}

	for i, r := range revs {
		out.Revisions[i] = SkillRevision{
			Revision: int(r.Revision), Description: r.Description, Digest: r.Digest, Size: r.Size, Files: int(r.Files),
			Source: r.Source, Commit: r.SourceCommit.String, Requires: splitNames(r.Requires.String),
			CreatedAt: millis(r.CreatedAt),
		}
	}

	if len(out.Revisions) > 0 {
		out.Latest = out.Revisions[0]
		out.BuiltIn = builtinSource(out.Revisions[len(out.Revisions)-1].Source)
	}

	return out, nil
}

// SetSkillEveryone gives a skill to every worker and Bot, or takes it back.
func (s *Service) SetSkillEveryone(ctx context.Context, name string, on bool) (Skill, error) {
	every := int64(0)
	if on {
		every = 1
	}

	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		n, err := q.SetSkillEveryone(ctx, store.SetSkillEveryoneParams{Everyone: every, Now: now, Name: name})
		if err != nil {
			return err
		}

		if n == 0 {
			return fmt.Errorf("%w: skill %s", ErrNotFound, name)
		}

		return nil
	}); err != nil {
		return Skill{}, fmt.Errorf("setting skill %s for everyone: %w", name, err)
	}

	return s.GetSkill(ctx, name)
}

// DeleteSkill removes a skill and its revisions, unless a profile names it,
// as Omnara refuses while an agent config does, or it is built in. What a
// Dispatch was given stays recorded.
func (s *Service) DeleteSkill(ctx context.Context, name string) error {
	return s.write(ctx, func(q *store.Queries, _ int64) error {
		first, err := q.FirstSkillSource(ctx, name)
		if err != nil {
			return notFound(err, "skill", name)
		}

		// The next start would publish it again: a built-in skill is turned off, as OpenMausBot's bundled ones
		// are (server/skill-library.ts, editable: false) and Codex's system skills (install_system_skills).
		if builtinSource(first) {
			return fmt.Errorf("%w: skill %s is built in; turn it off for everyone instead", ErrConflict, name)
		}

		profiles, err := q.ProfilesNamingSkill(ctx, nullString(name))
		if err != nil {
			return fmt.Errorf("finding the profiles of skill %s: %w", name, err)
		}

		if len(profiles) > 0 {
			return fmt.Errorf("%w: profiles %s name skill %s; take it out of them first", ErrConflict,
				strings.Join(profiles, ", "), name)
		}

		bots, err := q.BotsNamingSkill(ctx, nullString(name))
		if err != nil {
			return fmt.Errorf("finding the bots of skill %s: %w", name, err)
		}

		if len(bots) > 0 {
			return fmt.Errorf("%w: bots %s name skill %s; take it out of them first", ErrConflict,
				strings.Join(bots, ", "), name)
		}

		n, err := q.DeleteSkill(ctx, name)
		if err != nil {
			return fmt.Errorf("removing skill %s: %w", name, err)
		}

		if n == 0 {
			return fmt.Errorf("%w: skill %s", ErrNotFound, name)
		}

		return nil
	})
}

// recordSkills gives a new Dispatch its skills at their newest revisions:
// those for everyone that its profile version does not turn off, and those it
// adds, each whose requirements its Run meets (workerHas).
func recordSkills(ctx context.Context, q *store.Queries, dispatchID, versionID string, run store.Run) error {
	var add, off []string

	if versionID != "" {
		v, err := q.GetProfileVersion(ctx, versionID)
		if err != nil {
			return notFound(err, "profile version", versionID)
		}

		add, off = splitNames(v.Skills), splitNames(v.SkillsOff)
	}

	switches, err := dispatchToolSwitches(ctx, q, dispatchID)
	if err != nil {
		return err
	}

	has, err := workerHas(ctx, q, run, switches.AllowsFamily("phone"))
	if err != nil {
		return err
	}

	given, err := givenSkills(ctx, q, add, off, has)
	if err != nil {
		return err
	}

	for _, sk := range given {
		if err := q.InsertDispatchSkill(ctx, store.InsertDispatchSkillParams{
			DispatchID: dispatchID, Skill: sk.Name, Revision: int64(sk.Revision), Digest: sk.Digest,
		}); err != nil {
			return fmt.Errorf("recording skill %s of dispatch %s: %w", sk.Name, dispatchID, err)
		}
	}

	return nil
}

// givenSkills are the skills an agent that adds add and turns off off gets,
// at their newest revisions: those for everyone it does not turn off, and
// those it adds, but only those whose every requirement it has: a guide of
// an Integration goes only to an agent that may use a Connection of it
// (issue #536), as OpenClaw leaves out a skill whose requires it does not
// meet, whatever its allowlists say (src/skills/loading/config.ts,
// shouldIncludeSkill), and OpenMausBot selects a library skill only when
// every requiredCapability is available (server/skill-library.ts,
// selectBundledSkills).
func givenSkills(ctx context.Context, q *store.Queries, add, off []string, has map[string]bool,
) ([]nodeproto.Skill, error) {
	latest, err := q.LatestSkillRevisions(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the skills: %w", err)
	}

	var out []nodeproto.Skill

	for _, r := range latest {
		requires := splitNames(r.Requires.String)

		switch {
		case !slices.Contains(add, r.Name) && (r.Everyone != 1 || slices.Contains(off, r.Name)):
		case slices.ContainsFunc(requires, func(req string) bool { return !has[req] }):
		default:
			out = append(out, nodeproto.Skill{
				Name: r.Name, Revision: int(r.Revision), Digest: r.Digest, Requires: requires,
			})
		}
	}

	return out, nil
}

// workerHas is what a worker of a Run has for a skill's requirements: the
// Integrations of the Connections its Run was given, whose commands the node
// prepares for it (agenttool.ForDispatch), and the phone while a node holds
// one, as each offer then names it.
func workerHas(ctx context.Context, q *store.Queries, run store.Run, phoneAllowed bool) (map[string]bool, error) {
	has := map[string]bool{}

	for _, use := range splitUses(run.Uses) {
		if integration, _, ok := SplitUse(use); ok {
			has[skillpack.Requirement(skillpack.RequiresIntegration, integration)] = true
		}
	}

	return has, phoneHas(ctx, q, has, phoneAllowed)
}

// botHas is what a Bot has for a skill's requirements: the Integrations of
// which it may use a Connection now (Bot.MayUse: switched on for chats and
// on its list; the owner's own sign-in among them, though it is used only on
// his turn), as the agent API reads them at each call, and the phone while a node holds one.
// A Connection of a git host is of its kind too: a Bot given one of a GitLab
// host has gitlab, whose tool acts on any of them (issue #740).
func botHas(ctx context.Context, q *store.Queries, b Bot) (map[string]bool, error) {
	return skillCapabilities(ctx, q, b.MayUse, b.ToolSwitches().AllowsFamily("phone"))
}

// SkillCapabilities describes the requirements a Bot meets or a profile could meet on a Run.
func (s *Service) SkillCapabilities(ctx context.Context, bot *Bot, switches agenttool.ToolSwitches,
) (map[string]bool, error) {
	if bot != nil {
		return botHas(ctx, s.read(), *bot)
	}

	return skillCapabilities(ctx, s.read(), nil, switches.AllowsFamily("phone"))
}

func skillCapabilities(ctx context.Context, q *store.Queries, mayUse func(Connection) bool, phoneAllowed bool,
) (map[string]bool, error) {
	conns, err := q.ListConnections(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the connections: %w", err)
	}

	has := map[string]bool{}

	for _, row := range conns {
		c := connectionFromRow(row)
		if mayUse != nil && !mayUse(c) {
			continue
		}

		has[skillpack.Requirement(skillpack.RequiresIntegration, c.Integration)] = true

		if !githost.ValidHost(c.Integration) {
			continue
		}

		if h, err := gitHostOf(ctx, q, c.Integration); err == nil {
			has[skillpack.Requirement(skillpack.RequiresIntegration, string(h.Kind))] = true
		}
	}

	return has, phoneHas(ctx, q, has, phoneAllowed)
}

// phoneHas adds the phone when a node holds one, the node the control plane
// relays every phone tool to (internal/controlplane/phone).
func phoneHas(ctx context.Context, q *store.Queries, has map[string]bool, allowed bool) error {
	if !allowed {
		return nil
	}

	nodes, err := q.ListNodes(ctx)
	if err != nil {
		return fmt.Errorf("reading the nodes: %w", err)
	}

	for _, n := range nodes {
		if n.Device == DeviceAndroid {
			has[skillpack.Requirement(skillpack.RequiresDevice, DeviceAndroid)] = true
		}
	}

	return nil
}

// BotSkills are the skills a Bot gets now, at their newest revisions: its
// launcher puts them where pi reads them when pi starts for a chat.
func (s *Service) BotSkills(ctx context.Context, name string) ([]nodeproto.Skill, error) {
	q := s.read()

	b, err := q.GetBot(ctx, name)
	if err != nil {
		return nil, notFound(err, "bot", name)
	}

	has, err := botHas(ctx, q, botFromRow(b))
	if err != nil {
		return nil, err
	}

	return givenSkills(ctx, q, splitNames(b.Skills), splitNames(b.SkillsOff), has)
}

// SkillArchive returns the archive of one revision of a skill.
func (s *Service) SkillArchive(ctx context.Context, name string, revision int) ([]byte, error) {
	archive, err := s.read().GetSkillArchive(ctx, store.GetSkillArchiveParams{Skill: name, Revision: int64(revision)})
	if err != nil {
		return nil, notFound(err, "skill revision", fmt.Sprintf("%s@%d", name, revision))
	}

	return archive, nil
}

func dispatchSkills(ctx context.Context, q *store.Queries, dispatchID string) ([]nodeproto.Skill, error) {
	rows, err := q.ListDispatchSkills(ctx, dispatchID)
	if err != nil {
		return nil, fmt.Errorf("reading the skills of dispatch %s: %w", dispatchID, err)
	}

	out := make([]nodeproto.Skill, len(rows))
	for i, r := range rows {
		out[i] = nodeproto.Skill{
			Name: r.Skill, Revision: int(r.Revision), Digest: r.Digest, Requires: splitNames(r.Requires.String),
		}
	}

	return out, nil
}

// SkillArchiveForNode returns the archive of a skill revision a Dispatch on
// the node that has not ended was given, and nothing else: a node fetches
// only what an offer it holds names.
func (s *Service) SkillArchiveForNode(ctx context.Context, nodeID, digest string) ([]byte, error) {
	archive, err := s.read().SkillArchiveForNode(ctx, store.SkillArchiveForNodeParams{Digest: digest, NodeID: nodeID})
	if err != nil {
		return nil, notFound(err, "skill revision", digest)
	}

	return archive, nil
}

// BuiltinPublish is what publishing one built-in skill did.
type BuiltinPublish struct {
	Name string
	// Did is "added", "updated", "current", "edited" (the newest revision is
	// the owner's, which is kept) or "owner's" (the owner added a skill of that
	// name before the binary shipped one, which is left alone).
	Did string
}

// PublishBuiltinSkills puts the skills the control plane's binary ships into
// the store, each as a revision whose source is source ("builtin:<release>"),
// at every start (issue #492). A new one is given to everyone, as Codex's
// system skills reach every session and OpenMausBot's bundled skills every
// bot; its switch is the owner's after that. One whose files changed gets the
// next revision, as Hermes's skills_sync.py updates a bundled skill by its
// origin hash, unless the newest revision is one the owner added (Hermes
// skips a user-modified skill), and a skill of that name the owner added is
// never touched (Hermes never overwrites a same-named user skill,
// tools/skills_sync.py).
func (s *Service) PublishBuiltinSkills(ctx context.Context, skills []skillpack.Skill, source string,
) ([]BuiltinPublish, error) {
	if !builtinSource(source) {
		return nil, fmt.Errorf("%w: a built-in skill's source is %s:<release>", ErrInvalid, SkillBuiltin)
	}

	out := make([]BuiltinPublish, 0, len(skills))

	for _, sk := range skills {
		did, err := s.publishBuiltinSkill(ctx, sk, source)
		if err != nil {
			return out, err
		}

		out = append(out, BuiltinPublish{Name: sk.Name, Did: did})
	}

	return out, nil
}

func (s *Service) publishBuiltinSkill(ctx context.Context, sk skillpack.Skill, source string) (string, error) {
	archive, err := sk.Archive()
	if err != nil {
		return "", fmt.Errorf("packing skill %s: %w", sk.Name, err)
	}

	did := ""

	err = s.write(ctx, func(q *store.Queries, now int64) error {
		revision := int64(1)

		latest, err := q.LatestSkillRevision(ctx, sk.Name)

		switch {
		case errors.Is(err, sql.ErrNoRows):
			did = "added"

			if err := q.InsertSkill(ctx, store.InsertSkillParams{Name: sk.Name, Now: now}); err != nil {
				return err
			}

			every := store.SetSkillEveryoneParams{Everyone: 1, Now: now, Name: sk.Name}
			if _, err := q.SetSkillEveryone(ctx, every); err != nil {
				return err
			}
		case err != nil:
			return err
		default:
			first, err := q.FirstSkillSource(ctx, sk.Name)
			if err != nil {
				return err
			}

			switch {
			case !builtinSource(first):
				did = "owner's"

				return nil
			case latest.Digest == sk.Digest:
				did = "current"

				return nil
			case !builtinSource(latest.Source):
				did = "edited"

				return nil
			}

			did, revision = "updated", latest.Revision+1
		}

		if err := q.InsertSkillRevision(ctx, store.InsertSkillRevisionParams{
			Skill: sk.Name, Revision: revision, Description: sk.Description, Digest: sk.Digest,
			Size: sk.Size(), Files: int64(len(sk.Files)), Source: source, Archive: archive,
			Requires: nullString(strings.Join(sk.Requires, ",")), Now: now,
		}); err != nil {
			return err
		}

		return q.TouchSkill(ctx, store.TouchSkillParams{Name: sk.Name, Now: now})
	})
	if err != nil {
		return "", fmt.Errorf("publishing built-in skill %s: %w", sk.Name, err)
	}

	return did, nil
}
