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
