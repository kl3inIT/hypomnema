package controlplane

import (
	"context"
	"log/slog"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
)

// Images for Bots, workers and the Images page (issue #431): the model
// gateway makes them (modelgateway/images.go) and serves them on
// /v1/images/*, recording each on the key that asked; the client API gives a
// Bot the same through `hypomnema image`, recorded on the key of its chat's
// running pi as a web search is (web.go botWeb). Another token's request,
// which belongs to no chat, is not recorded. The agent API gives a worker
// generate_image (issue #492), recorded on its Dispatch's key as its web
// tools are (web.go workerWeb).
type botImages struct {
	server  *modelgateway.Server
	gateway *modelgateway.Service
	chats   chatKeyHolder
	logger  *slog.Logger
}

func (b botImages) Make(
	ctx context.Context, chatID string, req modelgateway.ImageRequest,
) (modelgateway.ImageResult, error) {
	return makeImages(ctx, b.server, b.gateway, b.logger, req, func(ctx context.Context) (string, bool) {
		return chatKeyID(ctx, b.gateway, b.chats, chatID)
	})
}

// workerImages is the images as the agent API gives them to a Dispatch's
// worker: recorded on the Dispatch's gateway key, when it has one.
type workerImages struct {
	server  *modelgateway.Server
	gateway *modelgateway.Service
	logger  *slog.Logger
}

func (w workerImages) Make(
	ctx context.Context, dispatchID string, req modelgateway.ImageRequest,
) (modelgateway.ImageResult, error) {
	return makeImages(ctx, w.server, w.gateway, w.logger, req, func(ctx context.Context) (string, bool) {
		return dispatchKeyID(ctx, w.gateway, w.logger, dispatchID)
	})
}

// makeImages makes the images and records the call on the key keyOf names,
// when it names one.
func makeImages(ctx context.Context, server *modelgateway.Server, gateway *modelgateway.Service, logger *slog.Logger,
	req modelgateway.ImageRequest, keyOf func(context.Context) (string, bool),
) (modelgateway.ImageResult, error) {
	start := time.Now()
	res, err := server.MakeImages(ctx, req)

	ctx = context.WithoutCancel(ctx)

	if keyID, ok := keyOf(ctx); ok {
		call := modelgateway.ImageCall{KeyID: keyID, Request: req, Result: res, Err: err, Duration: time.Since(start)}
		if rerr := gateway.RecordImageCall(ctx, call); rerr != nil {
			logger.ErrorContext(ctx, "recording an image call",
				slog.String("event", "controlplane.image_usage_failed"), slog.String("error", rerr.Error()))
		}
	}

	return res, err
}

// imageSettings is what the Images page shows and changes: the gateway's
// switches, order and models, its counts, and the rests the gateway keeps
// while it runs.
type imageSettings struct {
	*modelgateway.Service

	server *modelgateway.Server
}

func (i imageSettings) ImageRests(ctx context.Context) (map[string]map[string]time.Time, error) {
	return i.server.ImageRests(ctx)
}
