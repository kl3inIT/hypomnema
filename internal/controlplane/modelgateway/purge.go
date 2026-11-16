package modelgateway

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway/internal/store"
)

// The gateway's own clean-up (issue #628), as Coder's dbpurge deletes expired API keys and old rows on a schedule
// (coderd/database/dbpurge/dbpurge.go) and LiteLLM keeps spend logs for a set time. usageKept is how long a request
// is kept: the longest period the Usage page shows, 30 days from the viewer's midnight, and a day to spare.
const (
	usageKept  = 31 * 24 * time.Hour
	purgeEvery = 24 * time.Hour
	purgeFirst = 10 * time.Minute
)

// Purged is what one clean-up deleted.
type Purged struct {
	Requests, Keys int64
}

// Purge deletes the requests older than usageKept, then each revoked key no request names any more. A live key is
// never deleted, and a revoked one stays while a request it sent can still be shown.
func (s *Service) Purge(ctx context.Context) (Purged, error) {
	var out Purged

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		var err error

		if out.Requests, err = q.DeleteGatewayUsageBefore(ctx, now-usageKept.Milliseconds()); err != nil {
			return fmt.Errorf("deleting old requests: %w", err)
		}

		if out.Keys, err = q.DeleteUnusedRevokedGatewayKeys(ctx); err != nil {
			return fmt.Errorf("deleting revoked keys: %w", err)
		}

		return nil
	})

	return out, err
}

// PurgeLoop cleans up a while after the start and then once a day, until ctx ends.
func (s *Service) PurgeLoop(ctx context.Context, logger *slog.Logger) {
	timer := time.NewTimer(purgeFirst)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}

		purged, err := s.Purge(ctx)

		switch {
		case err != nil:
			logger.WarnContext(ctx, "the gateway's clean-up failed",
				slog.String("event", "gateway.purge_failed"), slog.String("error", err.Error()))
		case purged.Requests > 0 || purged.Keys > 0:
			logger.InfoContext(ctx, "the gateway deleted old requests and revoked keys",
				slog.String("event", "gateway.purged"), slog.Int64("requests", purged.Requests),
				slog.Int64("keys", purged.Keys))
		}

		timer.Reset(purgeEvery)
	}
}
