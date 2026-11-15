package clientcli

import (
	"encoding/json"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
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
// folder the slides skill goes into.
func installHome(t *testing.T) (*world, string) {
	t.Helper()

	home := t.TempDir()
	userHome = func() (string, error) { return home, nil }

	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Cleanup(func() { userHome = os.UserHomeDir })

	return &world{home: filepath.Join(home, ".hypomnema")}, filepath.Join(home, ".agents", "skills", "slides")
}

// Scenario: the coordinator's deploy installs the slides skill as files
// where every Bot reads skills, file for file, its script executable; a
// second install changes nothing, and the caches its script leaves do not
// make it an edited copy.
func TestAFolderSkillIsInstalledAsFiles(t *testing.T) { //nolint:paralleltest // replaces userHome
	w, dir := installHome(t)

	out, err := w.cmd(t, "skills", "install", "--agent", "agents")
	require.NoError(t, err)
	assert.Contains(t, out, "agents: installed "+dir)

	shipped, err := fs.Sub(skillFiles, "skills/slides")
	require.NoError(t, err)

	files := 0
	err = fs.WalkDir(shipped, ".", func(name string, d fs.DirEntry, err error) error {
		require.NoError(t, err)

		if d.IsDir() {
			return nil
		}

		files++

		want, err := fs.ReadFile(shipped, name)
		require.NoError(t, err)

		got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name))) //nolint:gosec // the test's own temporary home
		require.NoError(t, err)
		assert.Equal(t, string(want), string(got), name)

		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, 7, files, "LICENSE, SKILL.md, four references and extract-pptx.py")

	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(dir, "scripts", "extract-pptx.py"))
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o700), info.Mode().Perm(), "a script is executable")

		info, err = os.Stat(filepath.Join(dir, "SKILL.md"))
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}

	cache := filepath.Join(dir, "scripts", "__pycache__")
	require.NoError(t, os.Mkdir(cache, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(cache, "extract-pptx.cpython-314.pyc"), []byte("bytecode"), 0o600))

	out, err = w.cmd(t, "skills", "install", "--agent", "agents")
	require.NoError(t, err)
	assert.Contains(t, out, "agents: "+dir+" is current")
	assertNoStaging(t, filepath.Dir(dir))
}

// Scenario: the owner edited the installed slides skill. The deploy leaves it
// alone; --force replaces it and keeps the edited copy where pi does not
// read it.
func TestAnEditedFolderSkillIsLeftAlone(t *testing.T) { //nolint:paralleltest // replaces userHome
	w, dir := installHome(t)

	_, err := w.cmd(t, "skills", "install", "--agent", "agents")
	require.NoError(t, err)

	skill := filepath.Join(dir, "SKILL.md")
	require.NoError(t, os.WriteFile(skill, []byte("my own notes"), 0o600))

	out, err := w.cmd(t, "skills", "install", "--agent", "agents")
	require.NoError(t, err)
	assert.Contains(t, out, "was edited")

	got, err := os.ReadFile(skill) //nolint:gosec // the test's own temporary home
	require.NoError(t, err)
	assert.Equal(t, "my own notes", string(got))

	out, err = w.cmd(t, "skills", "install", "--agent", "agents", "--force", "--dry-run")
	require.NoError(t, err)
	assert.Contains(t, out, "would replace the edited "+dir)

	out, err = w.cmd(t, "skills", "install", "--agent", "agents", "--force")
	require.NoError(t, err)
	assert.Contains(t, out, "replaced "+dir)

	got, err = os.ReadFile(skill) //nolint:gosec // the test's own temporary home
	require.NoError(t, err)
	assert.Contains(t, string(got), "name: slides")

	backup, err := os.ReadFile(filepath.Join(filepath.Dir(dir), ".slides.bak", "SKILL.md"))
	require.NoError(t, err)
	assert.Equal(t, "my own notes", string(backup))
	assertNoStaging(t, filepath.Dir(dir))
}

