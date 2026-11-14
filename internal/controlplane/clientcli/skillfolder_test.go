package clientcli

import (
	"encoding/json"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// shippedSkills splits the skills this binary ships into those installed as
// a stub with a guide and those installed as a whole folder.
func shippedSkills() (stubs, folders []string) {
	for _, name := range skillNames() {
		if folderSkill(name) {
			folders = append(folders, name)
		} else {
			stubs = append(stubs, name)
		}
	}

	return stubs, folders
}

// installHome points the installer at a temporary home and returns the
// folder the pdf skill goes into.
func installHome(t *testing.T) (*world, string) {
	t.Helper()

	home := t.TempDir()
	userHome = func() (string, error) { return home, nil }

	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Cleanup(func() { userHome = os.UserHomeDir })

	return &world{home: filepath.Join(home, ".hypomnema")}, filepath.Join(home, ".agents", "skills", "pdf")
}

// Scenario: the coordinator's deploy installs the pdf skill as files where
// every Bot reads skills, its scripts executable; a second install changes
// nothing, and the caches its scripts leave do not make it an edited copy.
func TestAFolderSkillIsInstalledAsFiles(t *testing.T) { //nolint:paralleltest // replaces userHome
	w, dir := installHome(t)

	out, err := w.cmd(t, "skills", "install", "--agent", "agents")
	require.NoError(t, err)
	assert.Contains(t, out, "agents: installed "+dir)

	shipped, err := fs.Sub(skillFiles, "skills/pdf")
	require.NoError(t, err)

	err = fs.WalkDir(shipped, ".", func(name string, d fs.DirEntry, err error) error {
		require.NoError(t, err)

		if d.IsDir() {
			return nil
		}

		want, err := fs.ReadFile(shipped, name)
		require.NoError(t, err)

		got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name))) //nolint:gosec // the test's own temporary home
		require.NoError(t, err)
		assert.Equal(t, string(want), string(got), name)

		return nil
	})
	require.NoError(t, err)

	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(dir, "scripts", "pdf_read.py"))
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o700), info.Mode().Perm(), "a script is executable")

		info, err = os.Stat(filepath.Join(dir, "SKILL.md"))
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}

	cache := filepath.Join(dir, "scripts", "__pycache__")
	require.NoError(t, os.Mkdir(cache, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(cache, "_raster.cpython-314.pyc"), []byte("bytecode"), 0o600))

	out, err = w.cmd(t, "skills", "install", "--agent", "agents")
	require.NoError(t, err)
	assert.Contains(t, out, "agents: "+dir+" is current")
	assertNoStaging(t, filepath.Dir(dir))
}

// Scenario: the owner edited the installed pdf skill. The deploy leaves it
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
	assert.Contains(t, string(got), "name: pdf")

	backup, err := os.ReadFile(filepath.Join(filepath.Dir(dir), ".pdf.bak", "SKILL.md"))
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

	// What an earlier release shipped: one script fewer and another SKILL.md.
	require.NoError(t, os.Remove(filepath.Join(dir, "scripts", "pdf_merge.py")))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: pdf\n---\nolder"), 0o600))

	old, err := folderDigest(os.DirFS(dir))
	require.NoError(t, err)

	released := releasedFolders["pdf"]
	releasedFolders["pdf"] = append(append([]string{}, released...), old)

	t.Cleanup(func() { releasedFolders["pdf"] = released })

	out, err := w.cmd(t, "skills", "install", "--agent", "agents")
	require.NoError(t, err)
	assert.Contains(t, out, "agents: updated "+dir)
	assert.FileExists(t, filepath.Join(dir, "scripts", "pdf_merge.py"))

	got, err := folderDigest(os.DirFS(dir))
	require.NoError(t, err)

	shipped, err := fs.Sub(skillFiles, "skills/pdf")
	require.NoError(t, err)

	want, err := folderDigest(shipped)
	require.NoError(t, err)
	assert.Equal(t, want, got)
	assert.NoDirExists(t, filepath.Join(filepath.Dir(dir), ".pdf.bak"))
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
	require.Contains(t, folders, "pdf")

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
	assert.Contains(t, string(text), "S=~/.agents/skills/pdf/scripts", "the scripts are named where they are installed")
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
