package agenttool

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Skills are the guides that teach an agent each Integration's tool, one
// directory an Integration's skill: a stub (SKILL.md), the guide (guide.md)
// and its references. They are taken from the references and adapted, with
// each file naming its source (issue #103, section 9).
//
//go:embed skills
var Skills embed.FS

// SkillOf names the skill of an Integration.
func SkillOf(integration string) (string, bool) {
	switch integration {
	case "google":
		return "google-workspace", true
	case "lark":
		return "lark", true
	}

	return "", false
}

// EnvSkills is the directory an agent on a node finds its skills' guides in.
const EnvSkills = "HYPOMNEMA_SKILLS"

// Place is where a machine keeps the tools and what goes with them, under
// one home: the real tools (ToolPath), the scripts agents call them by, and
// the skills' guides.
type Place struct {
	// Home is the directory everything is under.
	Home string
	// Exe is the binary whose `tool` command the scripts run.
	Exe string
}

// Bin is the directory to put first on an agent's PATH.
func (p Place) Bin() string { return filepath.Join(p.Home, "tools", "bin") }

// SkillsDir is the directory the guides are written to.
func (p Place) SkillsDir() string { return filepath.Join(p.Home, "skills") }

// Prepare writes the script of each tool and the guide of each skill the
// Integrations need. It can be run again: a file is rewritten whole. The
// real tools are installed apart, by ToolInstaller.
func (p Place) Prepare(integrations []string) error {
	for _, integration := range integrations {
		def, ok := Of(integration)
		if !ok {
			continue
		}

		if err := writeWhole(filepath.Join(p.Bin(), def.Name), []byte(Shim(p.Exe, def.Name)), 0o700); err != nil {
			return fmt.Errorf("writing the script of %s: %w", def.Name, err)
		}

		if skill, ok := SkillOf(integration); ok {
			if err := p.writeSkill(skill); err != nil {
				return err
			}
		}
	}

	return nil
}

// writeSkill copies a skill's guide and references, not its stub: an agent
// on a node is pointed at the guide itself.
func (p Place) writeSkill(skill string) error {
	root := "skills/" + skill

	return fs.WalkDir(Skills, root, func(file string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || entry.Name() == "SKILL.md" {
			return err
		}

		raw, err := Skills.ReadFile(file)
		if err != nil {
			return err
		}

		target := filepath.Join(p.SkillsDir(), skill, filepath.FromSlash(strings.TrimPrefix(file, root+"/")))
		if err := writeWhole(target, raw, 0o600); err != nil {
			return fmt.Errorf("writing the %s skill: %w", skill, err)
		}

		return nil
	})
}

// writeWhole writes a file beside its place and renames it into it, so that
// an agent never reads half of one.
func writeWhole(target string, content []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}

	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, content, mode); err != nil {
		return err
	}

	return os.Rename(tmp, target)
}

// Env is what an agent's environment gains to use its Connections: the
// scripts first on PATH, where the launcher finds the control plane and the
// real tools, the agent's key, and where the guides are.
func (p Place) Env(url, key, path string) []string {
	return []string{
		"PATH=" + p.Bin() + string(os.PathListSeparator) + path,
		EnvURL + "=" + url, EnvKey + "=" + key, EnvTools + "=" + p.Home, EnvSkills + "=" + p.SkillsDir(),
	}
}

// IntegrationsOf names the Integrations of Connections ("google:work"), each
// once.
func IntegrationsOf(uses []string) []string {
	var out []string

	for _, use := range uses {
		integration, _, _ := strings.Cut(use, ":")
		if integration != "" && !slices.Contains(out, integration) {
			out = append(out, integration)
		}
	}

	return out
}

// Brief is what an agent is told about the accounts it may use: which, the
// command for each Integration, and where its guide is. It goes into a
// worker's first message.
func Brief(uses []string) string {
	if len(uses) == 0 {
		return ""
	}

	var b strings.Builder

	b.WriteString("\nAccounts you may use for this task: " + strings.Join(uses, ", ") + ".\n")

	for _, integration := range IntegrationsOf(uses) {
		def, ok := Of(integration)
		if !ok {
			continue
		}

		fmt.Fprintf(&b, "- %s: the `%s` command is on your PATH and already signed in; never log in yourself. ",
			integration, def.Name)

		if skill, ok := SkillOf(integration); ok {
			fmt.Fprintf(&b, "Read \"$%s/%s/guide.md\" before the first call; its references are in the folder "+
				"beside it. ", EnvSkills, skill)
		}

		b.WriteString("With several accounts of it, name one on each call: `" + def.Name + " --account <label> ...`.\n")
	}

	return b.String()
}

// ForDispatch prepares a node for a Dispatch that may use Connections and
// returns what the worker's environment gains. With no Connection it does
// nothing and adds nothing. home is the node's home, where its tools are
// installed; exe the node's binary; key the Dispatch's own.
func ForDispatch(home, exe, url string, uses []string, key string) ([]string, error) {
	if len(uses) == 0 {
		return nil, nil
	}

	if url == "" || key == "" {
		return nil, fmt.Errorf("the offer names accounts (%s) but the node has no key or control plane address "+
			"to get their tokens with", strings.Join(uses, ", "))
	}

	place := Place{Home: home, Exe: exe}
	if err := place.Prepare(IntegrationsOf(uses)); err != nil {
		return nil, err
	}

	return place.Env(url, key, os.Getenv("PATH")), nil
}
