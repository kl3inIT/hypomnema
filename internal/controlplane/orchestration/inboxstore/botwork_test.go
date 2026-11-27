package inboxstore_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/inboxstore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/storeerr"
)

// ranFor is a Run the Bot doing item td started from its attempt's chat, as a Bot's `hypomnema run` from that chat
// makes one (runs.chat_id, runs.bot_id), with one task and its dispatch, which gave pull request pr as its evidence
// when it finished (dispatches.result), as a worker's finish does. It gives the Run as evidence.
func (w *world) ranFor(t *testing.T, td inboxstore.Todo, id, pr string) []inboxstore.Link {
	t.Helper()

	require.NotNil(t, td.Work, "the item is doing")

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
			VALUES (?, 'Fix it', 'open', 'bot', ?, ?, ?, ?)`, []any{id, now, now, td.Work.ID, td.Work.Bot}},
		{`INSERT INTO tasks (id, run_id, position, profile, spec, status, created_at, updated_at)
			VALUES (?, ?, 0, 'implementer', 'Fix it', 'succeeded', ?, ?)`, []any{id + "_t", id, now, now}},
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
		inboxstore.Attached{Files: []string{"exports/q3-dashboards.xlsx"}, From: w.chat})
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
