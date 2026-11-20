package orchestration

import (
	"context"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
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
