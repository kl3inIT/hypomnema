package orchestration

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/artifactpack"
)

func page(t *testing.T, html string, more ...artifactpack.File) artifactpack.Pack {
	t.Helper()

	p, err := artifactpack.Check(append([]artifactpack.File{{Path: "index.html", Data: []byte(html)}}, more...))
	require.NoError(t, err)

	return p
}

// Scenario: a Bot shows a dashboard in its chat; the chat gets one message of kind artifact that names it. Shown
// again by its id with new data, it is revision 2 and the same message, in place, says so; the first revision is
// still there to step back to. The same files under the same title make nothing new. Another chat's pi, or the
// Bot without its chat, cannot replace it, and a Routine's run makes none but replaces its Bot's.
func TestABotShowsAnArtifactAndRevisesItInPlace(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()
	chat, err := w.s.CreateChat(ctx, "", "")
	require.NoError(t, err)

	pi := ArtifactMaker{Bot: chat.Bot, Chat: chat.ID}

	a, made, err := w.s.ShowArtifact(ctx, pi, ShowArtifact{Title: "Sales, week 46", Pack: page(t, "<p>v1")})
	require.NoError(t, err)
	assert.True(t, made)
	assert.Equal(t, 1, a.Revision)
	assert.Equal(t, artifactpack.TypeHTML, a.Type)

	msgs, err := w.s.ListChatMessages(ctx, chat.ID, 0, 0)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.Equal(t, ChatArtifact, msgs[0].Kind)
	assert.Equal(t, ChatSystem, msgs[0].Role)
	require.NotNil(t, msgs[0].Artifact)
	assert.Equal(t, ArtifactCard{ID: a.ID, Revision: 1, Title: "Sales, week 46", Type: artifactpack.TypeHTML,
		UpdatedAt: a.UpdatedAt}, *msgs[0].Artifact)

	undelivered, err := w.s.Undelivered(ctx, chat.ID)
	require.NoError(t, err)
	assert.Empty(t, undelivered, "pi made it; it is not given back to pi")

	w.advance(time.Minute)

	a, made, err = w.s.ShowArtifact(ctx, pi, ShowArtifact{ID: a.ID, Pack: page(t, "<p>v2")})
	require.NoError(t, err)
	assert.True(t, made)
	assert.Equal(t, 2, a.Revision)
	assert.Equal(t, "Sales, week 46", a.Title, "a replacement without a title keeps it")
	require.Len(t, a.Revisions, 2)
	assert.Equal(t, []int{2, 1}, []int{a.Revisions[0].Revision, a.Revisions[1].Revision})

	again, err := w.s.ListChatMessages(ctx, chat.ID, 0, 0)
	require.NoError(t, err)
	require.Len(t, again, 1, "the message is updated, not added")
	assert.Equal(t, msgs[0].Seq, again[0].Seq)
	assert.Equal(t, 2, again[0].Artifact.Revision)

	old, err := w.s.ReadArtifactFile(ctx, a.ID, 1, "")
	require.NoError(t, err)
	assert.Equal(t, "<p>v1", string(old.Data))

	_, made, err = w.s.ShowArtifact(ctx, pi, ShowArtifact{ID: a.ID, Title: "Sales, week 46", Pack: page(t, "<p>v2")})
	require.NoError(t, err)
	assert.False(t, made, "the same files and title are no new revision")

	other, err := w.s.CreateChat(ctx, "", "")
	require.NoError(t, err)

	_, _, err = w.s.ShowArtifact(ctx, ArtifactMaker{Bot: chat.Bot, Chat: other.ID},
		ShowArtifact{ID: a.ID, Pack: page(t, "<p>x")})
	require.ErrorIs(t, err, ErrNotFound, "another chat's pi does not see it")

	_, _, err = w.s.ShowArtifact(ctx, ArtifactMaker{Bot: chat.Bot}, ShowArtifact{Title: "New", Pack: page(t, "x")})
	require.ErrorIs(t, err, ErrForbidden, "a Routine's run makes none")

	a, made, err = w.s.ShowArtifact(ctx, ArtifactMaker{Bot: chat.Bot},
		ShowArtifact{ID: a.ID, Title: "Sales, live", Pack: page(t, "<p>v3")})
	require.NoError(t, err)
	assert.True(t, made, "a Routine's run of its Bot rebuilds it")
	assert.Equal(t, "Sales, live", a.Title)

	_, _, err = w.s.ShowArtifact(ctx, pi, ShowArtifact{Title: "", Pack: page(t, "x")})
	require.ErrorIs(t, err, ErrInvalid, "a new one needs a title")
}