// Scenario: a copy an earlier release installed is replaced by this
// release's, with no backup left.
func TestAReleasedFolderSkillIsUpdated(t *testing.T) { //nolint:paralleltest // replaces userHome and releasedFolders
	w, dir := installHome(t)

	_, err := w.cmd(t, "skills", "install", "--agent", "agents")
	require.NoError(t, err)

	// What an earlier release shipped: one reference fewer and another SKILL.md.
	require.NoError(t, os.Remove(filepath.Join(dir, "animation-patterns.md")))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: slides\n---\nolder"), 0o600))

	old, err := folderDigest(os.DirFS(dir))
	require.NoError(t, err)

	released := releasedFolders["slides"]
	releasedFolders["slides"] = append(append([]string{}, released...), old)

	t.Cleanup(func() { releasedFolders["slides"] = released })

	out, err := w.cmd(t, "skills", "install", "--agent", "agents")
	require.NoError(t, err)
	assert.Contains(t, out, "agents: updated "+dir)
	assert.FileExists(t, filepath.Join(dir, "animation-patterns.md"))

	got, err := folderDigest(os.DirFS(dir))
	require.NoError(t, err)

	shipped, err := fs.Sub(skillFiles, "skills/slides")
	require.NoError(t, err)

	want, err := folderDigest(shipped)
	require.NoError(t, err)
	assert.Equal(t, want, got)
	assert.NoDirExists(t, filepath.Join(filepath.Dir(dir), ".slides.bak"))
	assertNoStaging(t, filepath.Dir(dir))
}

func assertNoStaging(t *testing.T, root string) {
	t.Helper()

	entries, err := os.ReadDir(root)
	require.NoError(t, err)

	for _, e := range entries {
		assert.NotContains(t, e.Name(), ".new-", "a staging folder was left")
	}
}

// A folder that changes adds its digest to releasedFolders, so `skills
// install` can later update copies of it.
func TestShippedFoldersAreReleased(t *testing.T) {
	t.Parallel()

	_, folders := shippedSkills()
	require.Contains(t, folders, "slides")

	for _, name := range folders {
		skill, err := fs.Sub(skillFiles, "skills/"+name)
		require.NoError(t, err)

		d, err := folderDigest(skill)
		require.NoError(t, err)
		assert.Contains(t, releasedFolders[name], d, "add the folder's digest to releasedFolders[%q]", name)
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
	assert.Contains(t, string(text), "S=~/.agents/skills/slides", "the files are named where they are installed")
	assert.Contains(t, string(text), "hypomnema artifact show", "a deck is shown as an artifact")
	assert.Contains(t, string(text), "name: slides\n")
}

// The pages skill as shipped (issue #423): OpenMausBot's data-dashboards
// with its license, naming none of OpenMausBot's own tools, and a template
// that loads nothing from the network or the browser's storage, so it runs
// in the artifact's locked frame.
func TestTheShippedPagesSkillWorksInTheLockedFrame(t *testing.T) {
	t.Parallel()

	_, folders := shippedSkills()
	require.Contains(t, folders, "pages", "a Bot's skill: it names hypomnema artifact show, which a worker lacks")

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
	assert.Contains(t, string(text), "~/.agents/skills/pages/dashboard.html",
		"the template is named where it is installed")
	assert.Contains(t, string(text), "hypomnema artifact show")

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

// Scenario: pdf now comes from the skills store (issue #492). The deploy's
// install takes out the copy an earlier release put in ~/.agents/skills, so
// that a Bot gets it once, and does not install it; a copy the owner edited
// stays. pages, a Bot's skill, is still installed there.
func TestAStoreSkillIsTakenOutOfTheAgentsFolder(t *testing.T) { //nolint:paralleltest // replaces globals
	w, slides := installHome(t)
	agents := filepath.Dir(slides)
	pdf, pages := filepath.Join(agents, "pdf"), filepath.Join(agents, "pages")

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
	assert.FileExists(t, filepath.Join(pages, "dashboard.html"), "pages is still installed here")
	assert.DirExists(t, slides, "slides is still installed here")

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

// The skill the control plane publishes into the store is pdf as this
// binary ships it, whole, their scripts executable as `skills
// install` made them; the list of built-in skills leaves them to the store.
func TestTheStoreSkillsAreTheShippedFolders(t *testing.T) {
	t.Parallel()

	skills, err := StoreSkills()
	require.NoError(t, err)
	require.Len(t, skills, 1)
	assert.Equal(t, "pdf", skills[0].Name)

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
