package orchestration

import (
	"context"
	"fmt"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/inboxstore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/routinestore"
)

// InboxNeedsYou is what the Inbox's Needs you holds, for the sidebar (issue #1093): the to-dos that need the owner,
// the pending Approvals and the open questions workers asked him, each once. The Approvals and questions are counted
// from their own tables, never copied into to-dos, as OpenMausBot's person inbox answers an ask "here as there"
// (server/host/person-inbox.ts:223-249).
func (s *Service) InboxNeedsYou(ctx context.Context) (int, error) {
	now := s.clock.Now()

	todos, err := s.inbox.NeedsYou(ctx, now)
	if err != nil {
		return 0, err
	}

	asks, err := s.read().CountOwnerAsks(ctx, now.UnixMilli())
	if err != nil {
		return 0, fmt.Errorf("counting the asks that wait for the owner: %w", err)
	}

	return todos + int(asks), nil
}

// RoutineRunOf is the live run of a Routine a Bot token acts in (issue #966), by which the Inbox names where an item
// a Bot found came from: the run of the Routine a script's token is made for (Token.Routine), or the run a prompt
// run's chat was opened for, whose pi's token is bound to that chat. Empty when the token is not a Bot's or acts in
// no live run: a chat with the owner, or a Routine's chat whose run ended.
func (s *Service) RoutineRunOf(ctx context.Context, tok Token) (string, error) {
	if tok.Kind != bearertoken.Bot {
		return "", nil
	}

	q := store.New(s.db)

	var (
		run *routinestore.RoutineRun
		err error
	)

	switch {
	case tok.RoutineRun != "":
		return tok.RoutineRun, nil
	case tok.Routine != "":
		run, err = routinestore.LiveRunTx(ctx, q, tok.Routine)
	case tok.Chat != "":
		run, err = routinestore.LiveRunOfChatTx(ctx, q, tok.Chat)
	}

	if err != nil || run == nil {
		return "", err
	}

	return run.ID, nil
}

// InboxWorkOf is where a Bot's pi in a chat works on an Inbox item (issue #1004), as the client API's StartTodo
// says: the Routine's run the chat was opened for while it is live, else the chat.
func (s *Service) InboxWorkOf(ctx context.Context, chatID string) (inboxstore.Work, error) {
	run, err := s.RoutineRunOf(ctx, Token{Kind: bearertoken.Bot, Chat: chatID})
	if err != nil {
		return inboxstore.Work{}, err
	}

	if run != "" {
		return inboxstore.Work{Kind: inboxstore.WorkRun, ID: run}, nil
	}

	return inboxstore.Work{Kind: inboxstore.WorkChat, ID: chatID}, nil
}

// AppReviewAttempt is the attempt a review the owner's GitHub App makes now through the service proxy, from chat by
// bot, would be the work of (issue #1232; review round 3 of PR #1323): read before the review's POST goes out. Only a
// pi token's own chat and Bot name one, the chat's own Bot: "" for none.
func (s *Service) AppReviewAttempt(ctx context.Context, chat, bot, repository string, number int) (string, error) {
	work, ok, err := s.appReviewWork(ctx, chat, bot)
	if err != nil || !ok {
		return "", err
	}

	return s.inbox.ReviewAttempt(ctx, repository, number, bot, work)
}

// RecordAppReview records a review the owner's GitHub App made through the service proxy on the item of its pull
// request (issue #1232): the work of the attempt read before its POST (AppReviewAttempt), when that attempt is still
// on the item, by the chat's own Bot named by the pi token. Without a Bot, a chat or an attempt nothing is recorded
// (review round 3 of PR #1323). False when it is not.
func (s *Service) RecordAppReview(ctx context.Context, chat, bot, attempt, repository string, number int, id int64,
	url string,
) (bool, error) {
	work, ok, err := s.appReviewWork(ctx, chat, bot)
	if err != nil || !ok || attempt == "" {
		return false, err
	}

	return s.inbox.RecordReview(ctx, inboxstore.MadeReview{
		Repository: repository, Number: number, ID: id, URL: url, Bot: bot, Work: work, Attempt: attempt,
	})
}

// appReviewWork is where a review made from chat by bot is work: the chat's work, when bot is the chat's own Bot.
func (s *Service) appReviewWork(ctx context.Context, chat, bot string) (inboxstore.Work, bool, error) {
	if chat == "" || bot == "" {
		return inboxstore.Work{}, false, nil
	}

	c, err := s.GetChat(ctx, chat)
	if err != nil {
		return inboxstore.Work{}, false, err
	}

	if c.Bot != bot {
		return inboxstore.Work{}, false, nil
	}

	work, err := s.InboxWorkOf(ctx, chat)

	return work, err == nil, err
}

// InboxAttempt is the attempt of a Bot's pi at the Inbox item it names (issue #1004): the item's current attempt
// when the Bot is doing it in this chat or the chat's Routine's run, so that an Approval the Bot asks while working
// on the item records it (NewApproval.AttemptID) and the Bot never handles attempt ids. Any other item is a conflict
// the Bot is told.
func (s *Service) InboxAttempt(ctx context.Context, todoID, bot, chatID string) (string, error) {
	t, err := s.inbox.Get(ctx, todoID)
	if err != nil {
		return "", err
	}

	w, err := s.InboxWorkOf(ctx, chatID)
	if err != nil {
		return "", err
	}

	if t.State != inboxstore.Doing || t.Work == nil || t.Work.Bot != bot || t.Work.Kind != w.Kind || t.Work.ID != w.ID {
		return "", fmt.Errorf("%w: to-do %s is not one you are doing here (it is %s); start it with inbox_start first",
			ErrConflict, todoID, t.State)
	}

	return t.Work.Attempt, nil
}

// RoutineScriptReads says whether the script of a Routine's run reads only (issue #1023): by the access the run
// started with, its own snapshot, so that a change of the Routine meanwhile, the owner's or a Bot's, changes nothing
// of the run that goes on, and nothing of its token (reviews of #1037). A run that is not there is an error.
func (s *Service) RoutineScriptReads(ctx context.Context, runID string) (bool, error) {
	run, err := s.routines.GetRoutineRun(ctx, runID)
	if err != nil {
		return false, err
	}

	return run.Access == routinestore.AccessRead, nil
}
