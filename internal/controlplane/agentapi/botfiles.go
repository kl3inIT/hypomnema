package agentapi

import (
	"context"
	"errors"
	"fmt"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/inboxstore"
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

// botRead are the files of a Bot's comment that are in its folder now, each at the path the comment keeps
// (inboxstore.FolderPath), read with a raw read of at most one byte: a file larger than that is there (CodeTooLarge),
// one missing is not (review rounds 3 and 4 of PR #1243). A folder that cannot be read gives none, and a path the
// comment refuses is left to its refusal.
func (s *Server) botRead(ctx context.Context, bot string, files []string) ([]string, error) {
	if s.botFiles == nil {
		return nil, nil
	}

	var read []string

	for _, raw := range files {
		f, err := inboxstore.FolderPath(raw)
		if err != nil {
			continue
		}

		res, err := s.botFiles.Ask(ctx, bot, workspacefs.Request{Op: workspacefs.OpRead, Path: f, Raw: true, Limit: 1})
		if err != nil {
			return nil, fmt.Errorf("reading %s in bot %s's folder: %w", f, bot, err)
		}

		var e *workspacefs.Error

		if res.Error == nil && res.File != nil ||
			res.Error != nil && errors.As(error(res.Error), &e) && e.Code == workspacefs.CodeTooLarge {
			read = append(read, f)
		}
	}

	return read, nil
}
