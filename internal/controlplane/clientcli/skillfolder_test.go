package clientcli

import (
	"encoding/json"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/skillpack"
)

// shippedSkills splits the skills `skills install` installs into those
// installed as a stub with a guide and those installed as a whole folder;
// the skills the store places (storeSkills) are neither.
func shippedSkills() (stubs, folders []string) {
	for _, name := range skillNames() {
		switch {
		case slices.Contains(storeSkills, name):
		case folderSkill(name):
			folders = append(folders, name)
		default:
			stubs = append(stubs, name)
		}
	}

	return stubs, folders
}

// installHome points the installer at a temporary home and returns the
// folder pi and Codex read skills from, ~/.agents/skills.
func installHome(t *testing.T) (*world, string) {
	t.Helper()

	home := t.TempDir()
	userHome = func() (string, error) { return home, nil }

	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Cleanup(func() { userHome = os.UserHomeDir })

	return &world{home: filepath.Join(home, ".hypomnema")}, filepath.Join(home, ".agents", "skills")
}

// Every whole-folder skill reaches agents through the skills store, which
// `skills install` does not place (issue #536): one outside it would reach
// no agent. The digests of the copies earlier releases installed are kept so
// that the install can take them out.
func TestEveryFolderSkillIsInTheStore(t *testing.T) {
	t.Parallel()

	_, folders := shippedSkills()
	assert.Empty(t, folders, "add the skill to storeSkills")

	for name := range releasedFolders {
		assert.Contains(t, storeSkills, name)
	}
}

// The pdf skill as shipped: Hermes's files without the parts left out
// (OCR, nano-pdf editing, its tests) and without Hermes's own commands;
// every script and reference it names is there, with its MIT license.
func TestTheShippedPdfSkillNamesOnlyWhatItShips(t *testing.T) {
	t.Parallel()

	skill, err := fs.Sub(skillFiles, "skills/pdf")
	require.NoError(t, err)
	assert.True(t, exists(skill, "LICENSE"))

	removed := regexp.MustCompile(
		`(?i)hermes|ocr-extraction|nano-pdf|extract_pymupdf|extract_marker|tests/|\bterminal\b|vision_analyze`)
	named := regexp.MustCompile(`(?:scripts/|\$S/|\b)(pdf_[a-z_]+\.py|_raster\.py)|references/([a-z-]+\.md)`)

	err = fs.WalkDir(skill, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || name == "LICENSE" {
			return err
		}

		b, err := fs.ReadFile(skill, name)
		if err != nil {
			return err
		}

		assert.Empty(t, removed.FindAllString(string(b), -1), "%s names what is left out", name)

		for _, m := range named.FindAllStringSubmatch(string(b), -1) {
			file := "scripts/" + m[1]
			if m[2] != "" {
				file = "references/" + m[2]
			}

			assert.True(t, exists(skill, file), "%s names %s, which is not shipped", name, file)
		}

		return nil
	})
	require.NoError(t, err)

	text, err := fs.ReadFile(skill, "SKILL.md")
	require.NoError(t, err)
	assert.Contains(t, string(text), `S="<this skill's folder>/scripts"`,
		"the scripts are named beside SKILL.md, wherever the store places it")
	assert.NotContains(t, string(text), "~/.agents", "the store places it elsewhere for each agent kind")
}

// Every JSON sample the pdf skill shows parses, so a Bot that copies one gets
// a spec the scripts read: the fenced ```json blocks of its Markdown and the
// spec a script's docstring shows (issue #474).
func TestThePdfSkillsJSONSamplesParse(t *testing.T) {
	t.Parallel()

	skill, err := fs.Sub(skillFiles, "skills/pdf")
	require.NoError(t, err)

	fenced := regexp.MustCompile("(?s)```json\\r?\\n(.*?)```")
	docstring := regexp.MustCompile(`(?ms)^\{\r?$.*?^\}\r?$`)
	samples := 0

	err = fs.WalkDir(skill, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		b, err := fs.ReadFile(skill, name)
		if err != nil {
			return err
		}

		var found []string

		switch path.Ext(name) {
		case ".md":
			for _, m := range fenced.FindAllStringSubmatch(string(b), -1) {
				found = append(found, m[1])
			}
		case ".py":
			found = docstring.FindAllString(string(b), -1)
		}

		for i, sample := range found {
			assert.True(t, json.Valid([]byte(sample)), "%s: JSON sample %d does not parse:\n%s", name, i+1, sample)
		}

		samples += len(found)

		return nil
	})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, samples, 4,
		"the samples of SKILL.md, references/forms.md, pdf_create.py and pdf_make_form.py")
}

