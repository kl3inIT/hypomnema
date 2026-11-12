package clientcli

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/skillsource"
	"github.com/kl3inIT/hypomnema/internal/controlplane/skillssh"
	"github.com/kl3inIT/hypomnema/internal/shared/skillpack"
)

// directory is skills.sh with two skills, one matching every word.
type directory struct{}

func (directory) Find(context.Context, string, string) ([]skillssh.Result, int, error) {
	return []skillssh.Result{
		{ID: "anthropics/skills/pdf", Name: "pdf", Source: "anthropics/skills", Installs: 12_345},
		{ID: "o/r/forms", Name: "forms", Source: "o/r", Installs: 1},
	}, 1, nil
}

func (directory) Audits(_ context.Context, _, slug string) ([]skillssh.Audit, error) {
	two := 2

	return []skillssh.Audit{
		{Partner: "ath", Risk: "safe"}, {Partner: "socket", Risk: "low", Alerts: &two}, {Partner: "snyk", Risk: "high"},
	}, nil
}

// repository holds pdf in skills/pdf-tools.
type repository struct{}

func (repository) Fetch(context.Context, skillsource.Source) (skillpack.Skill, string, error) {
	archive, err := skillpack.Skill{Files: []skillpack.File{
		{Path: "SKILL.md", Data: []byte("---\nname: pdf\ndescription: Read PDFs.\n---\n")},
	}}.Archive()
	if err != nil {
		return skillpack.Skill{}, "", err
	}

	sk, err := skillpack.FromArchive(archive)

	return sk, "0123456789abcdef0123456789abcdef01234567", err
}

func (repository) Find(_ context.Context, src skillsource.Source, _ string) (skillsource.Source, error) {
	src.Path = "skills/pdf-tools"

	return src, nil
}

// Scenario: the owner searches skills.sh from the terminal, as with the
// skills CLI's `skills find`, then adds a result by its name: what skills.sh's
// auditors said is shown first, and the folder found is kept.
func TestTheOwnerSearchesSkillsShAndAddsOne(t *testing.T) {
	t.Parallel()

	w := newWorldWith(t, nil, func(srv *clientapi.Server) {
		srv.WithSkillDirectory(directory{}).WithSkillSources(repository{})
	})
	w.login(t, bearertoken.Owner)

	out, err := w.cmd(t, "skills", "search", "pdf", "forms")
	require.NoError(t, err)
	assert.Contains(t, out, "anthropics/skills@pdf  12.3K installs\n└ https://skills.sh/anthropics/skills/pdf\n")
	assert.Contains(t, out, "matching some of the words of \"pdf forms\":\n\no/r@forms  1 install\n")

	_, err = w.cmd(t, "skills", "add", "--skill", "pdf")
	require.ErrorIs(t, err, ErrUsage)

	out, err = w.cmd(t, "skills", "add", "--github", "anthropics/skills", "--skill", "pdf")
	require.NoError(t, err)
	assert.Contains(t, out, "skills.sh's audits of pdf:\n  Gen     Safe\n  Socket  2 alerts\n  Snyk    High Risk\n")
	assert.Contains(t, out, "skill pdf: revision 1 added")

	out, err = w.cmd(t, "skills", "show", "pdf")
	require.NoError(t, err)
	assert.Contains(t, out, "github:anthropics/skills/skills/pdf-tools@HEAD")
}

func TestInstallsAndSlugsAreWrittenAsTheSkillsCLIWritesThem(t *testing.T) {
	t.Parallel()

	for n, want := range map[int64]string{0: "", 1: "1 install", 999: "999 installs", 1000: "1K installs",
		12_345: "12.3K installs", 2_000_000: "2M installs"} {
		assert.Equal(t, want, installs(n), n)
	}

	assert.Equal(t, "react-best-practices", skillSlug(" React Best_Practices! "))
}
