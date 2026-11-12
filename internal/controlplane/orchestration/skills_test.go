package orchestration

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/skillpack"
)

// skillOf is the skill review-go whose SKILL.md ends with body, read back
// from its archive so that it has its real digest.
func skillOf(t *testing.T, body string) skillpack.Skill {
	t.Helper()

	manifest := "---\nname: review-go\ndescription: Review a Go change.\n---\n" + body
	archive, err := skillpack.Skill{Files: []skillpack.File{{Path: "SKILL.md", Data: []byte(manifest)}}}.Archive()
	require.NoError(t, err)

	sk, err := skillpack.FromArchive(archive)
	require.NoError(t, err)

	return sk
}

// Scenario: the owner adds a skill, adds the same folder again, then an edit
// of it. The same files make no new revision; the edit is revision 2, and
// revision 1 is kept as it was (Omnara's immutable revisions, OpenClaw's
// no-op identical saves).
func TestAddingASkillAgainMakesARevisionOnlyWhenItChanged(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	first, added, err := w.s.AddSkill(ctx, skillOf(t, "a"), SkillSource{Source: SkillUpload})
	require.NoError(t, err)
	require.True(t, added)
	assert.Equal(t, 1, first.Latest.Revision)
	assert.False(t, first.Everyone, "a skill added is off until the owner gives it")

	w.clock.now = w.clock.now.Add(time.Minute)

	same, added, err := w.s.AddSkill(ctx, skillOf(t, "a"), SkillSource{Source: "github:o/r@main", Commit: "c1"})
	require.NoError(t, err)
	assert.False(t, added)
	assert.Equal(t, first.Latest, same.Latest, "nothing is kept for the same files")

	edit, added, err := w.s.AddSkill(ctx, skillOf(t, "b"), SkillSource{Source: "github:o/r@main", Commit: "c2"})
	require.NoError(t, err)
	require.True(t, added)
	assert.Equal(t, 2, edit.Latest.Revision)
	assert.Equal(t, "c2", edit.Latest.Commit)
	require.Len(t, edit.Revisions, 2)
	assert.Equal(t, first.Latest, edit.Revisions[1], "revision 1 stays as it was")

	list, err := w.s.ListSkills(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, edit.Latest, list[0].Latest, "the list shows the newest revision")
}

// Scenario: the owner gives a skill to everyone, takes it back, then removes
// it; a skill that does not exist is not found.
func TestTheOwnerGivesASkillToEveryoneAndRemovesIt(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	_, _, err := w.s.AddSkill(ctx, skillOf(t, "a"), SkillSource{Source: SkillUpload})
	require.NoError(t, err)

	on, err := w.s.SetSkillEveryone(ctx, "review-go", true)
	require.NoError(t, err)
	assert.True(t, on.Everyone)

	off, err := w.s.SetSkillEveryone(ctx, "review-go", false)
	require.NoError(t, err)
	assert.False(t, off.Everyone)

	require.NoError(t, w.s.DeleteSkill(ctx, "review-go"))

	list, err := w.s.ListSkills(ctx)
	require.NoError(t, err)
	assert.Empty(t, list)

	_, err = w.s.GetSkill(ctx, "review-go")
	require.ErrorIs(t, err, ErrNotFound)
	_, err = w.s.SetSkillEveryone(ctx, "review-go", true)
	require.ErrorIs(t, err, ErrNotFound)
	require.ErrorIs(t, w.s.DeleteSkill(ctx, "review-go"), ErrNotFound)
}
