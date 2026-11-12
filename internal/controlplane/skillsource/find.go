package skillsource

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/shared/skillpack"
)

// Finding a skill named, not placed, in a repository: skills.sh gives a
// repository and a skill's name. The folder is found as the skills CLI finds
// it (MIT, Copyright (c) 2026 Vercel, Inc., https://github.com/vercel-labs/skills):
// src/blob.ts findSkillMdPaths for where it looks and in which order,
// src/skills.ts filterSkills for how a name matches, src/skills-sh-config.ts
// for the folders a repository declares in skills.sh.json.

// skillsShConfig is the file in which a repository names more folders of skills.
const skillsShConfig = "skills.sh.json"

// containerDepth is how deep a skill is looked for under a known folder of
// skills (the CLI's DEFAULT_SKILL_CONTAINER_DEPTH).
const containerDepth = 3

// skipDirs are never looked through (the CLI's SKIP_DIRS).
var skipDirs = []string{"node_modules", ".git", "dist", "build", "__pycache__"}

// agentSkillDirs are the folders in which the agents the skills CLI knows
// keep a project's skills (its AGENT_PROJECT_SKILL_DIRS: each agent's
// skillsDir and the legacy ones, without "skills", sorted).
var agentSkillDirs = []string{
	".adal/skills", ".agents/skills", ".aider-desk/skills", ".augment/skills", ".autohand/skills",
	".bob/skills", ".claude/skills", ".cline/skills", ".codeartsdoer/skills", ".codebuddy/skills",
	".codemaker/skills", ".codestudio/skills", ".codex/skills", ".commandcode/skills", ".continue/skills",
	".cortex/skills", ".crush/skills", ".devin/skills", ".factory/skills", ".forge/skills",
	".github/skills", ".goose/skills", ".grok/skills", ".hermes/skills", ".iflow/skills",
	".inferencesh/skills", ".jazz/skills", ".junie/skills", ".kilo/skills", ".kilocode/skills",
	".kimchi/skills", ".kiro/skills", ".kode/skills", ".lingma/skills", ".mcpjam/skills",
	".minimax/skills", ".moxby/skills", ".mux/skills", ".neovate/skills", ".ona/skills",
	".opencode/skills", ".openhands/skills", ".pi/skills", ".pochi/skills", ".posit/assistant/skills",
	".qoder/skills", ".qwen/skills", ".reasonix/skills", ".roo/skills", ".rovodev/skills",
	".tabnine/agent/skills", ".terramind/skills", ".tinycloud/skills", ".trae/skills", ".vibe/skills",
	".windsurf/skills", ".zcode/skills", ".zencoder/skills", "agent/skills", "data/skills",
}