// Scenario: a Routine rebuilds a dashboard every hour. Only the last ten revisions are kept, and the bytes that no
// kept revision names go with them, while a file every revision shares stays.
func TestOnlyTheLastRevisionsAreKept(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()
	chat, err := w.s.CreateChat(ctx, "", "")
	require.NoError(t, err)

	pi := ArtifactMaker{Bot: chat.Bot, Chat: chat.ID}
	shared := artifactpack.File{Path: "lib.js", Data: []byte("shared")}

	a, _, err := w.s.ShowArtifact(ctx, pi, ShowArtifact{Title: "Live", Pack: page(t, "rev 1", shared)})
	require.NoError(t, err)

	for i := 2; i <= ArtifactRevisionsKept+3; i++ {
		a, _, err = w.s.ShowArtifact(ctx, pi, ShowArtifact{ID: a.ID, Pack: page(t, fmt.Sprintf("rev %d", i), shared)})
		require.NoError(t, err)
	}

	assert.Equal(t, ArtifactRevisionsKept+3, a.Revision)
	require.Len(t, a.Revisions, ArtifactRevisionsKept)
	assert.Equal(t, 4, a.Revisions[len(a.Revisions)-1].Revision)

	_, err = w.s.ReadArtifactFile(ctx, a.ID, 3, "")
	require.ErrorIs(t, err, ErrNotFound)

	var blobs int
	require.NoError(t, w.db.QueryRowContext(ctx, "SELECT count(*) FROM artifact_blobs").Scan(&blobs))
	assert.Equal(t, ArtifactRevisionsKept+1, blobs, "ten pages and the one shared script")

	lib, err := w.s.ReadArtifactFile(ctx, a.ID, a.Revision, "lib.js")
	require.NoError(t, err)
	assert.Equal(t, "shared", string(lib.Data))
	assert.Equal(t, "text/javascript; charset=utf-8", lib.MediaType)
}

// Scenario: the Lark relay names under a reply the artifacts shown since the Bot's previous reply: the first reply
// after a dashboard names it, the next one does not, and a reply after it was revised names it again.
func TestAReplyFollowsTheArtifactsMadeBeforeIt(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()
	chat, err := w.s.CreateChat(ctx, "", "")
	require.NoError(t, err)

	pi := ArtifactMaker{Bot: chat.Bot, Chat: chat.ID}
	reply := func(text string) int64 {
		t.Helper()
		w.advance(time.Second)

		m, err := w.s.AddChatMessage(ctx, chat.ID, ChatCoordinator, text)
		require.NoError(t, err)

		return m.Seq
	}

	reply("Hello.")
	w.advance(time.Second)

	a, _, err := w.s.ShowArtifact(ctx, pi, ShowArtifact{Title: "Sales", Pack: page(t, "v1")})
	require.NoError(t, err)

	got, err := w.s.ArtifactsBeforeReply(ctx, chat.ID, reply("Here it is."))
	require.NoError(t, err)
	assert.Equal(t, []ArtifactRef{{ID: a.ID, Title: "Sales", Type: artifactpack.TypeHTML}}, got)

	got, err = w.s.ArtifactsBeforeReply(ctx, chat.ID, reply("Anything else?"))
	require.NoError(t, err)
	assert.Empty(t, got)

	w.advance(time.Second)
	_, _, err = w.s.ShowArtifact(ctx, pi, ShowArtifact{ID: a.ID, Title: "Sales by region", Pack: page(t, "v2")})
	require.NoError(t, err)

	got, err = w.s.ArtifactsBeforeReply(ctx, chat.ID, reply("Added the regions."))
	require.NoError(t, err)
	assert.Equal(t, []ArtifactRef{{ID: a.ID, Title: "Sales by region", Type: artifactpack.TypeHTML}}, got)
}
