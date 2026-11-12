package orchestration

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
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

// namedSkill is a skill called name whose SKILL.md ends with body.
func namedSkill(t *testing.T, name, body string) skillpack.Skill {
	t.Helper()

	manifest := "---\nname: " + name + "\ndescription: d.\n---\n" + body
	archive, err := skillpack.Skill{Files: []skillpack.File{{Path: "SKILL.md", Data: []byte(manifest)}}}.Archive()
	require.NoError(t, err)

	sk, err := skillpack.FromArchive(archive)
	require.NoError(t, err)

	return sk
}

// Scenario: two skills are for everyone and a third is not; the profile
// turns one of the first off and adds the third. Its worker is offered the
// other skill for everyone and the third, each at the revision of the moment
// the Dispatch was made: a later revision does not reach it. The node of the
// Dispatch fetches those revisions and no other node does. A skill a profile
// names is not removed, and a profile cannot name one that does not exist.
func TestAWorkerIsGivenTheSkillsOfItsProfile(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	add := func(name, body string) Skill {
		t.Helper()

		sk, _, err := w.s.AddSkill(ctx, namedSkill(t, name, body), SkillSource{Source: SkillUpload})
		require.NoError(t, err)

		return sk
	}

	everyone := add("review-go", "1")
	off := add("pdf", "1")
	own := add("lark-docs", "1")

	for _, name := range []string{"review-go", "pdf"} {
		_, err := w.s.SetSkillEveryone(ctx, name, true)
		require.NoError(t, err)
	}

	_, _, err := w.s.ApplyProfile(ctx, "",
		"name: implementer\ndescription: d\nruntime: claude\nskills: [nowhere]\n")
	require.ErrorIs(t, err, ErrNotFound)

	_, _, err = w.s.ApplyProfile(ctx, "",
		"name: implementer\ndescription: d\nruntime: claude\nskills: [pdf]\nskills_off: [pdf]\n")
	require.ErrorIs(t, err, ErrInvalid)

	p, _, err := w.s.ApplyProfile(ctx, "",
		"name: implementer\ndescription: d\nruntime: claude\nskills: [lark-docs]\nskills_off: [pdf]\n")
	require.NoError(t, err)
	assert.Equal(t, []string{"lark-docs"}, p.Skills)
	assert.Equal(t, []string{"pdf"}, p.SkillsOff)

	task := w.tasks(NewTask{Ref: "a", Spec: "do a"})[0]
	w.dispatch(task.ID)

	add("review-go", "2")

	pending, err := w.s.NodePending(ctx, w.node.ID)
	require.NoError(t, err)
	require.Len(t, pending.Offers, 1)
	assert.Equal(t, []nodeproto.Skill{
		{Name: "lark-docs", Revision: 1, Digest: own.Latest.Digest},
		{Name: "review-go", Revision: 1, Digest: everyone.Latest.Digest},
	}, pending.Offers[0].Skills, "the revisions of when the Dispatch was made")

	archive, err := w.s.SkillArchiveForNode(ctx, w.node.ID, everyone.Latest.Digest)
	require.NoError(t, err)

	got, err := skillpack.FromArchive(archive)
	require.NoError(t, err)
	assert.Equal(t, everyone.Latest.Digest, got.Digest)

	other := w.addNode("node_b", 1, "claude")
	_, err = w.s.SkillArchiveForNode(ctx, other.ID, everyone.Latest.Digest)
	require.ErrorIs(t, err, ErrNotFound, "another node holds no offer naming it")
	_, err = w.s.SkillArchiveForNode(ctx, w.node.ID, off.Latest.Digest)
	require.ErrorIs(t, err, ErrNotFound, "the Dispatch was not given it")

	err = w.s.DeleteSkill(ctx, "lark-docs")
	require.ErrorIs(t, err, ErrConflict)
	assert.ErrorContains(t, err, "implementer")
	require.ErrorIs(t, w.s.DeleteSkill(ctx, "pdf"), ErrConflict, "turned off is named too")

	// Once its Dispatch has ended, a skill no profile names is removed though the Dispatch had it.
	require.NoError(t, w.s.CancelTask(ctx, task.ID))
	_, err = w.s.SkillArchiveForNode(ctx, w.node.ID, everyone.Latest.Digest)
	require.ErrorIs(t, err, ErrNotFound, "a Dispatch that ended fetches nothing")
	require.NoError(t, w.s.DeleteSkill(ctx, "review-go"))

	pending, err = w.s.NodePending(ctx, w.node.ID)
	require.NoError(t, err)
	assert.Empty(t, pending.Offers)
}
