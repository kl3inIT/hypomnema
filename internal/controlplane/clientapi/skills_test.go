package clientapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/skillsource"
	"github.com/kl3inIT/hypomnema/internal/controlplane/skillssh"
	"github.com/kl3inIT/hypomnema/internal/shared/skillpack"
)

const reviewManifest = "---\nname: review-go\ndescription: Review a Go change.\n---\n"

// archiveOf is a skill's archive with a reference file of extra random
// bytes, which do not compress.
func archiveOf(t *testing.T, extra int) []byte {
	t.Helper()

	noise := make([]byte, extra)
	_, _ = rand.Read(noise)

	b, err := skillpack.Skill{Files: []skillpack.File{
		{Path: "SKILL.md", Data: []byte(reviewManifest)},
		{Path: "references/noise.bin", Data: noise},
	}}.Archive()
	require.NoError(t, err)

	return b
}

type fakeSources struct{ asked []skillsource.Source }

func (f *fakeSources) Fetch(_ context.Context, src skillsource.Source) (skillpack.Skill, string, error) {
	f.asked = append(f.asked, src)

	sk, err := skillpack.FromArchive(archiveOfManifest("---\nname: pdf\ndescription: Read PDFs.\n---\n"))

	return sk, "0123456789abcdef0123456789abcdef01234567", err
}

func (f *fakeSources) Find(_ context.Context, src skillsource.Source, name string) (skillsource.Source, error) {
	if name != "pdf" {
		return skillsource.Source{}, skillsource.ErrInvalid
	}

	src.Path = "skills/pdf-tools"

	return src, nil
}

// fakeDirectory is skills.sh: it finds pdf, and its auditors said one thing of it.
type fakeDirectory struct{ asked []string }

func (f *fakeDirectory) Find(_ context.Context, query, owner string) ([]skillssh.Result, int, error) {
	f.asked = append(f.asked, query+"|"+owner)

	return []skillssh.Result{
		{ID: "anthropics/skills/pdf", Name: "pdf", Source: "anthropics/skills", Installs: 1200},
		{ID: "o/r/pdf-lite", Name: "pdf-lite", Source: "o/r", Installs: 3},
	}, 1, nil
}

func (f *fakeDirectory) Audits(_ context.Context, source, slug string) ([]skillssh.Audit, error) {
	if source != "anthropics/skills" || slug != "pdf" {
		return nil, skillssh.ErrUnavailable
	}

	return []skillssh.Audit{{Partner: "snyk", Risk: "high"}, {Partner: "ath", Risk: "unheard-of"}}, nil
}

func archiveOfManifest(m string) []byte {
	b, _ := skillpack.Skill{Files: []skillpack.File{{Path: "SKILL.md", Data: []byte(m)}}}.Archive()

	return b
}

// Scenario: the owner uploads a skill's archive larger than any other body
// the API takes, then the same archive again, which adds nothing; a
// coordinator reads skills but cannot add one; an archive that is not a skill
// is refused as the owner's mistake.
func TestTheOwnerUploadsASkill(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	owner, _ := w.token("owner", bearertoken.Owner)
	coord, _ := w.token("pi", bearertoken.Coordinator)

	archive := archiveOf(t, 2<<20)
	require.Greater(t, len(archive), maxBodyBytes, "the archive is over the API's usual limit")

	resp, err := owner.UploadSkillWithBodyWithResponse(ctx, "application/octet-stream", bytes.NewReader(archive))
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode(), string(resp.Body))
	assert.Equal(t, "review-go", resp.JSON201.Name)
	assert.Equal(t, 1, resp.JSON201.Latest.Revision)
	assert.Equal(t, "upload", resp.JSON201.Latest.Source)

	again, err := owner.UploadSkillWithBodyWithResponse(ctx, "application/octet-stream", bytes.NewReader(archive))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, again.StatusCode(), "the same files add nothing")

	denied, err := coord.UploadSkillWithBodyWithResponse(ctx, "application/octet-stream", bytes.NewReader(archive))
	require.NoError(t, err)
	assert.Equal(t, openapi.Forbidden, errorCode(t, denied.Body, denied.StatusCode(), http.StatusForbidden))

	list, err := coord.ListSkillsWithResponse(ctx)
	require.NoError(t, err)
	require.NotNil(t, list.JSON200)
	require.Len(t, list.JSON200.Data, 1)

	got, err := coord.GetSkillWithResponse(ctx, "review-go")
	require.NoError(t, err)
	require.NotNil(t, got.JSON200)
	require.NotNil(t, got.JSON200.Revisions)
	assert.Len(t, *got.JSON200.Revisions, 1)

	bad, err := owner.UploadSkillWithBodyWithResponse(ctx, "application/octet-stream",
		bytes.NewReader(archiveOfManifest("# no frontmatter\n")))
	require.NoError(t, err)
	assert.Equal(t, openapi.InvalidRequest, errorCode(t, bad.Body, bad.StatusCode(), http.StatusBadRequest))

	big, err := owner.UploadSkillWithBodyWithResponse(ctx, "application/octet-stream",
		bytes.NewReader(make([]byte, skillpack.MaxArchive+1)))
	require.NoError(t, err)
	assert.Equal(t, http.StatusRequestEntityTooLarge, big.StatusCode())
}

