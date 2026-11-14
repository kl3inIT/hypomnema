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
// A skill without a guide.md is a whole folder installed as files, scripts
// and references with it (issue #458), as Hermes and OpenClaw ship bundled
// skills: pdf, adapted from Hermes's skills/productivity/pdf, and slides,
// adapted from zarazhangrui/frontend-slides (issue #479). Their scripts and
// references are read from the installed folder, so they have no stub.

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
	"lark":                  {"04115d29ecb6f7c706684dc868052997fe3015a32607711d3e060845b15fb774"},
	"superset":              {"b7cbafe434fcc81b8305bfe6a5c811bd33168b3125a6139cfa67480edf18f7ef"},
	"aws":                   {"b038020ed0278ff5213f82d6e67a134fdd4115575899370dbb964183fa6676fd"},
	"web-search":            {"cdf4d06decd3d71394b4353a8a34b98853765b5f8c88b7e1712159d6cf9be35b"},
	"images":                {"d5914e30a9da434727d0ce057216b1c48a1b3104f23b9c7072f3feeda31e4971"},
}

// releasedFolders holds the folderDigest of every release of a skill
// installed as a whole folder, as releasedStubs does for stubs: `skills
// install` replaces an installed folder only when its digest is one of these
// (Hermes's .bundled_manifest origin hashes, tools/skills_sync.py). A changed
// folder adds its digest here (TestShippedFoldersAreReleased).
var releasedFolders = map[string][]string{
	"pdf": {
		"c945c0d5aaeae2eb9abaf15af44d5c640060785bd86b7b1ad6821bbb8fd8cb2b", // #458
		"5a21174acf72f04f8a75d55451d35094ff66a46117f42ce6f77202ce3dd0f020", // #474: JSON samples, pdf_read modes, a TTF for Vietnamese
	},
	"slides": {"ae1d255cf5ebc308b35005cffa50c005e9f70495347388ce89fb8b06861e1f6f"}, // #479
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
			var err error

			switch {
			case !folderSkill(name):
				err = installStub(e, fset, agent, filepath.Join(roots[agent], name), name)
			case agent == "agents":
				// A whole-folder skill names its scripts under ~/.agents/skills,
				// where every Bot reads skills (issue #458), so it goes only there.
				err = installFolder(e, fset, agent, roots[agent], name)
			}

			if err != nil {
				return err
			}
		}
	}

	return nil
}

// installFolder puts one whole-folder skill into root/name, unless an edited
// copy is there and --force is not given. Like Hermes's skills_sync.py, the
// installed copy is replaced only while its digest is a released one, the
// new copy is made beside it first and the old one comes back when the swap
// fails; the folders it makes start with a dot, which pi and Codex skip.
func installFolder(e *env, fset *flag.FlagSet, agent, root, name string) error {
	src := path.Join("skills", name)

	shipped, err := fs.Sub(skillFiles, src)
	if err != nil {
		return fmt.Errorf("reading the %s skill: %w", name, err)
	}

	want, err := folderDigest(shipped)
	if err != nil {
		return fmt.Errorf("reading the %s skill: %w", name, err)
	}

	dir := filepath.Join(root, name)
	dry, force := boolean(fset, "dry-run"), boolean(fset, "force")
	action := "install"

	info, err := os.Lstat(dir)

	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return fmt.Errorf("reading %s: %w", dir, err)
	default:
		have := ""
		if info.IsDir() {
			if have, err = folderDigest(os.DirFS(dir)); err != nil {
				return fmt.Errorf("reading %s: %w", dir, err)
			}
		}

		switch {
		case have == want:
			e.printf("%s: %s is current\n", agent, dir)

			return nil
		case slices.Contains(releasedFolders[name], have):
			action = "update"
		case !force:
			e.printf("%s: %s was edited; left as it is (--force replaces it and keeps a backup)\n", agent, dir)

			return nil
		default:
			action = "replace the edited"
		}
	}

	if dry {
		e.printf("%s: would %s %s\n", agent, action, dir)

		return nil
	}

	backup := filepath.Join(root, "."+name+".bak")
	if err := swapFolder(shipped, root, dir, backup, action != "install"); err != nil {
		return err
	}

	switch action {
	case "replace the edited":
		e.printf("%s: replaced %s; the edited copy is %s\n", agent, dir, backup)
	case "update":
		if err := os.RemoveAll(backup); err != nil {
			return fmt.Errorf("removing %s: %w", backup, err)
		}

		e.printf("%s: updated %s\n", agent, dir)
	default:
		e.printf("%s: installed %s\n", agent, dir)
	}

	return nil
}

// swapFolder writes the skill into a new folder beside dir, then moves the
// old dir to backup (when there is one) and the new folder to dir.
func swapFolder(skill fs.FS, root, dir, backup string, replace bool) error {
	if err := os.MkdirAll(root, 0o750); err != nil {
		return fmt.Errorf("making %s: %w", root, err)
	}

	stage, err := os.MkdirTemp(root, "."+filepath.Base(dir)+".new-")
	if err != nil {
		return fmt.Errorf("making a folder in %s: %w", root, err)
	}

	defer func() { _ = os.RemoveAll(stage) }()

	if err := writeFolder(skill, stage); err != nil {
		return err
	}

	if !replace {
		if err := os.Rename(stage, dir); err != nil {
			return fmt.Errorf("installing %s: %w", dir, err)
		}

		return nil
	}

	if err := os.RemoveAll(backup); err != nil {
		return fmt.Errorf("removing %s: %w", backup, err)
	}

	if err := os.Rename(dir, backup); err != nil {
		return fmt.Errorf("moving %s aside: %w", dir, err)
	}

	if err := os.Rename(stage, dir); err != nil {
		if back := os.Rename(backup, dir); back != nil {
			return fmt.Errorf("installing %s: %w; the old copy is %s", dir, err, backup)
		}

		return fmt.Errorf("installing %s: %w", dir, err)
	}

	return nil
}

// writeFolder copies a skill's files into dir. What is under scripts/ is
// executable.
func writeFolder(skill fs.FS, dir string) error {
	if err := os.Chmod(dir, 0o750); err != nil { //nolint:gosec // the agent's own skill folder, read by its agents
		return fmt.Errorf("making %s: %w", dir, err)
	}

	err := fs.WalkDir(skill, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || name == "." {
			return err
		}

		target := filepath.Join(dir, filepath.FromSlash(name))
		if d.IsDir() {
			return os.Mkdir(target, 0o750)
		}

		b, err := fs.ReadFile(skill, name)
		if err != nil {
			return err
		}

		mode := os.FileMode(0o600)
		if strings.HasPrefix(name, "scripts/") {
			mode = 0o700
		}

		if err := os.WriteFile(target, b, mode); err != nil {
			return err
		}

		// WriteFile's mode passes through the umask; scripts must stay executable.
		return os.Chmod(target, mode)
	})
	if err != nil {
		return fmt.Errorf("writing the skill into %s: %w", dir, err)
	}

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

// BuiltinSkills are the skills this binary ships, by name: the coordinator's,
// those of the Integrations' tools and the whole-folder ones (pdf, slides).
func BuiltinSkills() ([]BuiltinSkill, error) {
	var out []BuiltinSkill

	for _, name := range skillNames() {
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
