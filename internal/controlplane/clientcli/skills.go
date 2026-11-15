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
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
	"github.com/kl3inIT/hypomnema/internal/shared/skillpack"
)

// Skills, after Orca's (skills/orchestration, `orca skills get|install`): the
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
// Each reaches every agent kind, Bots and workers alike, through the skills
// store (issues #492, #536): the control plane publishes them as built-in
// revisions at each start (StoreSkills), and `skills install` takes out the
// copies earlier releases put in ~/.agents/skills, so that a Bot gets each
// once. Each names only tools both kinds have (show_artifact, Superset's
// tools) and its files relative to its own folder; only pages's Routine row
// names commands (`hypomnema superset chart`, `hypomnema artifact show`),
// which a Routine's script runs
// (docs/decisions/2026-11-15-one-tool-for-every-agent.md).

// The all: prefix keeps files whose names start with _ (pdf's _raster.py).
//
//go:embed all:skills
var ownSkills embed.FS

// skillFiles are the skills this binary ships: its own, and those of the
// Integrations' tools, which a node's binary ships too
// (internal/shared/agenttool).
var skillFiles = skillSet{ownSkills, agenttool.Skills}

// skillSet reads from the first of several trees that has a file.
type skillSet []fs.ReadDirFS

func (set skillSet) ReadFile(name string) ([]byte, error) {
	for _, tree := range set {
		if b, err := fs.ReadFile(tree, name); err == nil {
			return b, nil
		}
	}

	return nil, fmt.Errorf("%s: %w", name, fs.ErrNotExist)
}

func (set skillSet) Open(name string) (fs.File, error) {
	for _, tree := range set {
		if f, err := tree.Open(name); err == nil {
			return f, nil
		}
	}

	return nil, fmt.Errorf("%s: %w", name, fs.ErrNotExist)
}

// ReadDir lists a directory in every tree that has it.
func (set skillSet) ReadDir(name string) ([]fs.DirEntry, error) {
	var (
		out   []fs.DirEntry
		found bool
	)

	for _, tree := range set {
		entries, err := tree.ReadDir(name)
		if err != nil {
			continue
		}

		found = true
		out = append(out, entries...)
	}

	if !found {
		return nil, fmt.Errorf("%s: %w", name, fs.ErrNotExist)
	}

	slices.SortFunc(out, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })

	return out, nil
}

// defaultSkill is the one skill shipped today.
const defaultSkill = "hypomnema-coordinator"

// releasedStubs holds the SHA-256 of every SKILL.md stub a release shipped,
// per skill. `skills install` replaces an installed stub only when its
// digest is one of these, so a copy the owner edited is left alone (Orca's
// releasedDigests). A changed stub adds its digest here
// (TestShippedStubsAreReleased).
var releasedStubs = map[string][]string{
	"hypomnema-coordinator": {"a47e4460588d8d58dfc994b34b6aaf8cad11dd7ec13c9151d9188f2c2d587e2a"},
	"google-workspace":      {"129a8091fdeb9d028e8b0550625f2efd54c3eb27b38eba618407d68b82fdd894"},
	"lark": {
		"04115d29ecb6f7c706684dc868052997fe3015a32607711d3e060845b15fb774",
		"f3acc3af4bb9045de0285f5b2fc2ce7a648117f89f3aeaedb0577578951a6c88",
	},
	"superset": {
		"b7cbafe434fcc81b8305bfe6a5c811bd33168b3125a6139cfa67480edf18f7ef",
		"d224bf62df9bbc768f84b90d9b261db118c46889f8b643f9967655c227193a61", // #536: the superset_* tools
	},
	"aws": {"b038020ed0278ff5213f82d6e67a134fdd4115575899370dbb964183fa6676fd"},
	"android-device": {
		"9983ee10554a557330d7e5f7f0fa4079fe17c55eafbe923e425ac95fe0224127",
		"354020b3fc5f96ee9be92432902284298b21164c8cc37c3510906f1f6efe7df4", // #536: Bots have the phone too
	},
}

// retiredStubs holds, as releasedStubs does, the digests of the stubs of the
// skills a release shipped and this binary no longer does. `skills install`
// removes an installed copy whose digest is one of these, as Codex replaces
// its whole system skills folder at an update (codex-rs skills/src/lib.rs,
// install_system_skills) and OpenMausBot takes a switched-off skill's links
// out of each engine's folder (server/skills.ts); a copy the owner edited is
// left, as an edited stub always is. web-search and images became the tools
// web_search, web_fetch and generate_image of a Bot's pi (issue #492).
var retiredStubs = map[string][]string{
	"web-search": {"cdf4d06decd3d71394b4353a8a34b98853765b5f8c88b7e1712159d6cf9be35b"},
	"images":     {"d5914e30a9da434727d0ce057216b1c48a1b3104f23b9c7072f3feeda31e4971"},
}

// releasedFolders holds the folderDigest of every release of a skill an
// earlier release installed as a whole folder in ~/.agents/skills, as
// releasedStubs does for stubs (Hermes's .bundled_manifest origin hashes,
// tools/skills_sync.py): `skills install` removes an installed copy only when
// its digest is one of these, now that the store places them (storeSkills).
var releasedFolders = map[string][]string{
	"pages": {
		"4d876aa95c82401380832a67a0a51d435845bcfc70b27d543330b617bc32a2b1", // #423
		"c9c4d76decceacba2d9ef276cde823ed9296e4a39f142d09d4a26935498e2ed7", // #536: the show_artifact tool
	},
	"pdf": {
		"c945c0d5aaeae2eb9abaf15af44d5c640060785bd86b7b1ad6821bbb8fd8cb2b", // #458
		"5a21174acf72f04f8a75d55451d35094ff66a46117f42ce6f77202ce3dd0f020", // #474: JSON samples, pdf_read modes, a TTF for Vietnamese
	},
	"slides": {"ae1d255cf5ebc308b35005cffa50c005e9f70495347388ce89fb8b06861e1f6f"}, // #479
}

