package controlplane

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/profilestore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/subscription"
)

// waitsSeen records the deadline for the accounts' lists each reading was given.
type waitsSeen struct{ limits, wire []time.Time }

func (w *waitsSeen) ServedLimits(ctx context.Context, _, _ string) (int64, int64, bool) {
	d, _ := subscription.ListDeadline(ctx)
	w.limits = append(w.limits, d)

	return 0, 0, false
}

func (w *waitsSeen) ServedWire(ctx context.Context, _, _ string) (string, bool) {
	d, _ := subscription.ListDeadline(ctx)
	w.wire = append(w.wire, d)

	return "", false
}

// Scenario (review of #832, round 7): a model's Codex target waits for a list being read, and its OpenCode Go target
// comes next. Reading the wire and limits OpenCode Go serves the model on waits no later than the request's one wait,
// set when it entered the gateway, rather than a wait of its own after the Codex target's.
func TestOpenCodeGosListIsReadWithinTheRequestsWait(t *testing.T) {
	t.Parallel()

	ends := time.Date(2026, 11, 19, 9, 0, 30, 0, time.UTC)
	ctx := modelgateway.WithAccountWait(t.Context(), ends)
	seen := &waitsSeen{}

	gatewayServedWire(seen)(ctx, profilestore.ProviderOpenCodeGo, "glm-5.2")
	gatewayServedLimits(seen)(ctx, profilestore.ProviderOpenCodeGo, "glm-5.2")
	gatewayServedLimits(seen)(ctx, profilestore.ProviderAntigravitySubscription, "gemini-4-carbon-high")

	assert.Equal(t, []time.Time{ends}, seen.wire)
	assert.Equal(t, []time.Time{ends, ends}, seen.limits)
}
