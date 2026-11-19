package orchestration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
)

func gatewayRoute(runtime, wire string) string {
	switch {
	case runtime == "codex", runtime == "omp" && wire == "openai-responses":
		return "/v1/responses"
	case runtime == "claude", runtime == "omp" && wire == "anthropic-messages":
		return "/v1/messages"
	default:
		return "/v1/chat/completions"
	}
}

func gatewayReset(
	ctx context.Context, q *store.Queries, now int64, d store.Dispatch, conv store.Conversation,
) (*time.Time, error) {
	wire, err := modelAPI(ctx, q, conv.AccountRef, d.Model.String)
	if err != nil {
		return nil, err
	}
	fact, err := q.GetGatewayModelCooling(ctx, store.GetGatewayModelCoolingParams{
		Route: gatewayRoute(conv.Runtime, wire), Model: d.Model.String,
	})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("reading cooldown of gateway model %s: %w", d.Model.String, err)
	}

	until := now + RateLimitCooldown.Milliseconds()
	if err == nil && (fact.ResetsAt > now || fact.ObservedAt >= max(d.CreatedAt, d.StartedAt.Int64)) {
		until = fact.ResetsAt
	}
	reset := time.UnixMilli(until)

	return &reset, nil
}

func (p *placement) gatewayCoolingUntil(r resolved) int64 {
	if !r.gateway {
		return 0
	}
	until := max(r.gatewayUntil, p.gatewayCooling[[2]string{r.gatewayRoute, r.model}])
	if until <= p.now {
		return 0
	}
	return until
}
