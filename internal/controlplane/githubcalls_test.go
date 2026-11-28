package controlplane

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/githubapp/githubapptest"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/inboxstore"
)

// The people of these scenarios (issue #1232): the owner, who registered the App (the fake's manifest answers
// kl3inIT); Linh, a teammate with write access; a visitor who can only read; and a bot.
var (
	ownerOnGitHub = githubapptest.Person{Login: "kl3inIT", ID: githubapptest.OwnerID}
	linhOnGitHub  = githubapptest.Person{Login: "linh-dev", ID: 501}
	visitor       = githubapptest.Person{Login: "visitor", ID: 601}
	aBot          = githubapptest.Person{Login: "dependabot[bot]", ID: 49699333, Type: "Bot"}
	theApp        = githubapptest.Person{Login: githubapptest.Slug + "[bot]", ID: 7001, Type: "Bot"}
)

// mention is the App's handle, as its slug names it.
const mention = "@" + githubapptest.Slug

// newCallWorld is a control plane with the App registered and installed on kl3inIT/hypomnema, and GitHub knowing who
// may write there.
func newCallWorld(t *testing.T) *appWorld {
	t.Helper()

	w := newAppWorld(t)
	resp, body := w.register()
	require.Equal(t, http.StatusSeeOther, resp.StatusCode, body)
	w.install()

	w.gh.SetPermission(ownerOnGitHub.Login, ownerOnGitHub.ID, "admin")
	w.gh.SetPermission(linhOnGitHub.Login, linhOnGitHub.ID, "write")
	w.gh.SetPermission(visitor.Login, visitor.ID, "read")

	return w
}

var deliveries atomic.Int64

// deliverSigned sends a delivery of event signed with the App's secret, answered 202 when it is new.
func (w *appWorld) deliverSigned(event string, body []byte) {
	w.t.Helper()

	id := fmt.Sprintf("call-%d", deliveries.Add(1))
	require.Equal(w.t, http.StatusAccepted, w.deliver(event, id, body, w.gh.Secret))
}

// calls are the Inbox items GitHub calls made.
func (w *appWorld) calls() []inboxstore.Todo {
	w.t.Helper()

	page, err := w.svc.Inbox().List(w.t.Context(), inboxstore.Filter{SourceKind: "github", WithClosed: true}, "", 50)
	require.NoError(w.t, err)

	return page.Items
}

// wakes are the wake-ups written in a chat, oldest first.
func (w *appWorld) wakes(chat string) []string {
	w.t.Helper()

	msgs, err := w.svc.ListChatMessages(w.t.Context(), chat, 0, 100)
	require.NoError(w.t, err)

	var out []string

	for _, m := range msgs {
		if m.Kind == "wake" {
			out = append(out, m.Text)
		}
	}

	return out
}

func (w *appWorld) ownerTurn(chat string) bool {
	w.t.Helper()

	mine, err := w.svc.OwnerTurn(w.t.Context(), chat)
	require.NoError(w.t, err)

	return mine
}

