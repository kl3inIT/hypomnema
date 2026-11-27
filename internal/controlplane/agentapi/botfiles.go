package agentapi

import (
	"context"

	"github.com/kl3inIT/hypomnema/internal/shared/workspacefs"
)

// BotFiles answers a question about a Bot's folder, as the client API's Files panel asks it (issue #255).
type BotFiles interface {
	Ask(ctx context.Context, bot string, req workspacefs.Request) (workspacefs.Result, error)
}

// WithBotFiles lets the Inbox's comments say which files a Bot made (issue #1206). Not built yet.
func (s *Server) WithBotFiles(_ BotFiles) *Server { return s }
