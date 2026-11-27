package agentapi

import (
	"context"
	"errors"
	"fmt"

	"github.com/kl3inIT/hypomnema/internal/shared/workspacefs"
)

// BotFiles answers a question about a Bot's folder, as the client API's Files panel asks it (issue #255).
type BotFiles interface {
	Ask(ctx context.Context, bot string, req workspacefs.Request) (workspacefs.Result, error)
}

// WithBotFiles lets the Inbox's comments say which files a Bot made (issue #1206): a comment's files read in its
// folder are its work, which it may close an item on as its own; without it, none is.
func (s *Server) WithBotFiles(f BotFiles) *Server {
	s.botFiles = f

	return s
}

// botMade says whether every file a Bot's comment names is in its folder now, read with a raw read of at most one
// byte: a file larger than that is there (CodeTooLarge), one missing is not (review round 3 of PR #1243). A folder
// that cannot be read says nothing is.
func (s *Server) botMade(ctx context.Context, bot string, files []string) (bool, error) {
	if s.botFiles == nil || len(files) == 0 {
		return false, nil
	}

	for _, f := range files {
		res, err := s.botFiles.Ask(ctx, bot, workspacefs.Request{Op: workspacefs.OpRead, Path: f, Raw: true, Limit: 1})
		if err != nil {
			return false, fmt.Errorf("reading %s in bot %s's folder: %w", f, bot, err)
		}

		var e *workspacefs.Error

		switch {
		case res.Error == nil && res.File != nil:
		case res.Error != nil && errors.As(error(res.Error), &e) && e.Code == workspacefs.CodeTooLarge:
		default:
			return false, nil
		}
	}

	return true, nil
}