// The slides skill as shipped: frontend-slides without what needs the
// network, a host or a Mac (Vercel, Fontshare and Google Fonts, the PDF
// export, the bold template pack, the Claude Code plugin), with no
// localStorage in its code; every file it names is shipped, with its MIT
// license, and named where it is installed.
func TestTheShippedSlidesSkillNamesOnlyWhatItShips(t *testing.T) {
	t.Parallel()

	skill, err := fs.Sub(skillFiles, "skills/slides")
	require.NoError(t, err)
	assert.True(t, exists(skill, "LICENSE"))

	removed := regexp.MustCompile(`(?i)https?://|vercel|fontshare|fonts\.googleapis|google fonts|deploy\.sh|` +
		`export-pdf|bold-template-pack|design\.md|preview\.md|\.claude-plugin|plugins/|\bbrew\b|\bnpx\b|` +
		`pip install \w|localStorage\.|setItem|\.frontend-slides|\bopen \[`)
	named := regexp.MustCompile(`\]\(([^)#]+)\)|\$S/([\w./-]+\.(?:md|css|py))`)

	err = fs.WalkDir(skill, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || name == "LICENSE" {
			return err
		}

		b, err := fs.ReadFile(skill, name)
		if err != nil {
			return err
		}

		assert.Empty(t, removed.FindAllString(string(b), -1), "%s names what is left out", name)

		for _, m := range named.FindAllStringSubmatch(string(b), -1) {
			file := m[1] + m[2]
			assert.True(t, exists(skill, file), "%s names %s, which is not shipped", name, file)
		}

		return nil
	})
	require.NoError(t, err)

	text, err := fs.ReadFile(skill, "SKILL.md")
	require.NoError(t, err)
	assert.Contains(t, string(text), `S="<this skill's folder>"`,
		"the files are named beside SKILL.md, wherever each agent kind's skills are placed")
	assert.Contains(t, string(text), "`show_artifact`", "a deck is shown as an artifact, with the tool both kinds have")
	assert.NotContains(t, string(text), "hypomnema ", "it names no command a worker lacks")
	assert.Contains(t, string(text), "name: slides\n")
}