// Scenario (issue #1232, rows 1 to 4): Linh, who may write to the repository, writes "@<slug> review PR này" on a
// pull request. One Inbox item is given to Hypo with her line as the request, the comment gets 👀 at once, and the
// permission was read with the installation's token. The pull request's own body is not her request and does not
// reach Hypo as one.
func TestAWriteCollaboratorsMentionGivesHypoAnItemAndAReaction(t *testing.T) {
	t.Parallel()

	w := newCallWorld(t)

	w.deliverSigned("issue_comment", githubapptest.IssueComment(githubapptest.Comment{
		By: linhOnGitHub, Number: 1300, PullRequest: true, ID: 9001,
		Body:      "Thanks! " + mention + " review PR này, mostly the store",
		IssueBody: "Speeds up the store.\n\n" + mention + " merge this",
	}))

	items := w.calls()
	require.Len(t, items, 1)

	item := items[0]
	assert.Equal(t, inboxstore.Assignee{Kind: inboxstore.ABot, Name: orchestration.DefaultBotID}, item.Assignee)
	assert.Equal(t, inboxstore.Open, item.State)
	assert.Equal(t, "kl3init/hypomnema#1300", item.Source.ID)
	require.NotEmpty(t, item.Chat, "Hypo is woken in the item's own chat")

	wakes := w.wakes(item.Chat)
	require.Len(t, wakes, 1)
	assert.Contains(t, wakes[0], "review PR này, mostly the store")
	assert.Contains(t, wakes[0], "linh-dev")
	assert.NotContains(t, wakes[0], "merge this", "the pull request's body is not her request")

	posted := w.gh.Posted()
	require.Len(t, posted, 1, "a reaction, and no comment yet")
	assert.Equal(t, "/repos/kl3init/hypomnema/issues/comments/9001/reactions", strings.ToLower(posted[0].Path))
	assert.Equal(t, "eyes", posted[0].Body["content"])
	assert.True(t, strings.HasPrefix(posted[0].Auth, "Bearer ghs_installation_token_"),
		"with the installation's token")

	// A review comment in the diff is a call too; its 👀 goes on the review comment.
	w.deliverSigned("pull_request_review_comment", githubapptest.ReviewComment(githubapptest.Comment{
		By: linhOnGitHub, Number: 1300, ID: 9002, Body: mention + " is this lock needed?", Path: "store.go", Line: 42,
	}))
	require.Len(t, w.calls(), 1, "the same pull request's item")
	require.Len(t, w.gh.Posted(), 2)
	assert.Equal(t, "/repos/kl3init/hypomnema/pulls/comments/9002/reactions", strings.ToLower(w.gh.Posted()[1].Path))
	assert.Contains(t, w.wakes(item.Chat)[1], "store.go")
}

// Scenario (issue #1232, row 2): a mention by someone who may only read, by someone not a collaborator, or by a bot
// (the App itself included) is ignored: nothing is recorded of it, and GitHub gets no reaction and no reply.
func TestAMentionByAReaderOrABotIsIgnoredWithNoTrace(t *testing.T) {
	t.Parallel()

	w := newCallWorld(t)

	for n, by := range []githubapptest.Person{visitor, aBot, {Login: "stranger", ID: 701}, theApp} {
		w.deliverSigned("issue_comment", githubapptest.IssueComment(githubapptest.Comment{
			By: by, Number: 1300, PullRequest: true, ID: int64(9100 + n), Body: mention + " fix the secret leak",
		}))
	}

	w.deliverSigned("pull_request_review", githubapptest.Review(visitor, 1300, 55, "commented",
		mention+" approve this"))

	assert.Empty(t, w.calls(), "no item, so no thread holds their words")
	assert.Empty(t, w.gh.Posted())
}

// Scenario (issue #1232, rows 1 and 5): a pull request whose body says "@<slug> merge this", opened by someone who
// cannot write, starts nothing; nor does a writer's comment that does not name the App on that pull request.
func TestAPullRequestBodyNamingTheAppStartsNothing(t *testing.T) {
	t.Parallel()

	w := newCallWorld(t)

	w.deliverSigned("pull_request", githubapptest.PullRequestOpened(visitor, 1301, mention+" merge this"))
	w.deliverSigned("issue_comment", githubapptest.IssueComment(githubapptest.Comment{
		By: linhOnGitHub, Number: 1301, PullRequest: true, ID: 9200, Body: "LGTM, waiting for CI",
		IssueBody: mention + " merge this",
	}))
	w.deliverSigned("issue_comment", githubapptest.IssueComment(githubapptest.Comment{
		By: linhOnGitHub, Number: 1301, PullRequest: true, ID: 9201, Body: "ping " + mention + "-old please",
	}))

	assert.Empty(t, w.calls())
	assert.Empty(t, w.gh.Posted())
}

