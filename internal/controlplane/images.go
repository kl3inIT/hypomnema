package controlplane

import (
	"context"
	"log/slog"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
)

// Images for Bots and the Images page (issue #431): the model gateway makes
// them (modelgateway/images.go) and serves them on /v1/images/*, recording
// each on the key that asked; the client API gives a Bot the same through
// `hypomnema image`, recorded on the key of its chat's running pi as a web
// search is (web.go botWeb). Another token's request, which belongs to no
// chat, is not recorded.
type botImages struct {
	server  *modelgateway.Server
	gateway *modelgateway.Service
	chats   chatKeyHolder
	logger  *slog.Logger
}

func (b botImages) Make(
	ctx context.Context, chatID string, req modelgateway.ImageRequest,
) (modelgateway.ImageResult, error) {
	start := time.Now()
	res, err := b.server.MakeImages(ctx, req)

	if keyID, ok := chatKeyID(ctx, b.gateway, b.chats, chatID); ok {
		ctx := context.WithoutCancel(ctx)

		call := modelgateway.ImageCall{KeyID: keyID, Request: req, Result: res, Err: err, Duration: time.Since(start)}
		if rerr := b.gateway.RecordImageCall(ctx, call); rerr != nil {
			b.logger.ErrorContext(ctx, "recording an image call",
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