// Find reads the repository at the source's ref and gives the source with
// its path set to the folder of the skill named name, so that it is fetched,
// and fetched again later, as any placed skill.
func (g *GitHub) Find(ctx context.Context, src Source, name string) (Source, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Source{}, fmt.Errorf("%w: no skill named", ErrInvalid)
	}

	ref := src.Ref
	if ref == "" {
		ref = "HEAD"
	}

	commit, err := g.commitOf(ctx, src, ref)
	if err != nil {
		return Source{}, err
	}

	resp, err := g.get(ctx, g.Codeload+"/"+src.Owner+"/"+src.Repo+"/tar.gz/"+commit, "")
	if err != nil {
		return Source{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	files, err := skillpack.TarballFiles(&limited{r: resp.Body, left: MaxTarball}, func(p string) bool {
		return isManifest(p) || p == skillsShConfig
	})
	if err != nil {
		return Source{}, fmt.Errorf("reading %s at %s: %w", src, commit[:12], err)
	}

	dir, ok := folderOf(files, name)
	if !ok {
		return Source{}, fmt.Errorf("%w: %s has no skill named %q", ErrInvalid, src, name)
	}

	src.Path = dir

	return src, nil
}

func isManifest(p string) bool {
	return strings.EqualFold(path.Base(p), skillpack.Manifest)
}

// folderOf is the folder of the first skill, in the order the skills CLI
// finds them, whose name is name: written the same, then the same once
// lowercased with spaces and underscores as hyphens.
func folderOf(files []skillpack.File, name string) (string, bool) {
	var (
		manifests []string
		content   = map[string][]byte{}
		extra     []string
	)

	for _, f := range files {
		if f.Path == skillsShConfig {
			extra = skillsShPaths(f.Data)

			continue
		}

		manifests = append(manifests, f.Path)
		content[f.Path] = f.Data
	}

	type found struct{ dir, name string }

	var skills []found

	seen := map[string]bool{}

	for _, p := range manifestOrder(manifests, extra) {
		n, _, err := skillpack.Frontmatter(content[p])
		if err != nil || seen[n] {
			continue
		}

		seen[n] = true
		dir := path.Dir(p)
		if dir == "." {
			dir = ""
		}

		skills = append(skills, found{dir: dir, name: n})
	}

	for _, same := range []func(string) string{strings.ToLower, normalized} {
		for _, s := range skills {
			if same(s.name) == same(name) {
				return s.dir, true
			}
		}
	}

	return "", false
}

var spacesAndUnderscores = regexp.MustCompile(`[\s_]+`)

func normalized(name string) string {
	return spacesAndUnderscores.ReplaceAllString(strings.ToLower(name), "-")
}

// manifestOrder is the CLI's findSkillMdPaths without a subpath: the
// SKILL.md files in the known folders, the root and each folder of skills in
// turn, a skill's own folder or one level below; under a folder of skills
// down to three levels, not under another skill nor a skipped folder; the
// folders skills.sh.json names take only their children. With none there,
// every SKILL.md at most five levels down.
func manifestOrder(manifests, extra []string) []string {
	prefixes := append([]string{"", "skills/", "skills/.curated/", "skills/.experimental/", "skills/.system/"},
		withSlash(agentSkillDirs)...)
	known := len(prefixes)

	for _, e := range withSlash(extra) {
		if !slices.Contains(prefixes, e) {
			prefixes = append(prefixes, e)
		}
	}

	lower := map[string]bool{}
	for _, m := range manifests {
		lower[strings.ToLower(m)] = true
	}

	var out []string

	seen := map[string]bool{}
	add := func(m string) {
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}

	for i, prefix := range prefixes {
		container, declared := prefix != "", i >= known

		for _, m := range manifests {
			rest, ok := strings.CutPrefix(m, prefix)
			if !ok {
				continue
			}

			parts := strings.Split(rest, "/")

			switch {
			case len(parts) == 1:
				if !declared {
					add(m)
				}
			case len(parts) == 2:
				add(m)
			case container && len(parts) <= containerDepth+1 && clearPath(parts[:len(parts)-1]) &&
				!underSkill(prefix, parts[:len(parts)-1], lower):
				add(m)
			}
		}
	}

	if len(out) > 0 {
		return out
	}

	for _, m := range manifests {
		if len(strings.Split(m, "/")) <= 6 {
			out = append(out, m)
		}
	}

	return out
}

func withSlash(dirs []string) []string {
	out := make([]string, 0, len(dirs))
	for _, d := range dirs {
		out = append(out, d+"/")
	}

	return out
}

func clearPath(dirs []string) bool {
	return !slices.ContainsFunc(dirs, func(d string) bool { return slices.Contains(skipDirs, d) })
}

// underSkill is whether a folder above the skill's own has a SKILL.md.
func underSkill(prefix string, dirs []string, lower map[string]bool) bool {
	for i := 1; i < len(dirs); i++ {
		if lower[strings.ToLower(prefix+strings.Join(dirs[:i], "/")+"/SKILL.md")] {
			return true
		}
	}

	return false
}

// skillsShPaths is the CLI's parseSkillsShPaths: the "paths" of
// skills.sh.json that start with ./ and stay in the repository, without the
// root.
func skillsShPaths(content []byte) []string {
	var config struct {
		Paths []any `json:"paths"`
	}

	if json.Unmarshal(content, &config) != nil {
		return nil
	}

	var out []string

	for _, entry := range config.Paths {
		s, ok := entry.(string)
		if !ok || !strings.HasPrefix(s, "./") || strings.Contains(s, `\`) {
			continue
		}

		clean := strings.TrimRight(path.Clean(s), "/")
		if clean == "." || clean == "" || clean == ".." || strings.HasPrefix(clean, "../") || path.IsAbs(clean) {
			continue
		}

		if !slices.Contains(out, clean) {
			out = append(out, clean)
		}
	}

	return out
}