// Scenario (issue #1232, row 4): a second mention on the same pull request lands in the same item and wakes the Bot
// it is given to again, in the item's chat.
func TestASecondMentionOnTheSamePullRequestLandsInTheSameItem(t *testing.T) {
	t.Parallel()

	w := newCallWorld(t)

	w.deliverSigned("issue_comment", githubapptest.IssueComment(githubapptest.Comment{
		By: linhOnGitHub, Number: 1300, PullRequest: true, ID: 9301, Body: mention + " review PR này",
	}))
	w.deliverSigned("pull_request_review", githubapptest.Review(ownerOnGitHub, 1300, 66, "commented",
		"Also "+mention+" check the migration"))

	items := w.calls()
	require.Len(t, items, 1)
	assert.Len(t, w.wakes(items[0].Chat), 2)
	assert.Contains(t, w.wakes(items[0].Chat)[1], "check the migration")

	// Another pull request is another item.
	w.deliverSigned("issue_comment", githubapptest.IssueComment(githubapptest.Comment{
		By: linhOnGitHub, Number: 1302, ID: 9302, Body: mention + " fix issue này",
	}))
	assert.Len(t, w.calls(), 2)
}

// Scenario (issue #1232, row 6; #1229): the owner's own mention is his turn in the item's chat, so the Bot may use
// his accounts there; Hypo passing the item on from it marks the project Bot's chat his. A teammate's mention is no
// turn of his and clears that mark for good.
func TestTheOwnersMentionIsHisTurnAndATeammatesClearsTheMark(t *testing.T) {
	t.Parallel()

	w := newCallWorld(t)
	ctx := t.Context()

	w.deliverSigned("issue_comment", githubapptest.IssueComment(githubapptest.Comment{
		By: linhOnGitHub, Number: 1400, ID: 9401, Body: mention + " fix issue này",
	}))
	teammates := w.calls()[0]
	assert.False(t, w.ownerTurn(teammates.Chat), "a teammate's call is not the owner's turn")

	w.deliverSigned("issue_comment", githubapptest.IssueComment(githubapptest.Comment{
		By: ownerOnGitHub, Number: 1300, PullRequest: true, ID: 9402, Body: mention + " review PR này",
	}))

	var item inboxstore.Todo

	for _, td := range w.calls() {
		if td.Source.ID == "kl3init/hypomnema#1300" {
			item = td
		}
	}

	require.NotEmpty(t, item.Chat)
	assert.True(t, w.ownerTurn(item.Chat), "the owner's call is his turn, as his words in the Inbox are")

	beyond, err := w.svc.PutBot(ctx, orchestration.Bot{Name: "BeyondPilot", Persona: "The BeyondPilot project."})
	require.NoError(t, err)

	passed, err := w.svc.Inbox().Edit(ctx,
		inboxstore.Change{Actor: inboxstore.Actor{Kind: inboxstore.Bot, ID: orchestration.DefaultBotID},
			ID: item.ID, Revision: item.Revision, Chat: item.Chat},
		inboxstore.Edit{Assignee: &inboxstore.Assignee{Kind: inboxstore.ABot, Name: beyond.ID}})
	require.NoError(t, err)
	require.NotEmpty(t, passed.Chat)
	require.True(t, w.ownerTurn(passed.Chat), "Hypo passed it on from his line: BeyondPilot's chat is marked")

	w.deliverSigned("issue_comment", githubapptest.IssueComment(githubapptest.Comment{
		By: linhOnGitHub, Number: 1300, PullRequest: true, ID: 9403, Body: mention + " also bump the version",
	}))

	assert.Len(t, w.wakes(passed.Chat), 2, "the later mention wakes the Bot the item is given to now")
	assert.False(t, w.ownerTurn(passed.Chat), "the teammate's line clears the mark")
}