// The pages skill as shipped (issue #423): OpenMausBot's data-dashboards
// with its license, naming none of OpenMausBot's own tools, and a template
// that loads nothing from the network or the browser's storage, so it runs
// in the artifact's locked frame.
func TestTheShippedPagesSkillWorksInTheLockedFrame(t *testing.T) {
	t.Parallel()

	require.Contains(t, storeSkills, "pages", "every agent kind's: it names tools both kinds have")

	skill, err := fs.Sub(skillFiles, "skills/pages")
	require.NoError(t, err)
	assert.True(t, exists(skill, "LICENSE"))
	assert.False(t, exists(skill, "manifest.json"))

	removed := regexp.MustCompile(
		`(?i)show_html|publish_page|data_sql|data_show|data_export|mausbot|duckdb|echarts|tailwind|lucide|\bjsx\b|parquet`)

	for _, name := range []string{"SKILL.md", "dashboard.html"} {
		b, err := fs.ReadFile(skill, name)
		require.NoError(t, err)
		assert.Empty(t, removed.FindAllString(string(b), -1), "%s names what OpenMausBot has and Hypomnema has not", name)
	}

	text, err := fs.ReadFile(skill, "SKILL.md")
	require.NoError(t, err)
	assert.Contains(t, string(text), "name: pages")
	assert.Contains(t, string(text), "`dashboard.html` is beside this SKILL.md, in this skill's folder",
		"the template is named beside the skill, wherever each agent kind's skills are placed")
	assert.NotContains(t, string(text), "~/.agents")
	assert.Contains(t, string(text), "the `show_artifact` tool", "a page is shown with the tool")
	assert.Contains(t, string(text), "`superset_chart_data`", "a chart's rows are read with the tool")
	assert.Contains(t, string(text), "runs `hypomnema artifact show --id <id> --path dashboard`",
		"a Routine's script runs the command, having no tools")

	// Only the Routine's row names commands: a script runs them.
	for line := range strings.SplitSeq(string(text), "\n") {
		if strings.Contains(line, "hypomnema ") {
			assert.Contains(t, line, "Routine", "a command a worker lacks: %s", line)
		}
	}

	page, err := fs.ReadFile(skill, "dashboard.html")
	require.NoError(t, err)

	// The SVG namespace is a name, not an address the page loads.
	for _, u := range regexp.MustCompile(`(?i)(?:https?:)?//[a-z0-9.-]+\.[a-z]{2,}`).FindAllString(string(page), -1) {
		assert.Equal(t, "http://www.w3.org", u, "the template loads nothing from another host")
	}

	external := regexp.MustCompile(
		`(?i)<script[^>]+\bsrc=|<link[^>]+rel="?stylesheet|@import|@font-face|localStorage|sessionStorage|indexedDB|document\.cookie`)
	assert.Empty(t, external.FindAllString(string(page), -1), "the locked frame has no network, storage or cookies")
	assert.Less(t, len(page), 256*1024, "a page shown with --html-file is at most 256 KB")
}

// webSearchStub is the stub of the web-search skill as releases until issue
// #492 installed it.
var webSearchStub = strings.Join([]string{
	"---",
	"name: web-search",
	"description: >-",
	"  Searches the web and reads a web page with Hypomnema's own web commands, through the control plane: each " +
		"result's",
	"  title, address, date and snippet, or a page as markdown. Use when the owner asks about something that may " +
		"have",
	"  changed since your training (news, prices, releases, a person or a company today), names a page to read, " +
		"or asks",
	"  you to look something up online. Not for the owner's own mail, files, Lark or Superset, which have their " +
		"skills.",
	"---",
	"",
	"# Web search",
	"",
	"This stub loads the guide that ships with the `hypomnema` binary on this machine, so the guide names exactly " +
		"the",
	"commands that binary accepts.",
	"",
	"1. Run `hypomnema skills get web-search` once in the session and follow the guide it prints.",
	"",
}, "\n")

// Scenario: a release no longer ships the web-search and images skills
// (issue #492). The setup script's install, which every update runs, takes
// out the stubs an earlier release put in each agent's folder, and their
// folders; a stub the owner edited stays, as does a folder he put other
// files in.
func TestARetiredSkillIsRemovedAtInstall(t *testing.T) { //nolint:paralleltest // replaces userHome
	w, _ := installHome(t)
	home := filepath.Dir(w.home)
	agents := filepath.Join(home, ".agents", "skills")
	claude := filepath.Join(home, ".claude", "skills")

	write := func(path, content string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	}

	write(filepath.Join(agents, "web-search", "SKILL.md"), webSearchStub)
	write(filepath.Join(claude, "web-search", "SKILL.md"), strings.ReplaceAll(webSearchStub, "\n", "\r\n"))
	write(filepath.Join(claude, "web-search", "notes.md"), "the owner's notes")
	write(filepath.Join(agents, "images", "SKILL.md"), "---\nname: images\n---\nmy own way to draw")

	out, err := w.cmd(t, "skills", "install", "--dry-run")
	require.NoError(t, err)
	assert.Contains(t, out, "agents: would remove "+filepath.Join(agents, "web-search", "SKILL.md"))
	assert.FileExists(t, filepath.Join(agents, "web-search", "SKILL.md"))

	out, err = w.cmd(t, "skills", "install")
	require.NoError(t, err)
	assert.Contains(t, out, "agents: removed "+filepath.Join(agents, "web-search", "SKILL.md"))
	assert.NoDirExists(t, filepath.Join(agents, "web-search"))
	assert.NoFileExists(t, filepath.Join(claude, "web-search", "SKILL.md"))
	assert.FileExists(t, filepath.Join(claude, "web-search", "notes.md"), "a folder with the owner's files stays")
	assert.Contains(t, out, "was edited; left as it is, though this binary no longer ships images")
	assert.FileExists(t, filepath.Join(agents, "images", "SKILL.md"))

	out, err = w.cmd(t, "skills", "install")
	require.NoError(t, err)
	assert.NotContains(t, out, "removed", "a second install has nothing to take out")
}

