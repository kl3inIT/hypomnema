package clientcli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/shared/skillpack"
)

// Scenario: the owner adds a skill from an archive on the laptop, adds it
// again unchanged, gives it to everyone, reads it and removes it. A
// coordinator lists it and cannot add one.
func TestSkillsFromTheCLI(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.login(t, bearertoken.Owner)

	archive, err := skillpack.Skill{Files: []skillpack.File{
		{Path: "SKILL.md", Data: []byte("---\nname: review-go\ndescription: Review a Go change.\n---\n")},
		{Path: "scripts/check.sh", Exec: true, Data: []byte("go vet ./...\n")},
	}}.Archive()
	require.NoError(t, err)

	file := filepath.Join(t.TempDir(), "review-go.tar.gz")
	require.NoError(t, os.WriteFile(file, archive, 0o600))

	_, err = w.cmd(t, "skills", "add", "--file", file, "--github", "o/r")
	require.ErrorIs(t, err, ErrUsage)

	out, err := w.cmd(t, "skills", "add", "--file", file)
	require.NoError(t, err)
	assert.Contains(t, out, "skill review-go: revision 1 added")
	assert.Contains(t, out, "skills everyone review-go on", "the owner is told no one has it yet")

	out, err = w.cmd(t, "skills", "add", "--file", file)
	require.NoError(t, err)
	assert.Contains(t, out, "the files are those of revision 1; nothing added")

	out, err = w.cmd(t, "skills", "everyone", "review-go", "on")
	require.NoError(t, err)
	assert.Contains(t, out, "is for every worker and Bot")

	out, err = w.cmd(t, "skills", "show", "review-go")
	require.NoError(t, err)
	assert.Contains(t, out, "review-go\trevision 1\tupload\tfor everyone\n\nReview a Go change.\n")
	assert.Contains(t, out, "2 files")

	pi := &world{svc: w.svc, url: w.url, home: filepath.Join(t.TempDir(), "coordinator")}
	pi.login(t, bearertoken.Coordinator)

	var list openapi.SkillList
	pi.jsonCmd(t, &list, "", "skills", "list")
	require.Len(t, list.Data, 1)

	_, err = pi.cmd(t, "skills", "add", "--file", file)
	require.ErrorContains(t, err, "forbidden")

	out, err = w.cmd(t, "skills", "rm", "review-go")
	require.NoError(t, err)
	assert.Contains(t, out, "skill review-go is removed")

	out, err = w.cmd(t, "skills", "list")
	require.NoError(t, err)
	assert.Contains(t, out, "no skills")
}

// Scenario: the owner gives a Bot a skill. A later change of the Bot that
// names no skill keeps it. The launcher's `skills sync`, run with pi's
// token, puts the skill in the Bot's folder, fetches nothing the second time,
// takes away a skill the Bot no longer gets and keeps what starts with a dot.
func TestABotsSkillsAreSyncedForPi(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.login(t, bearertoken.Owner)

	archive, err := skillpack.Skill{Files: []skillpack.File{
		{Path: "SKILL.md", Data: []byte("---\nname: review-go\ndescription: Review a Go change.\n---\n")},
		{Path: "references/style.md", Data: []byte("short names\n")},
	}}.Archive()
	require.NoError(t, err)

	file := filepath.Join(t.TempDir(), "review-go.tar.gz")
	require.NoError(t, os.WriteFile(file, archive, 0o600))

	_, err = w.cmd(t, "skills", "add", "--file", file)
	require.NoError(t, err)

	out, err := w.cmd(t, "bots", "set", "vmap", "--skills", "review-go")
	require.NoError(t, err)
	assert.Contains(t, out, "skills: review-go")

	var bot openapi.Bot
	w.jsonCmd(t, &bot, "", "bots", "set", "vmap", "--effort", "high")
	assert.Equal(t, []string{"review-go"}, bot.Skills, "a change that names no skill keeps them")

	pi := &world{svc: w.svc, url: w.url, home: filepath.Join(t.TempDir(), "coordinator")}
	pi.login(t, bearertoken.Coordinator)

	dir := filepath.Join(t.TempDir(), "skills")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "gone"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".keep"), 0o700))

	out, err = pi.cmd(t, "skills", "sync", "--bot", "vmap", "--dir", dir)
	require.NoError(t, err)
	assert.Contains(t, out, "bot vmap: 1 skills in "+dir)
	assert.FileExists(t, filepath.Join(dir, "review-go", "references", "style.md"))
	assert.NoDirExists(t, filepath.Join(dir, "gone"), "a skill the Bot does not get is taken away")
	assert.DirExists(t, filepath.Join(dir, ".keep"))

	// The second time the folder holds the revision: an edit there stays, since nothing is fetched.
	edited := filepath.Join(dir, "review-go", "SKILL.md")
	require.NoError(t, os.WriteFile(edited, []byte("edited"), 0o600))
	_, err = pi.cmd(t, "skills", "sync", "--bot", "vmap", "--dir", dir)
	require.NoError(t, err)

	b, err := os.ReadFile(edited) //nolint:gosec // the test's own folder
	require.NoError(t, err)
	assert.Equal(t, "edited", string(b))

	_, err = w.cmd(t, "bots", "set", "vmap", "--skills", "")
	require.NoError(t, err)
	_, err = pi.cmd(t, "skills", "sync", "--bot", "vmap", "--dir", dir)
	require.NoError(t, err)
	assert.NoDirExists(t, filepath.Join(dir, "review-go"))
}

