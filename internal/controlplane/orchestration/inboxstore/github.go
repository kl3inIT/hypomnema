package inboxstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/storeutil"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/storeerr"
)

// The calls of the owner's GitHub App (issue #1232): someone who may write to a repository the App is installed on
// names it (@<app-slug>) in a comment, a review comment or a review, and the control plane gives the issue or pull
// request one Inbox item, given to the main Bot, the hub (#1231), as Omnara launches an agent on a mention and later
// mentions reach that same agent (docs/integrations/github.mdx:258-304). The App's package checks who called, from
// GitHub's signed delivery, before this store is reached; this store keeps the item, its thread and the calls.

// FromGitHub is a source: a call of the GitHub App on an issue or pull request. ID is its owner/name#number in lower
// case, Ref the first call (a LinkURL to the comment or review). Only Call makes such an item.
const FromGitHub = "github"

// WakeCall is a kind of Wake: a call of the GitHub App on the item's issue or pull request.
const WakeCall = "call"

// EventReviewed records on an item the review the GitHub App made on its pull request in a Bot's attempt on it
// (RecordReview): after.review is the review's address, after.review_id GitHub's id of it, after.work_attempt the
// attempt.
const EventReviewed = "reviewed"

// The kinds of GitHubCall: a comment on an issue or a pull request's conversation, a comment in a pull request's
// diff, or a review's body.
const (
	CallComment       = "comment"
	CallReviewComment = "review_comment"
	CallReview        = "review"
)

// GitHubCall is one call of the GitHub App, as its package read it from GitHub's signed delivery.
type GitHubCall struct {
	// Delivery is GitHub's id of the delivery: a call delivered again is handled once.
	Delivery string
	// Repository is owner/name in lower case; Number the issue or pull request, PullRequest whether it is one.
	Repository  string
	Number      int
	PullRequest bool
	// Title is the issue's or pull request's title: someone's words, data, not a request.
	Title string
	// Kind is CallComment, CallReviewComment or CallReview; CommentID GitHub's id of the comment or review and URL
	// its address.
	Kind      string
	CommentID int64
	URL       string
	// ReplyTo is, for a review comment, the first comment of its thread: GitHub takes a reply only to it. 0 for
	// any other call.
	ReplyTo int64
	// Login is who called, GitHub's sender of the delivery, never the text; Owner says it is the owner's account,
	// the one that registered the App (#1232, row 7).
	Login string
	Owner bool
	// Request is the caller's own words after the mention on its line, the only request (#1232, rows 4 and 5);
	// Place where a review
	// comment is in the diff, "path:line".
	Request string
	Place   string
}

// Source is the item's source for the call's issue or pull request.
func (c GitHubCall) Source() string { return fmt.Sprintf("%s#%d", c.Repository, c.Number) }

// Actor is whom the call's comment in the thread and its wake-up are by: the owner for his own account, as his words
// in the Inbox are (#1232, row 6), else the system, named by the caller's login.
func (c GitHubCall) Actor() Actor {
	if c.Owner {
		return Actor{Kind: Owner}
	}

	return Actor{Kind: System, ID: "github:" + c.Login}
}

func checkCall(c GitHubCall) error {
	switch {
	case c.Delivery == "" || len(c.Delivery) > 128:
		return fmt.Errorf("%w: a call names its delivery", storeerr.ErrInvalid)
	case c.Repository == "" || c.Repository != strings.ToLower(c.Repository) || !strings.Contains(c.Repository, "/"):
		return fmt.Errorf("%w: a call names its repository, owner/name in lower case", storeerr.ErrInvalid)
	case c.Number <= 0 || c.CommentID <= 0:
		return fmt.Errorf("%w: a call names its issue or pull request and its comment", storeerr.ErrInvalid)
	case c.Kind != CallComment && c.Kind != CallReviewComment && c.Kind != CallReview:
		return fmt.Errorf("%w: unknown kind of call %q", storeerr.ErrInvalid, c.Kind)
	case c.Login == "" || len(c.Login) > MaxName:
		return fmt.Errorf("%w: a call names who made it", storeerr.ErrInvalid)
	case c.URL == "" || utf8.RuneCountInString(c.URL) > MaxRef:
		return fmt.Errorf("%w: a call names its address", storeerr.ErrInvalid)
	}

	return nil
}

