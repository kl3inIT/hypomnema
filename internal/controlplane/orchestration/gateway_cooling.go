package orchestration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
)

func gatewayRoute(ctx context.Context, q *store.Queries, runtime, model string) (string, error) {
	switch runtime {
	case "codex":
		return "/v1/responses", nil
	case "claude":
		return "/v1/messages", nil
	case "omp":
		wire, err := modelAPI(ctx, q, GatewayAccountRef, model)
		if err != nil {
			return "", err
		}
		switch wire {
		case "openai-responses":
			return "/v1/responses", nil
		case "anthropic-messages":
			return "/v1/messages", nil
		}
	}
	return "/v1/chat/completions", nil
}

func gatewayReset(
	ctx context.Context, q *store.Queries, now int64, d store.Dispatch, conv store.Conversation,
) (resetAt *time.Time, known bool, err error) {
	route, err := gatewayRoute(ctx, q, conv.Runtime, d.Model.String)
	if err != nil {
		return nil, false, err
	}
	fact, err := q.GetGatewayModelCooling(ctx, store.GetGatewayModelCoolingParams{
		Route: route, Model: d.Model.String,
	})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, false, fmt.Errorf("reading cooldown of gateway model %s: %w", d.Model.String, err)
	}

	until := now + RateLimitCooldown.Milliseconds()
	known = err == nil && (fact.ResetsAt > now || fact.ObservedAt >= max(d.CreatedAt, d.StartedAt.Int64))
	if known {
		until = fact.ResetsAt
	}
	reset := time.UnixMilli(until)

	return &reset, known, nil
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

func gatewayCoolingReason(model string, until int64) string {
	return fmt.Sprintf("no account free for %s until %s", model, millis(until).Format(time.RFC3339))
}
