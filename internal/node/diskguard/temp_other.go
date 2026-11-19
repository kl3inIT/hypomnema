//go:build !linux

package diskguard

import (
	"context"
	"log/slog"
	"time"
)

func sweepSystemTemp(_ context.Context, _, _ string, _ time.Time, _ *slog.Logger) error {
	return nil
}
