package controlplane

import (
	"context"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/agentapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
)

// Images for Bots, workers and the Images page (issue #431): the model
// gateway makes and meters them (modelgateway/images.go, metered_images.go);
// here, the agent API's and the Images page's view of them.

// agentImages is the images as the agent API gives them: a worker's
// recorded as modelgateway.WorkerImages records them, a Bot's pi's (issue
// #492) as modelgateway.BotImages records its `hypomnema image`.
type agentImages struct {
	worker modelgateway.WorkerImages
	bot    modelgateway.BotImages
}

func (a agentImages) Make(
	ctx context.Context, who agentapi.Who, req modelgateway.ImageRequest,
) (modelgateway.ImageResult, error) {
	if who.DispatchID != "" {
		return a.worker.Make(ctx, who.DispatchID, req)
	}

	return a.bot.Make(ctx, who.ChatID, req)
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
