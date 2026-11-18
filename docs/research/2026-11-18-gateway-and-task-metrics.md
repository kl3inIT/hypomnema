# Gateway and Task metrics: speed, reliability, cost per Task, repeated failures, a daily ledger

Issue [#856](https://github.com/kl3inIT/hypomnema/issues/856). The design was approved by the owner on 2026-11-18,
with the comparison below; this note keeps the comparison and what the implementation checked.

## What was on main (2026-11-18)

- `gateway_usage` stored per request the tokens, `cost_micros`, `duration_ms` (the whole call), `attempts`,
  `outcome`, `status`, `error`, `account_id`, `provider`, `model`, `upstream_model`; no time to first token, no
  speed, no success rate, no percentiles, no grouping by provider (`modelgateway/queries/gateway.sql`).
- `gateway_usage` has no Task, but each Dispatch has keys of its own: `gateway_usage.key_id → gateway_keys.dispatch_id
  → dispatches.task_id → tasks.run_id` (migration 00031). Nothing read it.
- The request rows are purged after 31 days (`modelgateway/purge.go`, issue #628).
- Guards inside one Dispatch exist (loop guard, stalled, overrun); nothing noticed one Task failing again for the
  same reason.

## The comparison

| Question | References | Decision |
|---|---|---|
| Time to first token | LiteLLM `streaming_handler.py` sets `completion_start_time` at the first chunk and `lowest_latency.py` routes on it; Helicone's `ProxyRequestHandler.ts` times the first chunk; new-api `pkg/perf_metrics/metrics.go`; Bifrost `core/schemas/trace.go`; ai-gateway `internal/metrics/metrics_impl.go` | `gateway_usage.ttft_ms`, from receiving the request to the first chunk written to the client, on every streaming route; NULL for an answer that is not a stream, never set to the whole call |
| Output tokens a second | OmniRoute `open-sse/utils/generationThroughput.ts` (read at `bc4b0c5`): "tok/s MUST exclude TTFT", omitted when TTFT is unknown or generation ≤ 0; new-api and ai-gateway the same | Computed when read, `completion / ((duration − ttft)/1000)`, never stored; left out for an estimated row |
| Comparison by provider | new-api's `perf_metrics` table (`model/perf_metric.go`); 9router-go's usage by provider and model (`internal/db/usage.go`); OmniRoute's speed ranking (p95, tokens a second) | A Performance table by provider × served model and by account: requests, success rate (`client_gone` left out), TTFT and latency p50/p95 (nearest rank, in Go from the rows), median tokens a second, cost; `hypomnema gateway usage --by provider` prints it |
| Cost per Task and Run | Omnigent per-session cost (`omnigent/cli.py`); Orca's per-run attribution (`claude-usage-automation-attribution.ts`); Companion's per-run `costUsd` (`apps/server/src/executor.ts`); Coder's aibridge cost per interception | Summed per Dispatch through its keys, rolled up per Task and Run, on `GET /tasks/{id}` and `GET /runs/{id}`; a Dispatch outside the gateway adds its runtime's reported tokens, with no cost |
| Cost after the purge | 9router-go's prune carries the spend of the rows it removes into a kept ledger (`internal/db/usage_retention.go`); LiteLLM keeps daily spend tables apart from spend logs (`spend_log_cleanup.py`) | `dispatch_usage` keeps a Dispatch's sums when it settles; a Dispatch reads the request rows or the kept sums, whichever counts more requests |
| Repeated failure | OpenClaw Workboard's `repeated_failures` at `failureCount >= 2` (`extensions/workboard/src/store-card-helpers.ts`); Hermes `BLOCK_RECURRENCE_LIMIT = 2` → triage (`hermes_cli/kanban_db.py`) | Escalation `repeated_failure` when a Task's Dispatch fails with the same class (and unknown cause) as its previous failed one; nothing else changes |
| Long periods | LiteLLM `LiteLLM_DailyUserSpend` (read at `c030ffb`, `litellm/proxy/schema.prisma:961-985`): date, api_key, model, provider, token columns, `spend`, `api_requests`, `successful_requests`, `failed_requests`, `total_response_time_ms` — sums only | `gateway_usage_daily`, a row a UTC day × caller × model × provider × upstream model × account, upserted with each request in its transaction, never purged, naming no key; 90 days and 12 months read it, with averages and no percentiles |
| Left out | No Prometheus or OpenTelemetry export, no routing by speed (`2026-11-13-gateway-features.md` §3; only LiteLLM and OmniRoute route on latency), no clustering of failures across Tasks, no automatic change of prompt, profile or route | — |

## What the implementation found

- The cause a Dispatch was unknown for (`unknown_cause`) is cleared when it settles (`SettleDispatch` in
  `orchestration/queries/dispatches.sql`, and the table's CHECK ties it to the unknown statuses), so a settled
  failure's cause is read from its last `dispatch.status` event, whose payload holds `cause`.
- The Messages and Responses routes pass events line by line; their first chunk is the first event written, at the
  empty line that ends it. A stream translated from chat (`relayFromChat`) and the chat route mark it at the first
  write that reaches the client, so every adapter behind them is covered.
- LiteLLM's daily table holds `total_response_time_ms`, a sum: it has averages, not percentiles, as the ledger here.
