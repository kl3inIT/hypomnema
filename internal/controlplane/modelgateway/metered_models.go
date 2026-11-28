package modelgateway

import (
	"context"
	"errors"
	"log/slog"
)

// The gateway's chat models as a tool asks them for its caller (issue #1306: social listening's review of its
// shortlist): the call is Complete's, recorded on the caller's gateway key as a worker's or a Bot's image is
// (WorkerImages, BotImages), whatever models the key lets its own client name, since the tool chose the model, not
// the caller.

// ErrNoKey is WorkerModels' and BotModels' error when the caller has no gateway key to record the call on (a worker
// whose model the gateway does not serve, a chat whose pi runs no more): the model is not asked.
var ErrNoKey = errors.New("the caller has no gateway key to record a model call on")

// WorkerModels asks the gateway's chat models for a Dispatch's worker, recorded on the Dispatch's key.
type WorkerModels struct {
	Server  *Server
	Gateway *Service
	Logger  *slog.Logger
}

func (w WorkerModels) Complete(ctx context.Context, dispatchID string, body []byte) ([]byte, error) {
	keyID, ok := dispatchKeyID(ctx, w.Gateway, w.Logger, dispatchID)
	if !ok {
		return nil, ErrNoKey
	}

	return w.Server.Complete(ctx, keyID, body)
}

// BotModels asks the gateway's chat models for a Bot's pi, recorded on the key of its chat's running pi.
type BotModels struct {
	Server  *Server
	Gateway *Service
	Chats   ChatKeys
}

func (b BotModels) Complete(ctx context.Context, chatID string, body []byte) ([]byte, error) {
	keyID, ok := chatKeyID(ctx, b.Gateway, b.Chats, chatID)
	if !ok {
		return nil, ErrNoKey
	}

	return b.Server.Complete(ctx, keyID, body)
}
