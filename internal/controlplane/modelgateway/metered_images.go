package modelgateway

import (
	"context"
	"log/slog"
	"time"
)

// Images for Bots and workers (issue #431): the gateway makes them (images.go) and serves them on /v1/images/*,
// recording each on the key that asked; the client API gives a Bot the same through `hypomnema image`, recorded on
// the key of its chat's running pi as a web search is (BotWeb). Another token's request, which belongs to no chat,
// is not recorded. The agent API gives a worker generate_image (issue #492), recorded on its Dispatch's key as its
// web tools are (WorkerWeb), and a Bot's pi, recorded as its `hypomnema image` was.

// BotImages is the images as the client API gives them to a token: recorded on the key of the chat's running pi.
type BotImages struct {
	Server  *Server
	Gateway *Service
	Chats   ChatKeys
	Logger  *slog.Logger
}

func (b BotImages) Make(ctx context.Context, chatID string, req ImageRequest) (ImageResult, error) {
	return makeImages(ctx, b.Server, b.Gateway, b.Logger, req, func(ctx context.Context) (string, bool) {
		return chatKeyID(ctx, b.Gateway, b.Chats, chatID)
	})
}

// WorkerImages is the images as the agent API gives them to a Dispatch's worker: recorded on the Dispatch's gateway
// key, when it has one.
type WorkerImages struct {
	Server  *Server
	Gateway *Service
	Logger  *slog.Logger
}

func (w WorkerImages) Make(ctx context.Context, dispatchID string, req ImageRequest) (ImageResult, error) {
	return makeImages(ctx, w.Server, w.Gateway, w.Logger, req, func(ctx context.Context) (string, bool) {
		return dispatchKeyID(ctx, w.Gateway, w.Logger, dispatchID)
	})
}

// makeImages makes the images and records the call on the key keyOf names, when it names one.
func makeImages(ctx context.Context, server *Server, gateway *Service, logger *slog.Logger,
	req ImageRequest, keyOf func(context.Context) (string, bool),
) (ImageResult, error) {
	start := time.Now()
	res, err := server.MakeImages(ctx, req)

	ctx = context.WithoutCancel(ctx)

	if keyID, ok := keyOf(ctx); ok {
		call := ImageCall{KeyID: keyID, Request: req, Result: res, Err: err, Duration: time.Since(start)}
		if rerr := gateway.RecordImageCall(ctx, call); rerr != nil {
			logger.ErrorContext(ctx, "recording an image call",
				slog.String("event", "controlplane.image_usage_failed"), slog.String("error", rerr.Error()))
		}
	}

	return res, err
}
