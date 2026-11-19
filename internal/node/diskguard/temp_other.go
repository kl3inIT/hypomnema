//go:build !linux

package diskguard

import (
	"context"
	"time"
)

func sweepSystemTemp(_ context.Context, _, _ string, _ time.Time) error {
	return nil
}