// Scenario: the owner adds a skill from a repository; the source and the
// commit it was are kept. Without a way to reach GitHub the API says so.
func TestTheOwnerAddsASkillFromGitHub(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	owner, _ := w.token("owner", bearertoken.Owner)

	none, err := owner.AddSkillWithResponse(ctx, openapi.SkillFromGitHub{Github: "anthropics/skills/skills/pdf"})
	require.NoError(t, err)
	assert.Equal(t, openapi.Unavailable, errorCode(t, none.Body, none.StatusCode(), http.StatusServiceUnavailable))

	src := &fakeSources{}
	w.srv.WithSkillSources(src)

	bad, err := owner.AddSkillWithResponse(ctx, openapi.SkillFromGitHub{Github: "anthropics"})
	require.NoError(t, err)
	assert.Equal(t, openapi.InvalidRequest, errorCode(t, bad.Body, bad.StatusCode(), http.StatusBadRequest))

	resp, err := owner.AddSkillWithResponse(ctx, openapi.SkillFromGitHub{Github: "anthropics/skills/skills/pdf@main"})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode(), string(resp.Body))
	assert.Equal(t, "pdf", resp.JSON201.Name)
	assert.Equal(t, "github:anthropics/skills/skills/pdf@main", resp.JSON201.Latest.Source)
	assert.Equal(t, "0123456789abcdef0123456789abcdef01234567", val(resp.JSON201.Latest.Commit))
	assert.Equal(t, []skillsource.Source{{Owner: "anthropics", Repo: "skills", Path: "skills/pdf", Ref: "main"}},
		src.asked)

	on, err := owner.SetSkillEveryoneWithResponse(ctx, "pdf", openapi.SkillEveryone{Everyone: true})
	require.NoError(t, err)
	require.NotNil(t, on.JSON200)
	assert.True(t, on.JSON200.Everyone)

	gone, err := owner.DeleteSkillWithResponse(ctx, "pdf")
	require.NoError(t, err)
	assert.Equal(t, http.StatusNoContent, gone.StatusCode())
}

// Scenario: the web reads the files of a revision, each with its size and
// whether it runs, and the skills the binary ships beside those added.
func TestTheFilesOfARevisionAndTheBuiltinSkillsAreListed(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	owner, _ := w.token("owner", bearertoken.Owner)

	archive, err := skillpack.Skill{Files: []skillpack.File{
		{Path: "SKILL.md", Data: []byte(reviewManifest)},
		{Path: "scripts/check.sh", Exec: true, Data: []byte("go vet ./...\n")},
	}}.Archive()
	require.NoError(t, err)

	up, err := owner.UploadSkillWithBodyWithResponse(ctx, "application/octet-stream", bytes.NewReader(archive))
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, up.StatusCode(), string(up.Body))

	files, err := owner.ListSkillFilesWithResponse(ctx, "review-go", 1)
	require.NoError(t, err)
	require.NotNil(t, files.JSON200)
	assert.Equal(t, []openapi.SkillFile{
		{Path: "SKILL.md", Size: int64(len(reviewManifest)), Executable: false},
		{Path: "scripts/check.sh", Size: 13, Executable: true},
	}, files.JSON200.Data)

	missing, err := owner.ListSkillFilesWithResponse(ctx, "review-go", 2)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, missing.StatusCode())

	none, err := owner.ListBuiltinSkillsWithResponse(ctx)
	require.NoError(t, err)
	require.NotNil(t, none.JSON200)
	assert.Empty(t, none.JSON200.Data, "a control plane that wired none lists none")

	w.srv.WithBuiltinSkills(func() ([]BuiltinSkill, error) {
		return []BuiltinSkill{{Name: "lark", Description: "Lark through lark-cli."}}, nil
	})

	built, err := owner.ListBuiltinSkillsWithResponse(ctx)
	require.NoError(t, err)
	require.NotNil(t, built.JSON200)
	assert.Equal(t, []openapi.BuiltinSkill{{Name: "lark", Description: "Lark through lark-cli."}}, built.JSON200.Data)
}