// supersetStub is the stub of the superset guide as releases until issue
// #536 installed it.
var supersetStub = strings.Join([]string{
	"---",
	"name: superset",
	"description: >-",
	"  Reads the figures of the owner's Superset dashboards with Hypomnema's superset_* tools: finds a dashboard by its",
	"  title, lists its charts and filters, and returns a chart's rows as the dashboard shows them, as JSON or CSV, with",
	"  the SQL Superset ran. Can also send any other request to Superset's API. Use when the owner asks for a number, a",
	"  table or a list that is on a Superset dashboard, or to copy a chart's data somewhere. Not for drawing charts and",
	"  not for pictures of a dashboard.",
	"---",
	"",
	"# Superset",
	"",
	"This stub loads the guide that ships with the `hypomnema` binary on this machine.",
	"",
	"1. Run `hypomnema skills get superset` once in the session and follow the guide it prints.",
	"",
	"When there are no `superset_*` tools, or a call says you were not given the account, tell the owner: the account is",
	"connected in Settings, Integrations, and switched on there for chats.",
	"",
}, "\n")

// Scenario: the Integrations' guides now come from the skills store (issue
// #536). The deploy's install takes out the stubs an earlier release put in
// each agent's folder, which ran `hypomnema skills get`, and installs none;
// the coordinator's stub stays its own.
func TestAGuideStubIsTakenOutAtInstall(t *testing.T) { //nolint:paralleltest // replaces userHome
	w, agents := installHome(t)

	require.Contains(t, retiredStubs["superset"], digest([]byte(supersetStub)))
	require.NoError(t, os.MkdirAll(filepath.Join(agents, "superset"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(agents, "superset", "SKILL.md"), []byte(supersetStub), 0o600))

	out, err := w.cmd(t, "skills", "install", "--agent", "agents")
	require.NoError(t, err)
	assert.Contains(t, out, "agents: removed "+filepath.Join(agents, "superset", "SKILL.md"))
	assert.NoDirExists(t, filepath.Join(agents, "superset"))
	assert.FileExists(t, filepath.Join(agents, "hypomnema-coordinator", "SKILL.md"))

	for _, guide := range []string{"google-workspace", "lark", "aws", "superset", "android-device"} {
		assert.NoDirExists(t, filepath.Join(agents, guide), "the store places it, not the install")
	}

	out, err = w.cmd(t, "skills", "install", "--agent", "agents")
	require.NoError(t, err)
	assert.NotContains(t, out, "removed", "a second install has nothing to take out")
	assert.NotContains(t, out, "edited")
}

// Scenario: pdf, slides and pages now come from the skills store (issues
// #492, #536). The deploy's install takes out the copies an earlier release
// put in ~/.agents/skills, so that a Bot gets each once, and does not install
// them; a copy the owner edited stays.
func TestAStoreSkillIsTakenOutOfTheAgentsFolder(t *testing.T) { //nolint:paralleltest // replaces globals
	w, agents := installHome(t)
	pdf := filepath.Join(agents, "pdf")

	// slides and pages as the releases before issue #536 installed them.
	var olds []string

	for _, name := range []string{"slides", "pages"} {
		dir := filepath.Join(agents, name)
		require.NoError(t, os.MkdirAll(dir, 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: "+name+"\n---\nold"), 0o600))

		old, err := folderDigest(os.DirFS(dir))
		require.NoError(t, err)

		released := releasedFolders[name]
		releasedFolders[name] = append(append([]string{}, released...), old)

		t.Cleanup(func() { releasedFolders[name] = released })

		olds = append(olds, dir)
	}

	// What an earlier release installed: the folder whose digest it released.
	require.NoError(t, os.MkdirAll(filepath.Join(pdf, "scripts"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(pdf, "SKILL.md"), []byte("---\nname: pdf\n---\nold"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(pdf, "scripts", "pdf_read.py"), []byte("print()"), 0o600))

	old, err := folderDigest(os.DirFS(pdf))
	require.NoError(t, err)

	released := releasedFolders["pdf"]
	releasedFolders["pdf"] = append(append([]string{}, released...), old)

	t.Cleanup(func() { releasedFolders["pdf"] = released })

	out, err := w.cmd(t, "skills", "install", "--agent", "agents", "--dry-run")
	require.NoError(t, err)
	assert.Contains(t, out, "agents: would remove "+pdf)
	assert.DirExists(t, pdf)

	out, err = w.cmd(t, "skills", "install", "--agent", "agents")
	require.NoError(t, err)
	assert.Contains(t, out, "agents: removed "+pdf+", which now comes from the skills store")
	assert.NoDirExists(t, pdf)

	for _, dir := range olds {
		assert.Contains(t, out, "agents: removed "+dir+", which now comes from the skills store")
		assert.NoDirExists(t, dir)
	}

	require.NoError(t, os.MkdirAll(pdf, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(pdf, "SKILL.md"), []byte("my own pdf"), 0o600))

	out, err = w.cmd(t, "skills", "install", "--agent", "agents")
	require.NoError(t, err)
	assert.Contains(t, out, "agents: "+pdf+" was edited; left as it is")
	assert.FileExists(t, filepath.Join(pdf, "SKILL.md"))

	require.NoError(t, os.RemoveAll(pdf))

	out, err = w.cmd(t, "skills", "install", "--agent", "agents")
	require.NoError(t, err)
	assert.NotContains(t, out, "pdf")
	assert.NoDirExists(t, pdf, "the store places it, not the install")
}

// The skills the control plane publishes into the store are the folders
// this binary ships, whole, their scripts executable as `skills install`
// made them, each guide requiring what it teaches; the list of built-in
// skills leaves them to the store.
func TestTheStoreSkillsAreTheShippedFolders(t *testing.T) {
	t.Parallel()

	skills, err := StoreSkills()
	require.NoError(t, err)

	names := make([]string, len(skills))
	requires := map[string][]string{}

	for i, sk := range skills {
		names[i] = sk.Name
		requires[sk.Name] = sk.Requires
	}

	assert.Equal(t, []string{"pdf", "slides", "pages", "google-workspace", "lark", "aws", "superset", "android-device"},
		names)

	// A guide goes only to an agent that may use its Integration, or while a node holds the phone (issue #536).
	assert.Equal(t, map[string][]string{
		"pdf": nil, "slides": nil, "pages": nil,
		"google-workspace": {"integration:google"}, "lark": {"integration:lark"}, "aws": {"integration:aws"},
		"superset": {"integration:superset"}, "android-device": {"device:android"},
	}, requires)

	for _, sk := range skills {
		shipped, err := fs.Sub(skillFiles, "skills/"+sk.Name)
		require.NoError(t, err)

		files := 0
		require.NoError(t, fs.WalkDir(shipped, ".", func(_ string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				files++
			}

			return err
		}))
		assert.Len(t, sk.Files, files, sk.Name)

		for _, f := range sk.Files {
			assert.Equal(t, strings.HasPrefix(f.Path, "scripts/"), f.Exec, f.Path)
		}

		// A node and a Bot's sync check the archive they fetch against this digest.
		archive, err := sk.Archive()
		require.NoError(t, err)

		back, err := skillpack.FromArchive(archive)
		require.NoError(t, err)
		assert.Equal(t, sk.Digest, back.Digest, sk.Name)
	}

	builtin, err := BuiltinSkills()
	require.NoError(t, err)

	for _, b := range builtin {
		assert.NotContains(t, storeSkills, b.Name)
	}
}
