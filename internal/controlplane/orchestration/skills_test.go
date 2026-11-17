package orchestration

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/githost"
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
		"name: implementer\ndescription: d\nruntime: claude\nmodel: {provider: model-gateway}\nskills: [lark-docs]\n"+
			"skills_off: [pdf]\n")
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
	require.ErrorContains(t, err, "implementer")
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

// Scenario: a Bot turns off a skill for everyone and adds another; it gets
// the rest for everyone and the one it adds, at their newest revisions. A
// skill a Bot names is not removed, and a Bot cannot name one that does not
// exist.
func TestABotGetsItsSkills(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	for _, name := range []string{"review-go", "pdf", "lark-docs"} {
		_, _, err := w.s.AddSkill(ctx, namedSkill(t, name, "1"), SkillSource{Source: SkillUpload})
		require.NoError(t, err)
	}

	for _, name := range []string{"review-go", "pdf"} {
		_, err := w.s.SetSkillEveryone(ctx, name, true)
		require.NoError(t, err)
	}

	_, err := w.s.PutBot(ctx, Bot{Name: "vmap", Skills: []string{"nowhere"}})
	require.ErrorIs(t, err, ErrNotFound)

	vmapBot, err := w.s.PutBot(ctx, Bot{Name: "vmap", Skills: []string{"lark-docs"}, SkillsOff: []string{"pdf"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"lark-docs"}, vmapBot.Skills)
	assert.Equal(t, []string{"pdf"}, vmapBot.SkillsOff)

	latest, _, err := w.s.AddSkill(ctx, namedSkill(t, "review-go", "2"), SkillSource{Source: SkillUpload})
	require.NoError(t, err)

	given, err := w.s.BotSkills(ctx, vmapBot.ID)
	require.NoError(t, err)
	require.Len(t, given, 2)
	assert.Equal(t, "lark-docs", given[0].Name)
	assert.Equal(t, nodeproto.Skill{Name: "review-go", Revision: 2, Digest: latest.Latest.Digest}, given[1],
		"a Bot takes the newest revision each time pi starts")

	archive, err := w.s.SkillArchive(ctx, "review-go", 2)
	require.NoError(t, err)

	got, err := skillpack.FromArchive(archive)
	require.NoError(t, err)
	assert.Equal(t, latest.Latest.Digest, got.Digest)

	_, err = w.s.SkillArchive(ctx, "review-go", 3)
	require.ErrorIs(t, err, ErrNotFound)

	err = w.s.DeleteSkill(ctx, "lark-docs")
	require.ErrorIs(t, err, ErrConflict)
	require.ErrorContains(t, err, "vmap", "the Bots are named by their names")
}

// Scenario: the control plane starts with a release that ships pdf (issue
// #492). pdf is in the store for everyone, a Bot and a worker get it without
// the owner doing anything, and a restart of the same release keeps nothing
// new. The owner turns it off for everyone; the next release's pdf is
// revision 2 and stays off. The owner cannot remove it.
func TestABuiltinSkillIsPublishedAtEachRelease(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	done, err := w.s.PublishBuiltinSkills(ctx, []skillpack.Skill{namedSkill(t, "pdf", "1")}, "builtin:v1")
	require.NoError(t, err)
	assert.Equal(t, []BuiltinPublish{{Name: "pdf", Did: "added"}}, done)

	pdf, err := w.s.GetSkill(ctx, "pdf")
	require.NoError(t, err)
	assert.True(t, pdf.Everyone, "a built-in skill starts for everyone")
	assert.True(t, pdf.BuiltIn)
	assert.Equal(t, "builtin:v1", pdf.Latest.Source)

	bot, err := w.s.PutBot(ctx, Bot{Name: "vmap"})
	require.NoError(t, err)

	given, err := w.s.BotSkills(ctx, bot.ID)
	require.NoError(t, err)
	assert.Equal(t, []nodeproto.Skill{{Name: "pdf", Revision: 1, Digest: pdf.Latest.Digest}}, given)

	task := w.tasks(NewTask{Ref: "a", Spec: "do a"})[0]
	w.dispatch(task.ID)

	pending, err := w.s.NodePending(ctx, w.node.ID)
	require.NoError(t, err)
	require.Len(t, pending.Offers, 1)
	assert.Equal(t, given, pending.Offers[0].Skills, "a worker gets it the same way")

	done, err = w.s.PublishBuiltinSkills(ctx, []skillpack.Skill{namedSkill(t, "pdf", "1")}, "builtin:v1")
	require.NoError(t, err)
	assert.Equal(t, []BuiltinPublish{{Name: "pdf", Did: "current"}}, done)

	_, err = w.s.SetSkillEveryone(ctx, "pdf", false)
	require.NoError(t, err)

	done, err = w.s.PublishBuiltinSkills(ctx, []skillpack.Skill{namedSkill(t, "pdf", "2")}, "builtin:v2")
	require.NoError(t, err)
	assert.Equal(t, []BuiltinPublish{{Name: "pdf", Did: "updated"}}, done)

	pdf, err = w.s.GetSkill(ctx, "pdf")
	require.NoError(t, err)
	assert.Equal(t, 2, pdf.Latest.Revision)
	assert.Equal(t, "builtin:v2", pdf.Latest.Source)
	assert.False(t, pdf.Everyone, "the owner's switch is kept")

	err = w.s.DeleteSkill(ctx, "pdf")
	require.ErrorIs(t, err, ErrConflict)
	require.ErrorContains(t, err, "turn it off")

	list, err := w.s.ListSkills(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.True(t, list[0].BuiltIn)
}

// Scenario: the owner uploads his own revision of a built-in skill; the next
// release leaves it his. A skill the owner added under a name before the
// binary shipped one of that name is never touched, and is his to remove.
func TestABuiltinSkillNeverOverwritesTheOwners(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	_, err := w.s.PublishBuiltinSkills(ctx, []skillpack.Skill{namedSkill(t, "pdf", "1")}, "builtin:v1")
	require.NoError(t, err)

	mine, _, err := w.s.AddSkill(ctx, namedSkill(t, "pdf", "my notes"), SkillSource{Source: SkillUpload})
	require.NoError(t, err)

	_, _, err = w.s.AddSkill(ctx, namedSkill(t, "pages", "my pages"), SkillSource{Source: SkillUpload})
	require.NoError(t, err)

	done, err := w.s.PublishBuiltinSkills(ctx,
		[]skillpack.Skill{namedSkill(t, "pdf", "2"), namedSkill(t, "pages", "1")}, "builtin:v2")
	require.NoError(t, err)
	assert.Equal(t, []BuiltinPublish{{Name: "pdf", Did: "edited"}, {Name: "pages", Did: "owner's"}}, done)

	pdf, err := w.s.GetSkill(ctx, "pdf")
	require.NoError(t, err)
	assert.Equal(t, mine.Latest, pdf.Latest, "the owner's revision stays the newest")
	assert.True(t, pdf.BuiltIn, "still the binary's skill, edited")

	pages, err := w.s.GetSkill(ctx, "pages")
	require.NoError(t, err)
	assert.False(t, pages.BuiltIn)
	assert.False(t, pages.Everyone)
	assert.Equal(t, SkillUpload, pages.Latest.Source)
	require.NoError(t, w.s.DeleteSkill(ctx, "pages"))

	_, err = w.s.PublishBuiltinSkills(ctx, []skillpack.Skill{namedSkill(t, "pdf", "3")}, SkillUpload)
	require.ErrorIs(t, err, ErrInvalid, "a built-in revision says so in its source")
}

// requiringSkill is a skill whose frontmatter requires what an agent must
// have, as the Integrations' guides do (issue #536).
func requiringSkill(t *testing.T, name, kind, value string) skillpack.Skill {
	t.Helper()

	manifest := "---\nname: " + name + "\ndescription: d.\nmetadata:\n  hypomnema:\n    requires:\n      " + kind + ": " +
		value + "\n---\nbody"
	archive, err := skillpack.Skill{Files: []skillpack.File{{Path: "SKILL.md", Data: []byte(manifest)}}}.Archive()
	require.NoError(t, err)

	sk, err := skillpack.FromArchive(archive)
	require.NoError(t, err)
	require.Equal(t, []string{kind + ":" + value}, sk.Requires)

	return sk
}

// Scenario: the guides of Superset and of the phone are built in, for
// everyone, beside a skill that requires nothing (issue #536). A worker
// whose Run was given no Connection gets only the latter; one whose Run was
// given superset:vmaps gets the Superset guide too, and the phone's once a
// node holds one. A Bot gets the Superset guide only while it may use the
// Connection (switched on for chats, and on its list when it has one), read
// each time pi starts.
func TestAGuideGoesOnlyToAnAgentThatMayUseItsIntegration(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	_, err := w.s.PublishBuiltinSkills(ctx, []skillpack.Skill{
		namedSkill(t, "pdf", "1"),
		requiringSkill(t, "superset", skillpack.RequiresIntegration, "superset"),
		requiringSkill(t, "android-device", skillpack.RequiresDevice, "android"),
	}, "builtin:v1")
	require.NoError(t, err)

	got, err := w.s.GetSkill(ctx, "superset")
	require.NoError(t, err)
	assert.Equal(t, []string{"integration:superset"}, got.Latest.Requires)

	_, err = w.s.SaveServiceConnection(ctx, Connection{Integration: "superset", Label: "vmaps", Secret: "s"})
	require.NoError(t, err)

	names := func(skills []nodeproto.Skill) []string {
		out := make([]string, len(skills))
		for i, sk := range skills {
			out[i] = sk.Name
		}

		return out
	}

	taskOf := func(run Run) string {
		t.Helper()

		tasks, err := w.s.CreateTasks(ctx, run.ID, []NewTask{{Ref: "a", Spec: "do a"}})
		require.NoError(t, err)

		return tasks[0].ID
	}

	offered := func(d Dispatch) []nodeproto.Skill {
		t.Helper()

		pending, err := w.s.NodePending(ctx, w.node.ID)
		require.NoError(t, err)

		for _, o := range pending.Offers {
			if o.DispatchID == d.ID {
				return o.Skills
			}
		}

		require.Fail(t, "no offer of the dispatch")

		return nil
	}

	assert.Equal(t, []string{"pdf"}, names(offered(w.dispatch(taskOf(w.run)))), "a Run given no Connection")

	withSuperset := func() Run {
		run, err := w.s.CreateRun(ctx, NewRun{
			Objective: "read a chart", CreatedBy: "owner", Uses: []string{"superset:vmaps"},
		})
		require.NoError(t, err)

		return run
	}

	skills := offered(w.dispatch(taskOf(withSuperset())))
	assert.Equal(t, []string{"pdf", "superset"}, names(skills))
	assert.Equal(t, []string{"integration:superset"}, skills[1].Requires, "a node of an older release is sent none")

	phone, _, err := w.s.AddNode(ctx, "phone", 1)
	require.NoError(t, err)
	require.NoError(t, w.s.NodeHello(ctx, phone.ID, Hello{WorktreeRoot: "/srv/wt", Device: DeviceAndroid}))
	assert.Equal(t, []string{"android-device", "pdf", "superset"}, names(offered(w.dispatch(taskOf(withSuperset())))))

	vmapBot, err := w.s.PutBot(ctx, Bot{Name: "vmap"})
	require.NoError(t, err)

	given, err := w.s.BotSkills(ctx, vmapBot.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"android-device", "pdf"}, names(given), "superset:vmaps is not on for chats")

	_, err = w.s.SetConnectionChats(ctx, "superset", "vmaps", true)
	require.NoError(t, err)

	given, err = w.s.BotSkills(ctx, vmapBot.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"android-device", "pdf", "superset"}, names(given))

	_, err = w.s.SaveServiceConnection(ctx, Connection{Integration: "google", Label: "work", Secret: "g"})
	require.NoError(t, err)

	narrow, err := w.s.PutBot(ctx, Bot{
		Name: "narrow", Connections: AllowList{Limited: true, Names: []string{"google:work"}},
	})
	require.NoError(t, err)

	given, err = w.s.BotSkills(ctx, narrow.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"android-device", "pdf"}, names(given), "superset:vmaps is not on its list")
}