// Scenario: the owner searches skills.sh, reads what its auditors said of
// one result, and adds it by the name skills.sh gave: the folder found is
// what the source keeps, so adding it again fetches the same folder. A
// coordinator may not search; skills.sh not answering leaves the audits
// empty.
func TestTheOwnerAddsASkillFoundOnSkillsSh(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	owner, _ := w.token("owner", bearertoken.Owner)
	coord, _ := w.token("pi", bearertoken.Coordinator)

	none, err := owner.SearchSkillsWithResponse(ctx, &openapi.SearchSkillsParams{Q: "pdf"})
	require.NoError(t, err)
	assert.Equal(t, openapi.Unavailable, errorCode(t, none.Body, none.StatusCode(), http.StatusServiceUnavailable))

	dir, src := &fakeDirectory{}, &fakeSources{}
	w.srv.WithSkillDirectory(dir).WithSkillSources(src)

	who := "Anthropics "
	found, err := owner.SearchSkillsWithResponse(ctx, &openapi.SearchSkillsParams{Q: " pdf forms ", Owner: &who})
	require.NoError(t, err)
	require.NotNil(t, found.JSON200, string(found.Body))
	assert.Equal(t, 1, found.JSON200.Exact)
	assert.Equal(t, openapi.SkillSearchResult{
		Id: "anthropics/skills/pdf", Name: "pdf", Source: "anthropics/skills", Installs: 1200,
	}, found.JSON200.Data[0])
	assert.Equal(t, []string{"pdf forms|anthropics"}, dir.asked)

	denied, err := coord.SearchSkillsWithResponse(ctx, &openapi.SearchSkillsParams{Q: "pdf"})
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, denied.StatusCode())

	audits, err := owner.ListSkillAuditsWithResponse(ctx,
		&openapi.ListSkillAuditsParams{Source: "anthropics/skills", Skill: "pdf"})
	require.NoError(t, err)
	require.NotNil(t, audits.JSON200, string(audits.Body))
	assert.Equal(t, []openapi.SkillAudit{
		{Partner: "snyk", Risk: openapi.SkillAuditRiskHigh},
		{Partner: "ath", Risk: openapi.SkillAuditRiskUnknown},
	}, audits.JSON200.Data)

	quiet, err := owner.ListSkillAuditsWithResponse(ctx, &openapi.ListSkillAuditsParams{Source: "o/r", Skill: "x"})
	require.NoError(t, err)
	require.NotNil(t, quiet.JSON200)
	assert.Empty(t, quiet.JSON200.Data)

	bad, err := owner.ListSkillAuditsWithResponse(ctx, &openapi.ListSkillAuditsParams{Source: "../x", Skill: "x"})
	require.NoError(t, err)
	assert.Equal(t, openapi.InvalidRequest, errorCode(t, bad.Body, bad.StatusCode(), http.StatusBadRequest))

	pdf := "pdf"
	added, err := owner.AddSkillWithResponse(ctx, openapi.SkillFromGitHub{Github: "anthropics/skills", Skill: &pdf})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, added.StatusCode(), string(added.Body))
	assert.Equal(t, "github:anthropics/skills/skills/pdf-tools@HEAD", added.JSON201.Latest.Source)
	assert.Equal(t, []skillsource.Source{{Owner: "anthropics", Repo: "skills", Path: "skills/pdf-tools"}}, src.asked)

	both, err := owner.AddSkillWithResponse(ctx, openapi.SkillFromGitHub{Github: "anthropics/skills/x", Skill: &pdf})
	require.NoError(t, err)
	assert.Equal(t, openapi.InvalidRequest, errorCode(t, both.Body, both.StatusCode(), http.StatusBadRequest))

	other := "missing"
	unknown, err := owner.AddSkillWithResponse(ctx, openapi.SkillFromGitHub{Github: "anthropics/skills", Skill: &other})
	require.NoError(t, err)
	assert.Equal(t, openapi.InvalidRequest, errorCode(t, unknown.Body, unknown.StatusCode(), http.StatusBadRequest))
}