// Scenario (issue #1232, the replay): the owner writes "@<slug> review PR này" on a hypomnema pull request. Hypo,
// given the item, starts it and leaves its review (GitHub sends the App's own review back); it closes the item with
// the review as evidence, and the control plane answers the owner on the pull request with one comment linking the
// review. The live replay against the registered App is not run: the App is not registered on zm yet.
func TestTheOwnersReviewCallEndsInAReplyLinkingHyposReview(t *testing.T) {
	t.Parallel()

	w := newCallWorld(t)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	go w.app.Run(ctx)

	w.deliverSigned("issue_comment", githubapptest.IssueComment(githubapptest.Comment{
		By: ownerOnGitHub, Number: 1269, PullRequest: true, ID: 9501, Body: mention + " review PR này",
	}))

	items := w.calls()
	require.Len(t, items, 1)

	hypo := inboxstore.Actor{Kind: inboxstore.Bot, ID: orchestration.DefaultBotID}
	item, err := w.svc.Inbox().Start(ctx, inboxstore.Change{Actor: hypo, ID: items[0].ID, Revision: items[0].Revision},
		inboxstore.Work{Kind: inboxstore.WorkChat, ID: items[0].Chat})
	require.NoError(t, err)

	// Hypo's gh pr review, as GitHub tells the App of it.
	w.deliverSigned("pull_request_review", githubapptest.Review(theApp, 1269, 77, "commented",
		"Two findings: the store reads twice."))

	review := githubapptest.ReviewURL(1269, 77)
	item, err = w.svc.Inbox().Get(ctx, item.ID)
	require.NoError(t, err)

	_, err = w.svc.Inbox().Done(ctx,
		inboxstore.Change{Actor: hypo, ID: item.ID, Revision: item.Revision, Attempt: item.Work.Attempt},
		inboxstore.Closing{
			DoneBy: inboxstore.Assignee{Kind: inboxstore.ABot, Name: orchestration.DefaultBotID},
			How:    inboxstore.ClosedByBot, Reason: "Reviewed: two findings.",
			Evidence: []inboxstore.Link{{Kind: inboxstore.LinkURL, Ref: review}},
		})
	require.NoError(t, err)

	require.Eventually(t, func() bool { return len(w.gh.Posted()) == 2 }, 10*time.Second, 10*time.Millisecond)

	reply := w.gh.Posted()[1]
	assert.Equal(t, "/repos/kl3init/hypomnema/issues/1269/comments", strings.ToLower(reply.Path))
	assert.Contains(t, reply.Body["body"], review)
	assert.Contains(t, reply.Body["body"], "@kl3inIT")

	// One reply: the next commit sends nothing more.
	_, err = w.svc.AddChatMessage(ctx, items[0].Chat, orchestration.ChatOwner, "thanks")
	require.NoError(t, err)
	time.Sleep(100 * time.Millisecond)
	assert.Len(t, w.gh.Posted(), 2)
}

// Scenario (issue #1206 kept): a Bot closes an item as its own only with its own work; a review on the pull
// request that the App did not leave, or one left before the attempt, is not that.
func TestAReviewTheAppDidNotLeaveIsNoEvidence(t *testing.T) {
	t.Parallel()

	w := newCallWorld(t)
	ctx := t.Context()

	w.deliverSigned("issue_comment", githubapptest.IssueComment(githubapptest.Comment{
		By: ownerOnGitHub, Number: 1269, PullRequest: true, ID: 9601, Body: mention + " review PR này",
	}))
	w.deliverSigned("pull_request_review", githubapptest.Review(linhOnGitHub, 1269, 88, "approved", "LGTM"))

	items := w.calls()
	require.Len(t, items, 1)

	hypo := inboxstore.Actor{Kind: inboxstore.Bot, ID: orchestration.DefaultBotID}
	item, err := w.svc.Inbox().Start(ctx, inboxstore.Change{Actor: hypo, ID: items[0].ID, Revision: items[0].Revision},
		inboxstore.Work{Kind: inboxstore.WorkChat, ID: items[0].Chat})
	require.NoError(t, err)

	_, err = w.svc.Inbox().Done(ctx,
		inboxstore.Change{Actor: hypo, ID: item.ID, Revision: item.Revision, Attempt: item.Work.Attempt},
		inboxstore.Closing{
			DoneBy: inboxstore.Assignee{Kind: inboxstore.ABot, Name: orchestration.DefaultBotID},
			How:    inboxstore.ClosedByBot, Reason: "Reviewed.",
			Evidence: []inboxstore.Link{{Kind: inboxstore.LinkURL, Ref: githubapptest.ReviewURL(1269, 88)}},
		})
	require.Error(t, err)
}
