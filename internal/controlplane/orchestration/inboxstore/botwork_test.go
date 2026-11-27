package inboxstore_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/inboxstore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/storeerr"
	"github.com/kl3inIT/hypomnema/internal/shared/artifactpack"
)

// ranFor is a Run the Bot doing item td started from the item's own chat, as a Bot's `hypomnema run` from that chat
// makes one (runs.chat_id, runs.bot_id), with one task and its dispatch, which gave pull request pr as its evidence
// when it finished (dispatches.result), as a worker's finish does. It gives the Run as evidence.
func (w *world) ranFor(t *testing.T, td inboxstore.Todo, id, pr string) []inboxstore.Link {
	t.Helper()

	return w.ranAs(t, td, id, "implementer", pr)
}

// ranAs is ranFor with the task's profile: an implementer opens the pull request it reports, a reviewer only names
// the one it reviewed.
func (w *world) ranAs(t *testing.T, td inboxstore.Todo, id, profile, pr string) []inboxstore.Link {
	t.Helper()

	require.NotNil(t, td.Work, "the item is doing")
	require.NotEmpty(t, td.Chat, "the item has its own chat")

	now := w.clock.now.UnixMilli()
	result, err := json.Marshal(map[string]any{"summary": "Fixed.", "evidence": []map[string]string{
		{"kind": "pull_request", "ref": pr},
	}})
	require.NoError(t, err)

	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO runs (id, objective, status, created_by, created_at, updated_at, chat_id, bot_id)
			VALUES (?, 'Fix it', 'open', 'bot', ?, ?, ?, ?)`, []any{id, now, now, td.Chat, td.Work.Bot}},
		{`INSERT INTO tasks (id, run_id, position, profile, spec, status, created_at, updated_at)
			VALUES (?, ?, 0, ?, 'Fix it', 'succeeded', ?, ?)`, []any{id + "_t", id, profile, now, now}},
		{`INSERT INTO conversations (id, task_id, node_id, worktree, runtime, account_ref, status, created_at,
			updated_at) VALUES (?, ?, 'node_1', 'wt', 'claude', 'acc', 'open', ?, ?)`,
			[]any{id + "_c", id + "_t", now, now}},
		{`INSERT INTO dispatches (id, task_id, conversation_id, node_id, status, created_at, updated_at, result)
			VALUES (?, ?, ?, 'node_1', 'succeeded', ?, ?, ?)`,
			[]any{id + "_d", id + "_t", id + "_c", now, now, string(result)}},
	} {
		_, err := w.db.ExecContext(t.Context(), stmt.sql, stmt.args...)
		require.NoError(t, err, stmt.sql)
	}

	return []inboxstore.Link{{Kind: inboxstore.LinkRun, Ref: id}}
}

// Scenario: review round 2 of PR #1243. Lan asks the owner in the team's WhatsApp group; he hands the item to kai,
// which asks to send "em nhận rồi ạ" and has it approved; then the owner answers Lan himself in the same group. His
// answer is his, though it is from his account, in that chat and after the approval: what kai sent is only what its
// send recorded (RecordSent), in this attempt. A send it recorded in an earlier attempt is not this attempt's work.
func TestReviewApprovalDoesNotIdentifyReplyAuthor(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newWorld(t)
	w.inbox.WithSent(teamMessages())
	itsOwn := func(evidence ...inboxstore.Link) inboxstore.Closing {
		return inboxstore.Closing{DoneBy: inboxstore.Assignee{Kind: inboxstore.ABot, Name: w.kai},
			How: inboxstore.ClosedByBot, Evidence: evidence}
	}
	message := func(id string) inboxstore.Link {
		return inboxstore.Link{Kind: inboxstore.LinkWhatsAppMessage, Ref: id, Account: "main", Chat: "team"}
	}

	doing := w.handedTo(t, w.teamAsk(t, inboxstore.Open))
	review, err := w.inbox.LinkApproval(ctx, by(system, doing), w.approval(t, "apr_ack", "pending", doing))
	require.NoError(t, err)
	w.decide(t, review.ApprovalID, "approved")
	again, err := w.inbox.ApprovalApproved(ctx, by(system, review))
	require.NoError(t, err)

	// "mine" is the owner's own answer, sent from his account in the group after the approval (teamMessages).
	_, err = w.inbox.Done(ctx, inAttempt(bot(w.kai), again), itsOwn(message("mine")))
	require.ErrorIs(t, err, storeerr.ErrInvalid, "the owner's answer is not what kai sent")

	sent, err := w.inbox.RecordSent(ctx, inAttempt(bot(w.kai), again), message("3EB0ACK"))
	require.NoError(t, err)

	_, err = w.inbox.Done(ctx, inAttempt(bot(w.kai), sent), itsOwn(message("mine")))
	require.ErrorIs(t, err, storeerr.ErrInvalid, "a send recorded is not every message of the chat")

	// Released and started again: the earlier attempt's send is not this one's work.
	released, err := w.inbox.Release(ctx, inAttempt(bot(w.kai), sent), "Lan asked something else.")
	require.NoError(t, err)
	restarted, err := w.inbox.Start(ctx, by(bot(w.kai), released), inboxstore.Work{Kind: inboxstore.WorkChat,
		ID: w.chat})
	require.NoError(t, err)
	_, err = w.inbox.Done(ctx, inAttempt(bot(w.kai), restarted), itsOwn(message("3EB0ACK")))
	require.ErrorIs(t, err, storeerr.ErrInvalid, "a send of an earlier attempt")

	resent, err := w.inbox.RecordSent(ctx, inAttempt(bot(w.kai), restarted), message("3EB0ACK2"))
	require.NoError(t, err)
	done, err := w.inbox.Done(ctx, inAttempt(bot(w.kai), resent), itsOwn(message("3EB0ACK2")))
	require.NoError(t, err)
	require.NotNil(t, done.DoneBy)
	assert.Equal(t, inboxstore.ABot, done.DoneBy.Kind)

	// A send is recorded only by the Bot doing the item, in its attempt.
	other := w.handedTo(t, w.teamAsk(t, inboxstore.Open))
	_, err = w.inbox.RecordSent(ctx, by(bot(w.lan), other), message("x"))
	require.Error(t, err, "another Bot's send")
}

// Scenario: review round 2 of PR #1243. The owner handed kai Hà My's ask, then sent her the file himself and wrote
// its link in a comment on the item. The link is his: as kai's own evidence it shows nothing, and neither does a Run
// or pull request that is not of this attempt. kai's own file, posted on the item in this attempt with
// inbox_comment's files, is its work.
func TestReviewOwnerReplyWithOwnerFileURL(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newWorld(t)
	itsOwn := func(evidence ...inboxstore.Link) inboxstore.Closing {
		return inboxstore.Closing{DoneBy: inboxstore.Assignee{Kind: inboxstore.ABot, Name: w.kai},
			How: inboxstore.ClosedByBot, Evidence: evidence}
	}

	doing := w.handedTo(t, w.threadAsk(t))
	his, err := w.inbox.Comment(ctx, owner, doing.ID, "Anh gửi rồi: https://drive.example.com/q3-dashboards.xlsx")
	require.NoError(t, err)
	doing.Revision = his.Revision

	other := w.doing(t)
	elsewhere := w.ranFor(t, other, "run_other", "https://github.com/o/r/pull/9")

	for name, evidence := range map[string][]inboxstore.Link{
		"his file's link":    {{Kind: inboxstore.LinkURL, Ref: "https://drive.example.com/q3-dashboards.xlsx"}},
		"his comment":        commentOf(last(his)),
		"a Run of no item":   {{Kind: inboxstore.LinkRun, Ref: "run_none"}},
		"another item's":     elsewhere,
		"its pull request":   {{Kind: inboxstore.LinkPullRequest, Ref: "https://github.com/o/r/pull/9"}},
		"a pull request":     prLink,
		"a mail":             {{Kind: inboxstore.LinkMail, Ref: "18c2f0a9b1d2e3f5"}},
		"its chat's message": w.said,
	} {
		_, err := w.inbox.Done(ctx, inAttempt(bot(w.kai), doing), itsOwn(evidence...))
		require.ErrorIs(t, err, storeerr.ErrInvalid, "%s is no work of kai's on this item", name)
	}

	commented, err := w.inbox.CommentWith(ctx, bot(w.kai), doing.ID, "Bảng tổng hợp đây ạ.",
		inboxstore.Attached{Files: []string{"exports/q3-dashboards.xlsx"}, From: w.chat,
			Read: []string{"exports/q3-dashboards.xlsx"}})
	require.NoError(t, err)

	doing.Revision = commented.Revision
	done, err := w.inbox.Done(ctx, inAttempt(bot(w.kai), doing), itsOwn(commentOf(last(commented))...))
	require.NoError(t, err)
	require.NotNil(t, done.DoneBy)
	assert.Equal(t, inboxstore.ABot, done.DoneBy.Kind)
}

// Scenario: review rounds 1 and 2 of PR #1243. The owner hands kai a bug Tùng reported in a Lark thread; kai starts a
// Run from the item's chat, whose worker opens the pull request: the Run, or its pull request, closes the item as
// kai's own, as before #1206, whatever someone asked of the owner.
func TestReviewDelegatedFix(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newWorld(t)
	itsOwn := func(evidence ...inboxstore.Link) inboxstore.Closing {
		return inboxstore.Closing{DoneBy: inboxstore.Assignee{Kind: inboxstore.ABot, Name: w.kai},
			How: inboxstore.ClosedByBot, Evidence: evidence}
	}

	byPR := w.handedTo(t, w.threadAsk(t))
	w.ranFor(t, byPR, "run_fix", "https://github.com/o/r/pull/12")
	done, err := w.inbox.Done(ctx, inAttempt(bot(w.kai), byPR),
		itsOwn(inboxstore.Link{Kind: inboxstore.LinkPullRequest, Ref: "https://github.com/o/r/pull/12"}))
	require.NoError(t, err)
	require.NotNil(t, done.DoneBy)
	assert.Equal(t, inboxstore.ABot, done.DoneBy.Kind)

	byRun := w.handedTo(t, w.threadAsk(t))
	done, err = w.inbox.Done(ctx, inAttempt(bot(w.kai), byRun),
		itsOwn(w.ranFor(t, byRun, "run_fix2", "https://github.com/o/r/pull/13")...))
	require.NoError(t, err)
	assert.Equal(t, inboxstore.Done, done.State)

	// A Run started before the attempt is not its work.
	early := w.handedTo(t, w.threadAsk(t))
	w.clock.now = w.clock.now.Add(-time.Hour)
	before := w.ranFor(t, early, "run_before", "https://github.com/o/r/pull/14")
	w.clock.now = w.clock.now.Add(time.Hour)
	_, err = w.inbox.Done(ctx, inAttempt(bot(w.kai), early), itsOwn(before...))
	require.ErrorIs(t, err, storeerr.ErrInvalid)
}

// Scenario: review round 3 of PR #1243. Lan asks the owner in the team's WhatsApp group; kai, doing the item, sends
// "em nhận rồi ạ" with his approval, and its send is recorded; then the owner answers Lan himself and links the file
// he made. kai's own close is refused while its evidence holds his answer or his link: every piece of evidence it
// gives must be its own work, since the close records them all as its own. Its ack alone is its own work.
func TestRound3ApprovedAckPlusOwnersReplyAndFile(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newWorld(t)
	w.inbox.WithSent(teamMessages())
	itsOwn := func(evidence ...inboxstore.Link) inboxstore.Closing {
		return inboxstore.Closing{DoneBy: inboxstore.Assignee{Kind: inboxstore.ABot, Name: w.kai},
			How: inboxstore.ClosedByBot, Evidence: evidence}
	}
	message := func(id string) inboxstore.Link {
		return inboxstore.Link{Kind: inboxstore.LinkWhatsAppMessage, Ref: id, Account: "main", Chat: "team"}
	}

	doing := w.handedTo(t, w.teamAsk(t, inboxstore.Open))
	sent, err := w.inbox.RecordSent(ctx, inAttempt(bot(w.kai), doing), message("3EB0ACK"))
	require.NoError(t, err)

	his := inboxstore.Link{Kind: inboxstore.LinkURL, Ref: "https://drive.example.com/luma-keys.xlsx"}
	for name, evidence := range map[string][]inboxstore.Link{
		"its ack and his answer":       {message("3EB0ACK"), message("mine")},
		"its ack and his file":         {message("3EB0ACK"), his},
		"its ack, his answer and file": {message("3EB0ACK"), message("mine"), his},
		"his answer and its ack":       {message("mine"), message("3EB0ACK")},
	} {
		_, err := w.inbox.Done(ctx, inAttempt(bot(w.kai), sent), itsOwn(evidence...))
		require.ErrorIs(t, err, storeerr.ErrInvalid, "%s: his work is not kai's", name)
	}

	w.unchanged(t, sent)

	done, err := w.inbox.Done(ctx, inAttempt(bot(w.kai), sent), itsOwn(message("3EB0ACK")))
	require.NoError(t, err)
	require.NotNil(t, done.DoneBy)
	assert.Equal(t, inboxstore.ABot, done.DoneBy.Kind)
}

// Scenario: review round 3 of PR #1243. kai starts a Run from the item's chat whose reviewer task reports the pull
// request it reviewed, the owner's: that pull request is not kai's work, as the pull requests the control plane
// follows leave a review task out (pullrequests.go, reviewerProfile). An implementer's is.
func TestRound3ReviewerEvidenceIsNotItsPullRequest(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newWorld(t)
	itsOwn := func(evidence ...inboxstore.Link) inboxstore.Closing {
		return inboxstore.Closing{DoneBy: inboxstore.Assignee{Kind: inboxstore.ABot, Name: w.kai},
			How: inboxstore.ClosedByBot, Evidence: evidence}
	}
	owners := inboxstore.Link{Kind: inboxstore.LinkPullRequest, Ref: "https://github.com/o/r/pull/77"}

	for _, profile := range []string{"reviewer", "reviewer-strict"} {
		doing := w.handedTo(t, w.threadAsk(t))
		w.ranAs(t, doing, "run_"+profile, profile, owners.Ref)
		_, err := w.inbox.Done(ctx, inAttempt(bot(w.kai), doing), itsOwn(owners))
		require.ErrorIs(t, err, storeerr.ErrInvalid, "%s reported the pull request it reviewed", profile)
	}

	doing := w.handedTo(t, w.threadAsk(t))
	w.ranAs(t, doing, "run_impl", "implementer", owners.Ref)
	done, err := w.inbox.Done(ctx, inAttempt(bot(w.kai), doing), itsOwn(owners))
	require.NoError(t, err)
	assert.Equal(t, inboxstore.Done, done.State)
}

// Scenario: review round 3 of PR #1243. A file a Bot's comment names counts as its work only when the agent API read
// it in the Bot's folder (Attached.Checked): a path alone, which may name a file never made, does not.
func TestRound3MissingFileIsNotWork(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newWorld(t)
	itsOwn := func(evidence ...inboxstore.Link) inboxstore.Closing {
		return inboxstore.Closing{DoneBy: inboxstore.Assignee{Kind: inboxstore.ABot, Name: w.kai},
			How: inboxstore.ClosedByBot, Evidence: evidence}
	}

	doing := w.handedTo(t, w.threadAsk(t))
	unread, err := w.inbox.CommentWith(ctx, bot(w.kai), doing.ID, "Báo cáo đây ạ.",
		inboxstore.Attached{Files: []string{"exports/never-made.xlsx"}, From: w.chat})
	require.NoError(t, err)
	doing.Revision = unread.Revision
	_, err = w.inbox.Done(ctx, inAttempt(bot(w.kai), doing), itsOwn(commentOf(last(unread))...))
	require.ErrorIs(t, err, storeerr.ErrInvalid, "a path the agent API did not read")

	read, err := w.inbox.CommentWith(ctx, bot(w.kai), doing.ID, "Báo cáo đây ạ.",
		inboxstore.Attached{Files: []string{"exports/q3.xlsx"}, From: w.chat, Read: []string{"exports/q3.xlsx"}})
	require.NoError(t, err)
	doing.Revision = read.Revision
	done, err := w.inbox.Done(ctx, inAttempt(bot(w.kai), doing), itsOwn(commentOf(last(read))...))
	require.NoError(t, err)
	assert.Equal(t, inboxstore.Done, done.State)
}

// Scenario: review round 4 of PR #1243. The agent API reads a comment's files at the paths the comment keeps, after
// the store's cleaning ("./", spaces), and a comment is the Bot's work only when every file it keeps was read there:
// a path read as written ("other.md ") is not the path kept ("other.md"), and an Artifact the Bot showed before does
// not make a missing file its work.
func TestRound5FilesReadAtThePathKept(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newWorld(t)
	itsOwn := func(evidence ...inboxstore.Link) inboxstore.Closing {
		return inboxstore.Closing{DoneBy: inboxstore.Assignee{Kind: inboxstore.ABot, Name: w.kai},
			How: inboxstore.ClosedByBot, Evidence: evidence}
	}

	shown, _, err := w.svc.ShowArtifact(ctx, orchestration.ArtifactMaker{Bot: w.kai, Chat: w.chat},
		orchestration.ShowArtifact{Title: "Báo cáo cũ", Pack: oldPage(t)})
	require.NoError(t, err)

	doing := w.handedTo(t, w.threadAsk(t))
	for name, att := range map[string]inboxstore.Attached{
		"read as written, kept cleaned":  {Files: []string{"other.md "}, Read: []string{"other.md "}},
		"a missing file and an Artifact": {Files: []string{"missing.md"}, Artifacts: []string{shown.ID}},
		"one of two files read":          {Files: []string{"a.md", "b.md"}, Read: []string{"a.md"}},
	} {
		att.From = w.chat
		c, err := w.inbox.CommentWith(ctx, bot(w.kai), doing.ID, "Kết quả đây ạ.", att)
		require.NoError(t, err, name)
		doing.Revision = c.Revision
		_, err = w.inbox.Done(ctx, inAttempt(bot(w.kai), doing), itsOwn(commentOf(last(c))...))
		require.ErrorIs(t, err, storeerr.ErrInvalid, name)
	}

	c, err := w.inbox.CommentWith(ctx, bot(w.kai), doing.ID, "Kết quả đây ạ.", inboxstore.Attached{
		Files: []string{"./result.md "}, Read: []string{"result.md"}, Artifacts: []string{shown.ID}, From: w.chat})
	require.NoError(t, err)
	doing.Revision = c.Revision
	done, err := w.inbox.Done(ctx, inAttempt(bot(w.kai), doing), itsOwn(commentOf(last(c))...))
	require.NoError(t, err)
	assert.Equal(t, inboxstore.Done, done.State)
}

// oldPage is an Artifact's one page.
func oldPage(t *testing.T) artifactpack.Pack {
	t.Helper()

	p, err := artifactpack.Check([]artifactpack.File{{Path: "index.html", Data: []byte("<p>old</p>")}})
	require.NoError(t, err)

	return p
}
