package clientcli

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/shared/skillpack"
)

// The coordinator's skill, after Orca's (skills/orchestration, `orca skills get|install`): the
// installed SKILL.md is a stub that runs `hypomnema skills get <skill>`, which
// prints the guide compiled into this binary, so the guide always names the
// commands this binary accepts. Rare branches are references loaded alone.
//
// A skill without a guide.md is a whole folder, scripts and references with
// it (issue #458), as Hermes and OpenClaw ship bundled skills: pdf, adapted
// from Hermes's skills/productivity/pdf, whose scripts run from the skill's
// folder; pages (issue #423), adapted from OpenMausBot's
// skills/data-dashboards, whose template is copied from it; and slides
// (issue #479), adapted from zarazhangrui/frontend-slides, whose references
// and script are read from the skill's folder.
//
// So are the guides of the Integrations' tools and of the phone
// (google-workspace, lark, aws, superset, android-device; issue #103, section
// 9, and issue #417), each adapted from its tool's own skills and naming its
// source, whose references are files beside it.
//
// Each reaches every agent kind, Bots and workers alike, through the skills
// store (issues #492, #536): the control plane publishes them as built-in
// revisions at each start (StoreSkills), and `skills install` takes out the
// copies and stubs earlier releases put in ~/.agents/skills, so that a Bot
// gets each once. A guide can declare its Integration, device or tool in
// metadata.hypomnema.requires. Orchestration gives it to agents that meet
// the requirement (givenSkills). Each names tools its agents can use and
// files relative to its own folder. Only
// pages's Routine row names commands (`hypomnema superset chart`, `hypomnema
// artifact show`), which a Routine's script runs
// (docs/decisions/2026-11-15-one-tool-for-every-agent.md).

// The all: prefix keeps files whose names start with _ (pdf's _raster.py).
//
//go:embed all:skills
var ownSkills embed.FS

// skillFiles are the skills this binary ships.
var skillFiles = ownSkills

// defaultSkill is the one skill shipped today.
const defaultSkill = "hypomnema-coordinator"

// releasedStubs holds the SHA-256 of every SKILL.md stub a release shipped,
// per skill, since every install took the current one (issue #1220).
// `skills install` replaces an installed stub only when its digest is one of
// these, so a copy the owner edited is left alone (Orca's releasedDigests).
// A changed stub adds its digest here (TestShippedStubsAreReleased).
var releasedStubs = map[string][]string{
	"hypomnema-coordinator": {
		"f35f29bba09f26acf8be84321b0c0998b6a76b9853c03a8a5e5737528cc4b5f8", // #672: the description says when to use it
	},
}

// storeSkills are the whole-folder skills that reach every agent kind
// through the skills store (issue #492), as Codex gives its system skills to
// every session (codex-rs skills/src/lib.rs, install_system_skills), OpenClaw
// its bundled skills to every agent, Claude CLI runs included
// (src/agents/cli-runner/claude-skills-plugin.ts), and OpenMausBot its
// library's skills to every engine (server/skill-library.ts, everyBot).
// `skills install` does not install them.
// Only a skill that names no command an agent may lack goes here, and every
// whole-folder skill is one (TestEveryFolderSkillIsInTheStore); a guide
// names its Integration's command, which every agent given a Connection of
// it has.
var storeSkills = []string{
	"pdf", "slides", "pages", "google-workspace", "lark", "aws", "superset", "android-device", "github", "gitlab",
	"social-listening",
}

// StoreSkills are the skills the control plane publishes into its skills
// store at each start, read from this binary: what is under scripts/ is
// executable, as `skills install` made it.
func StoreSkills() ([]skillpack.Skill, error) {
	out := make([]skillpack.Skill, 0, len(storeSkills))

	for _, name := range storeSkills {
		tree, err := fs.Sub(skillFiles, path.Join("skills", name))
		if err != nil {
			return nil, fmt.Errorf("reading the %s skill: %w", name, err)
		}

		sk, err := skillpack.FromFS(tree, func(p string) bool { return strings.HasPrefix(p, "scripts/") })
		if err != nil {
			return nil, fmt.Errorf("reading the %s skill: %w", name, err)
		}

		out = append(out, sk)
	}

	return out, nil
}

// folderSkill says whether a skill is installed as a whole folder: it has no
// guide.md for `skills get` to print.
func folderSkill(name string) bool {
	_, err := fs.Stat(skillFiles, path.Join("skills", name, "guide.md"))

	return err != nil
}

func skillsGet(_ context.Context, e *env, fset *flag.FlagSet) error {
	name := fset.Arg(0)
	if name == "" {
		name = defaultSkill
	}

	if folderSkill(name) {
		var guides []string

		for _, n := range skillNames() {
			if !folderSkill(n) {
				guides = append(guides, n)
			}
		}

		return fmt.Errorf("%w: no guide %q; this binary ships the guides of %s", ErrUsage, name, strings.Join(guides, ", "))
	}

	refs, err := references(name)
	if err != nil {
		return err
	}

	switch {
	case boolean(fset, "references"):
		for _, r := range refs {
			e.printf("%s\n", r)
		}

		return nil
	case str(fset, "reference") != "":
		ref := strings.TrimSuffix(path.Base(str(fset, "reference")), ".md")
		if !slices.Contains(refs, ref) {
			return fmt.Errorf("%w: skill %s has no reference %q; it has %s", ErrUsage, name, ref, strings.Join(refs, ", "))
		}

		return e.cat(path.Join("skills", name, "references", ref+".md"))
	}

	if err := e.cat(path.Join("skills", name, "guide.md")); err != nil {
		return err
	}

	if !boolean(fset, "full") {
		return nil
	}

	for _, r := range refs {
		e.printf("\n")

		if err := e.cat(path.Join("skills", name, "references", r+".md")); err != nil {
			return err
		}
	}

	return nil
}