// storeSkills are the whole-folder skills that reach every agent kind
// through the skills store (issue #492), as Codex gives its system skills to
// every session (codex-rs skills/src/lib.rs, install_system_skills), OpenClaw
// its bundled skills to every agent, Claude CLI runs included
// (src/agents/cli-runner/claude-skills-plugin.ts), and OpenMausBot its
// library's skills to every engine (server/skill-library.ts, everyBot).
// `skills install` no longer installs them; it removes a copy it installed.
// Only a skill that names no command an agent may lack goes here, and every
// whole-folder skill is one (TestEveryFolderSkillIsInTheStore).
var storeSkills = []string{"pdf", "slides", "pages"}

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
		for _, name := range slices.Sorted(maps.Keys(retiredStubs)) {
			if err := removeRetired(e, fset, agent, filepath.Join(roots[agent], name), name); err != nil {
				return err
			}
		}

		for _, name := range skillNames() {
			var err error

			switch {
			case slices.Contains(storeSkills, name):
				if agent == "agents" {
					err = removeRetiredFolder(e, fset, agent, filepath.Join(roots[agent], name), name)
				}
			case !folderSkill(name):
				err = installStub(e, fset, agent, filepath.Join(roots[agent], name), name)
			}

			if err != nil {
				return err
			}
		}
	}

	return nil
}

// removeRetired takes out the installed stub of a skill this binary no
// longer ships, and its folder when nothing else is in it, unless the stub
// was edited.
func removeRetired(e *env, fset *flag.FlagSet, agent, dir, name string) error {
	target := filepath.Join(dir, "SKILL.md")

	old, err := os.ReadFile(target) //nolint:gosec // the path is the agent's own skill folder

	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("reading %s: %w", target, err)
	case !slices.Contains(retiredStubs[name], digest(old)):
		e.printf("%s: %s was edited; left as it is, though this binary no longer ships %s\n", agent, target, name)

		return nil
	case boolean(fset, "dry-run"):
		e.printf("%s: would remove %s, which this binary no longer ships\n", agent, target)

		return nil
	}

	if err := os.Remove(target); err != nil {
		return fmt.Errorf("removing %s: %w", target, err)
	}

	// A folder the owner put other files in stays with them.
	if entries, err := os.ReadDir(dir); err == nil && len(entries) == 0 {
		if err := os.Remove(dir); err != nil {
			return fmt.Errorf("removing %s: %w", dir, err)
		}
	}

	e.printf("%s: removed %s, which this binary no longer ships\n", agent, target)

	return nil
}

// removeRetiredFolder takes out a whole-folder skill an earlier release
// installed and the skills store now places (storeSkills), while its digest
// is a released one (releasedFolders), as removeRetired does a stub: an
// edited copy is left and named, as Hermes's skills_sync.py skips a
// user-modified skill (tools/skills_sync.py).
func removeRetiredFolder(e *env, fset *flag.FlagSet, agent, dir, name string) error {
	info, err := os.Lstat(dir)

	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("reading %s: %w", dir, err)
	}

	have := ""
	if info.IsDir() {
		if have, err = folderDigest(os.DirFS(dir)); err != nil {
			return fmt.Errorf("reading %s: %w", dir, err)
		}
	}

	switch {
	case !slices.Contains(releasedFolders[name], have):
		e.printf("%s: %s was edited; left as it is, though %s now comes from the skills store\n", agent, dir, name)

		return nil
	case boolean(fset, "dry-run"):
		e.printf("%s: would remove %s, which now comes from the skills store\n", agent, dir)

		return nil
	}

	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("removing %s: %w", dir, err)
	}

	e.printf("%s: removed %s, which now comes from the skills store\n", agent, dir)

	return nil
}

// runtimeCaches are folders a skill's scripts leave behind when they run,
// which say nothing of an edit (Hermes's _RUNTIME_CACHE_DIRS).
var runtimeCaches = []string{"__pycache__", ".pytest_cache", ".mypy_cache", ".ruff_cache"}

// folderDigest is the SHA-256 of a skill folder's files, each by its path
// and its content with line endings normalized, in path order; runtime
// caches are left out (Hermes's dir_hash and _is_runtime_cache).
func folderDigest(skill fs.FS) (string, error) {
	h := sha256.New()

	err := fs.WalkDir(skill, ".", func(name string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir() && slices.Contains(runtimeCaches, d.Name()):
			return fs.SkipDir
		case d.IsDir():
			return nil
		case bytecode(skill, name):
			return nil
		case !d.Type().IsRegular():
			h.Write([]byte(name + "\x00not a file\x00"))

			return nil
		}

		b, err := fs.ReadFile(skill, name)
		if err != nil {
			return err
		}

		b = []byte(strings.ReplaceAll(string(b), "\r\n", "\n"))
		h.Write([]byte(name + "\x00" + strconv.Itoa(len(b)) + "\x00"))
		h.Write(b)

		return nil
	})
	if err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// bytecode says whether a file is Python's compiled copy of a source beside it.
func bytecode(skill fs.FS, name string) bool {
	ext := path.Ext(name)

	return (ext == ".pyc" || ext == ".pyo") && exists(skill, strings.TrimSuffix(name, ext)+".py")
}

func exists(fsys fs.FS, name string) bool {
	_, err := fs.Stat(fsys, name)

	return err == nil
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
// own rule, by name: the coordinator's and those of the Integrations' tools.
// pdf, slides and pages are in the skills store, listed there.
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