// Scenario: a link stands where the skills' folder is, or where the folder
// that holds it is, as a Bot could plant one in a folder it writes (issue
// #260). The sync is refused and nothing is written through the link; a
// skill's folder that is a link is refused too.
func TestASyncWritesNothingThroughALink(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.login(t, bearertoken.Owner)

	archive, err := skillpack.Skill{Files: []skillpack.File{
		{Path: "SKILL.md", Data: []byte("---\nname: review-go\ndescription: Review a Go change.\n---\n")},
	}}.Archive()
	require.NoError(t, err)

	file := filepath.Join(t.TempDir(), "review-go.tar.gz")
	require.NoError(t, os.WriteFile(file, archive, 0o600))

	_, err = w.cmd(t, "skills", "add", "--file", file)
	require.NoError(t, err)
	_, err = w.cmd(t, "bots", "set", "vmap", "--skills", "review-go")
	require.NoError(t, err)

	elsewhere := t.TempDir()
	root := t.TempDir()

	if err := os.Symlink(elsewhere, filepath.Join(root, "linked")); err != nil {
		t.Skipf("this machine makes no links: %v", err)
	}

	for _, dir := range []string{filepath.Join(root, "linked"), filepath.Join(root, "linked", "skills")} {
		_, err = w.cmd(t, "skills", "sync", "--bot", "vmap", "--dir", dir)
		require.ErrorContains(t, err, "is not a folder of its own", dir)
	}

	written, err := os.ReadDir(elsewhere)
	require.NoError(t, err)
	assert.Empty(t, written, "nothing is written where the link points")

	skills := filepath.Join(root, "skills")
	require.NoError(t, os.Mkdir(skills, 0o700))
	require.NoError(t, os.Symlink(elsewhere, filepath.Join(skills, "review-go")))

	_, err = w.cmd(t, "skills", "sync", "--bot", "vmap", "--dir", skills)
	require.ErrorContains(t, err, "is not a folder of its own")

	written, err = os.ReadDir(elsewhere)
	require.NoError(t, err)
	assert.Empty(t, written)
}

// Scenario: the skills this binary ships are listed with the description
// their stub gives, for the web's Skills page.
func TestTheBuiltinSkillsAreListed(t *testing.T) {
	t.Parallel()

	skills, err := BuiltinSkills()
	require.NoError(t, err)

	names := make([]string, len(skills))
	for i, s := range skills {
		names[i] = s.Name
		assert.NotEmpty(t, s.Description, s.Name)
	}

	assert.Equal(t, []string{
		"aws", "google-workspace", "hypomnema-coordinator", "images", "lark", "superset", "web-search",
	}, names)
}