func (e *env) cat(file string) error {
	b, err := skillFiles.ReadFile(file)
	if err != nil {
		return fmt.Errorf("reading %s: %w", file, err)
	}

	_, err = e.out.Write(b)

	return err
}

func skillNames() []string {
	entries, _ := skillFiles.ReadDir("skills")

	var out []string

	for _, d := range entries {
		if d.IsDir() {
			out = append(out, d.Name())
		}
	}

	return out
}

func references(name string) ([]string, error) {
	entries, err := skillFiles.ReadDir(path.Join("skills", name, "references"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}

	if err != nil {
		return nil, fmt.Errorf("listing references of %s: %w", name, err)
	}

	var out []string
	for _, d := range entries {
		out = append(out, strings.TrimSuffix(d.Name(), ".md"))
	}

	return out, nil
}

// userHome is os.UserHomeDir; tests replace it.
var userHome = os.UserHomeDir

// skillRoots are the folders agents read skills from: Claude Code's, and the
// shared ~/.agents/skills that pi and Codex read.
func skillRoots(agent string) (map[string]string, error) {
	home, err := userHome()
	if err != nil {
		return nil, fmt.Errorf("finding the home directory: %w", err)
	}

	claude := filepath.Join(home, ".claude")
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		claude = dir
	}

	all := map[string]string{
		"claude": filepath.Join(claude, "skills"),
		"agents": filepath.Join(home, ".agents", "skills"),
	}

	switch agent {
	case "all":
		return all, nil
	case "claude", "agents":
		return map[string]string{agent: all[agent]}, nil
	default:
		return nil, fmt.Errorf("%w: --agent is claude, agents or all", ErrUsage)
	}
}

func skillsInstall(_ context.Context, e *env, fset *flag.FlagSet) error {
	roots, err := skillRoots(str(fset, "agent"))
	if err != nil {
		return err
	}

	agents := make([]string, 0, len(roots))
	for a := range roots {
		agents = append(agents, a)
	}

	slices.Sort(agents)

	for _, agent := range agents {
		for _, name := range skillNames() {
			if slices.Contains(storeSkills, name) || folderSkill(name) {
				continue
			}

			if err := installStub(e, fset, agent, filepath.Join(roots[agent], name), name); err != nil {
				return err
			}
		}
	}

	return nil
}

// installStub writes one skill's stub into dir, unless an edited copy is
// there and --force is not given.
func installStub(e *env, fset *flag.FlagSet, agent, dir, name string) error {
	stub, err := skillFiles.ReadFile(path.Join("skills", name, "SKILL.md"))
	if err != nil {
		return fmt.Errorf("reading the %s stub: %w", name, err)
	}

	target := filepath.Join(dir, "SKILL.md")
	dry, force := boolean(fset, "dry-run"), boolean(fset, "force")

	old, err := os.ReadFile(target) //nolint:gosec // the path is the agent's own skill folder
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("reading %s: %w", target, err)
	}

	action := "install"

	switch {
	case err != nil:
	case digest(old) == digest(stub):
		e.printf("%s: %s is current\n", agent, target)

		return nil
	case slices.Contains(releasedStubs[name], digest(old)):
		action = "update"
	case !force:
		e.printf("%s: %s was edited; left as it is (--force replaces it and keeps a backup)\n", agent, target)

		return nil
	default:
		action = "replace the edited"
	}

	if dry {
		e.printf("%s: would %s %s\n", agent, action, target)

		return nil
	}

	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("making %s: %w", dir, err)
	}

	if action == "replace the edited" {
		if err := os.WriteFile(target+".bak", old, 0o600); err != nil { //nolint:gosec // beside the agent's own skill file
			return fmt.Errorf("backing up %s: %w", target, err)
		}
	}

	if err := os.WriteFile(target, stub, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", target, err)
	}

	done := map[string]string{"install": "installed", "update": "updated", "replace the edited": "replaced"}[action]
	e.printf("%s: %s %s\n", agent, done, target)

	return nil
}

// digest is the SHA-256 of a stub with line endings normalized, as Orca
// compares installed copies.
func digest(b []byte) string {
	sum := sha256.Sum256([]byte(strings.ReplaceAll(string(b), "\r\n", "\n")))

	return hex.EncodeToString(sum[:])
}

// BuiltinSkill is a skill this binary ships, as the control plane lists it
// beside the skills the owner added (issue #21).
type BuiltinSkill struct {
	Name        string
	Description string
}

// BuiltinSkills are the skills this binary ships that reach agents by their
// own rule, by name: the coordinator's. The others are in the skills store,
// listed there.
func BuiltinSkills() ([]BuiltinSkill, error) {
	var out []BuiltinSkill

	for _, name := range skillNames() {
		if slices.Contains(storeSkills, name) {
			continue
		}

		stub, err := skillFiles.ReadFile(path.Join("skills", name, "SKILL.md"))
		if err != nil {
			return nil, fmt.Errorf("reading the %s stub: %w", name, err)
		}

		_, description, err := skillpack.Frontmatter(stub)
		if err != nil {
			return nil, fmt.Errorf("the %s stub: %w", name, err)
		}

		out = append(out, BuiltinSkill{Name: name, Description: description})
	}

	return out, nil
}