// Call takes a call of the GitHub App (issue #1232): the issue's or pull request's item that is not closed gets it,
// or a new one, open and given to the main Bot, is made. An item the owner holds is given to the main Bot again; one
// a Bot holds stays with it. The caller's words go into the item's thread and wake that Bot in the item's own chat.
// It returns the item and false when the delivery was taken before. A closed item is reopened by the call (reopenTx),
// the owner's one exception to "only the owner reopens" (2026-11-28).
func (s *Store) Call(ctx context.Context, c GitHubCall) (Todo, bool, error) {
	c.Request = strings.TrimSpace(c.Request)
	if r := []rune(c.Request); len(r) > MaxComment {
		c.Request = string(r[:MaxComment-1]) + "…"
	}

	if err := checkCall(c); err != nil {
		return Todo{}, false, err
	}

	if s.mainBot == "" {
		return Todo{}, false, fmt.Errorf("%w: no main Bot takes GitHub's calls", storeerr.ErrUnavailable)
	}

	var (
		got   Todo
		fresh bool
	)

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		taken, err := q.GetTodoGitHubCall(ctx, c.Delivery)

		switch {
		case err == nil:
			got, err = s.get(ctx, q, taken.TodoID)

			return err
		case !errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("reading GitHub's call %s: %w", c.Delivery, err)
		}

		id, err := s.callItemTx(ctx, q, now, c)
		if err != nil {
			return err
		}

		cur, err := q.GetTodo(ctx, id)
		if err != nil {
			return storeutil.NotFound(err, "to-do", id)
		}

		actor := c.Actor()
		if _, err := s.wakeTx(ctx, q, now, id, Wake{
			By: actor, Kind: WakeCall, Bot: cur.Assignee, Text: c.Request, Comment: int(cur.Revision) + 1, Call: &c,
		}); err != nil {
			return err
		}

		after := map[string]any{"text": c.Request, "github": map[string]any{
			"login": c.Login, "url": c.URL, "kind": c.Kind,
		}}
		if err := commentTx(ctx, q, now, actor, id, cur.Revision, after); err != nil {
			return err
		}

		if err := q.InsertTodoGitHubCall(ctx, store.InsertTodoGitHubCallParams{
			Delivery: c.Delivery, TodoID: id, Repository: c.Repository, Number: int64(c.Number), Kind: c.Kind,
			CommentID: c.CommentID, ReplyTo: c.ReplyTo, Login: c.Login, Url: c.URL, Now: now,
		}); err != nil {
			return fmt.Errorf("keeping GitHub's call %s: %w", c.Delivery, err)
		}

		fresh = true
		got, err = s.get(ctx, q, id)

		return err
	})
	if err != nil {
		return Todo{}, false, err
	}

	return got, fresh, nil
}

// callTitle is a new item's title: where it was called, and the issue's or pull request's title.
func callTitle(c GitHubCall) string {
	title := strings.Join(strings.Fields(c.Source()+" "+c.Title), " ")
	if r := []rune(title); len(r) > MaxTitle {
		title = string(r[:MaxTitle-1]) + "…"
	}

	return title
}

// callItemTx is the item a call goes to: the issue's or pull request's that is not closed, given to a Bot (the main
// Bot when no Bot holds it), or a new one.
func (s *Store) callItemTx(ctx context.Context, q *store.Queries, now int64, c GitHubCall) (string, error) {
	main := Assignee{Kind: ABot, Name: s.mainBot}
	system := Actor{Kind: System, ID: "github"}

	row, err := q.LatestTodoOfSource(ctx, store.LatestTodoOfSourceParams{SourceKind: FromGitHub, SourceID: c.Source()})

	switch {
	case err == nil && Closed(row.State):
		return row.ID, s.reopenTx(ctx, q, now, c, row)
	case err == nil && !Closed(row.State) && row.AssigneeKind == ABot:
		return row.ID, nil
	case err == nil && !Closed(row.State):
		_, err := s.changeTx(ctx, q, now, Change{Actor: system, ID: row.ID, Revision: int(row.Revision)}, EventEdited,
			func(q *store.Queries, cur store.Todo, next *store.Todo, _ int64) (map[string]any, error) {
				if err := checkAssignee(ctx, q, main); err != nil {
					return nil, err
				}

				give(cur, next, main)
				next.SnoozedUntil.Valid = false

				if cur.State == Proposed {
					next.State = Open
				}

				return map[string]any{}, nil
			})

		return row.ID, err
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return "", fmt.Errorf("reading the item of %s: %w", c.Source(), err)
	}

	if err := checkAssignee(ctx, q, main); err != nil {
		return "", err
	}

	project, err := botsDepartment(ctx, q, main)
	if err != nil {
		return "", err
	}

	ref, err := optionalJSON(&Link{Kind: LinkURL, Ref: c.URL})
	if err != nil {
		return "", err
	}

	id := s.newID("tdo")
	if err := q.InsertTodo(ctx, store.InsertTodoParams{
		ID: id, Title: callTitle(c), DueKind: DueNone, AssigneeKind: main.Kind, Assignee: main.Name, State: Open,
		SourceKind: FromGitHub, SourceID: c.Source(), SourceRef: ref, CreatedByKind: system.Kind, CreatedBy: system.ID,
		ProjectID: project, Now: now,
	}); err != nil {
		return "", fmt.Errorf("adding the item of %s: %w", c.Source(), err)
	}

	made, err := q.GetTodo(ctx, id)
	if err != nil {
		return "", fmt.Errorf("reading the to-do added: %w", err)
	}

	if err := addEvent(ctx, q, id, now, 1, system, EventCreated, map[string]any{}, fields(made)); err != nil {
		return "", err
	}

	return id, queuePush(ctx, q, now, system, id)
}

