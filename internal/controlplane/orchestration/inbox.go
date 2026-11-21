package orchestration

import (
	"context"
	"fmt"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/inboxstore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/routinestore"
)

func (s *Service) InboxNeedsYou(ctx context.Context) (int, error) {
	return s.inbox.NeedsYou(ctx, s.clock.Now())
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

// RoutineScriptReads says whether the script of a Routine reads only (issue #1023): by the access its live run
// started with, so that a change of the Routine meanwhile, the owner's or a Bot's, changes nothing of the run that
// goes on (review of #1037); by the Routine's own when no run is live.
func (s *Service) RoutineScriptReads(ctx context.Context, routineID string) (bool, error) {
	q := store.New(s.db)

	run, err := routinestore.LiveRunTx(ctx, q, routineID)
	if err != nil {
		return false, err
	}

	if run != nil {
		return run.Access == routinestore.AccessRead, nil
	}

	r, err := routinestore.GetRoutineTx(ctx, q, routineID)
	if err != nil {
		return false, err
	}

	return r.Access == routinestore.AccessRead, nil
}