// Scenario: the gitlab guide requires the kind gitlab, not a host (issue #740): a Bot gets it while it may use a
// Connection of any GitLab host, gitlab.com or a server the owner declared as gitlab, and not for a host of another
// kind.
func TestTheGitLabGuideGoesToABotGivenAnyGitLabHost(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	_, err := w.s.PublishBuiltinSkills(ctx, []skillpack.Skill{
		requiringSkill(t, "gitlab", skillpack.RequiresIntegration, "gitlab"),
	}, "builtin:v1")
	require.NoError(t, err)

	_, err = w.s.AddGitHost(ctx, "git.example.com", githost.GitLab)
	require.NoError(t, err)
	_, err = w.s.AddGitHost(ctx, "ghe.example.com", githost.GitHub)
	require.NoError(t, err)

	bot, err := w.s.PutBot(ctx, Bot{Name: "vmap"})
	require.NoError(t, err)

	given := func() []string {
		t.Helper()

		skills, err := w.s.BotSkills(ctx, bot.ID)
		require.NoError(t, err)

		out := []string{}
		for _, sk := range skills {
			out = append(out, sk.Name)
		}

		return out
	}

	for _, host := range []string{"git.example.com", "ghe.example.com"} {
		_, err := w.s.SetConnection(ctx, host, "default", "", "conn-"+host)
		require.NoError(t, err)
	}

	assert.Empty(t, given(), "no Connection is on for chats")

	_, err = w.s.SetConnectionChats(ctx, "ghe.example.com", "default", true)
	require.NoError(t, err)
	assert.Empty(t, given(), "a GitHub host is not GitLab")

	_, err = w.s.SetConnectionChats(ctx, "git.example.com", "default", true)
	require.NoError(t, err)
	assert.Equal(t, []string{"gitlab"}, given())
}