// MadeReview is a review the GitHub App made on a pull request, as GitHub answered the POST that made it, and where
// it was made: the Bot whose gh sent it, the work of the chat it was sent from (a chat, or its Routine's run) and the
// attempt read before the POST went out (ReviewAttempt).
type MadeReview struct {
	Repository string
	Number     int
	ID         int64
	URL        string
	Bot        string
	Work       Work
	Attempt    string
}

// ReviewAttempt is the attempt a review the GitHub App makes now on a pull request would be the work of (issue
// #1232; review round 3 of PR #1323): the current attempt on the pull request's item, when bot is on it in work. It
// is read before the review's POST goes out and checked again when the review is recorded, as RecordSent records a
// message in the attempt its send named (issue #1206). "" for none.
func (s *Store) ReviewAttempt(ctx context.Context, repository string, number int, bot string, work Work,
) (string, error) {
	source := fmt.Sprintf("%s#%d", strings.ToLower(repository), number)

	row, err := store.New(s.db).LatestTodoOfSource(ctx, store.LatestTodoOfSourceParams{
		SourceKind: FromGitHub, SourceID: source,
	})

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", nil
	case err != nil:
		return "", fmt.Errorf("reading the item of %s: %w", source, err)
	case bot == "" || !onIt(row, bot, work):
		return "", nil
	}

	return row.WorkAttempt.String, nil
}

// onIt says whether bot's attempt on the item is in work.
func onIt(row store.Todo, bot string, work Work) bool {
	return (row.State == Doing || row.State == Review) && row.WorkBot.String == bot &&
		row.WorkKind.String == work.Kind && row.WorkID.String == work.ID
}

// reopenTx reopens the closed item of a call's issue or pull request (owner, 2026-11-28, on #1232's design row 4:
// later mentions land in the same item, as they reach the same agent in Omnara, docs/integrations/github.mdx:298):
// the one exception to "only the owner reopens", for a call that passed every check of the App's. The reopen is an
// event of who called (the owner, or the system as github:<login>) with the call's address, as Reopen clears the
// close; an item no Bot holds goes to the main Bot.
func (s *Store) reopenTx(ctx context.Context, q *store.Queries, now int64, c GitHubCall, row store.Todo) error {
	main := Assignee{Kind: ABot, Name: s.mainBot}

	_, err := s.changeTx(ctx, q, now, Change{Actor: c.Actor(), ID: row.ID, Revision: int(row.Revision)}, EventState,
		func(q *store.Queries, cur store.Todo, next *store.Todo, _ int64) (map[string]any, error) {
			if err := retake(ctx, q, cur); err != nil {
				return nil, err
			}

			next.State = Open
			next.DoneByKind.Valid, next.DoneBy.Valid = false, false
			next.RecordedByKind.Valid, next.RecordedBy.Valid = false, false
			next.ClosedHow.Valid, next.ClosedAt.Valid, next.DuplicateOf.Valid = false, false, false
			next.CloseReason, next.Evidence = "", "[]"

			if cur.AssigneeKind != ABot {
				if err := checkAssignee(ctx, q, main); err != nil {
					return nil, err
				}

				give(cur, next, main)
			}

			return map[string]any{"github": map[string]any{"login": c.Login, "url": c.URL, "kind": c.Kind}}, nil
		})

	return err
}

// RecordReview records on a pull request's item the review the GitHub App made on it, by the id GitHub gave it in
// its answer to the App's own POST (issue #1232; review round 2 of PR #1323), when the Bot's attempt on the item is
// in the chat the review was made from and is still the attempt read before the POST (round 3): then it is that attempt's work, the evidence its Bot closes the item with
// (botsWork). As RecordSent records a message when its send is made (issue #1206), it is recorded when it is made, so
// no review made before the attempt or elsewhere is ever its work, whatever GitHub's events say or when they come.
// False when it is not recorded.
func (s *Store) RecordReview(ctx context.Context, r MadeReview) (bool, error) {
	if r.URL == "" || utf8.RuneCountInString(r.URL) > MaxRef || r.ID <= 0 {
		return false, fmt.Errorf("%w: a review names its id and its address", storeerr.ErrInvalid)
	}

	if r.Bot == "" || r.Attempt == "" {
		return false, nil
	}

	source := fmt.Sprintf("%s#%d", strings.ToLower(r.Repository), r.Number)
	recorded := false

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		row, err := q.LatestTodoOfSource(ctx, store.LatestTodoOfSourceParams{SourceKind: FromGitHub, SourceID: source})

		switch {
		case errors.Is(err, sql.ErrNoRows):
			return nil
		case err != nil:
			return fmt.Errorf("reading the item of %s: %w", source, err)
		case !onIt(row, r.Bot, r.Work), row.WorkAttempt.String != r.Attempt:
			// Another attempt began meanwhile, or none is on: the review is not this one's.
			return nil
		}

		recorded = true

		return commentlessTx(ctx, q, now, Actor{Kind: Bot, ID: r.Bot}, row, EventReviewed, map[string]any{
			"review": r.URL, "review_id": r.ID, "work_attempt": row.WorkAttempt.String,
		})
	})
	if err != nil {
		return false, err
	}

	return recorded, nil
}

// commentlessTx records an event that changes no field at the item's revision, which it moves on, as commentTx does
// for a comment.
func commentlessTx(ctx context.Context, q *store.Queries, now int64, actor Actor, cur store.Todo, kind string,
	after map[string]any,
) error {
	n, err := q.TouchTodo(ctx, store.TouchTodoParams{Now: now, ID: cur.ID, Revision: cur.Revision})
	if err != nil {
		return fmt.Errorf("recording on to-do %s: %w", cur.ID, err)
	}

	if n != 1 {
		return fmt.Errorf("%w: to-do %s changed meanwhile", storeerr.ErrConflict, cur.ID)
	}

	return addEvent(ctx, q, cur.ID, now, cur.Revision+1, actor, kind, map[string]any{}, after)
}

// reviewedInAttempt says whether an event of the item is the GitHub App's review at the address ref that bot made
// in the item's current attempt (RecordReview).
func reviewedInAttempt(e store.TodoEvent, cur store.Todo, bot, ref string) bool {
	if e.Kind != EventReviewed || e.ActorKind != Bot || e.Actor != bot {
		return false
	}

	var after struct {
		Review  string `json:"review"`
		Attempt string `json:"work_attempt"`
	}

	return json.Unmarshal([]byte(e.After), &after) == nil && after.Review == ref && after.Attempt != "" &&
		after.Attempt == cur.WorkAttempt.String
}

// CallOf is a call an item was given, as kept: where and by whom, never its words.
type CallOf struct {
	Delivery   string
	Repository string
	Number     int
	Kind       string
	CommentID  int64
	ReplyTo    int64
	Login      string
	URL        string
}

// Reply is an item that closed with the calls of it not answered yet on GitHub, oldest first.
type Reply struct {
	Todo  Todo
	Calls []CallOf
}

// maxReplies bounds the calls one read of the replies due returns.
const maxReplies = 100

// Replies are the items closed whose calls wait for their answer on GitHub (issue #1232, row 3), each with its
// calls, the item that closed first first.
func (s *Store) Replies(ctx context.Context) ([]Reply, error) {
	var out []Reply

	err := s.snapshot(ctx, func(q *store.Queries) error {
		rows, err := q.ListGitHubCallsToReply(ctx, maxReplies)
		if err != nil {
			return fmt.Errorf("reading GitHub's calls to answer: %w", err)
		}

		at := map[string]int{}

		for _, r := range rows {
			n, ok := at[r.TodoID]
			if !ok {
				t, err := s.get(ctx, q, r.TodoID)
				if err != nil {
					return err
				}

				n = len(out)
				at[r.TodoID] = n
				out = append(out, Reply{Todo: t})
			}

			out[n].Calls = append(out[n].Calls, CallOf{
				Delivery: r.Delivery, Repository: r.Repository, Number: int(r.Number), Kind: r.Kind,
				CommentID: r.CommentID, ReplyTo: r.ReplyTo, Login: r.Login, URL: r.Url,
			})
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return out, nil
}

// Replied marks the calls a reply on GitHub answered, with its address.
func (s *Store) Replied(ctx context.Context, deliveries []string, replyURL string) error {
	list, err := json.Marshal(deliveries)
	if err != nil {
		return fmt.Errorf("writing the calls answered: %w", err)
	}

	return s.write(ctx, func(q *store.Queries, now int64) error {
		_, err := q.SetGitHubCallsReplied(ctx, store.SetGitHubCallsRepliedParams{
			Now: now, ReplyUrl: replyURL, Deliveries: string(list),
		})
		if err != nil {
			return fmt.Errorf("marking GitHub's calls answered: %w", err)
		}

		return nil
	})
}
