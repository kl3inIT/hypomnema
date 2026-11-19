package clientapi

import (
	"cmp"
	"context"
	"fmt"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// The Usage page (issue #199, step 4): what the gateway served in a period,
// in total, a bar at a time and by model, key and account, as 9router, T3 Code
// and Omnara show it; and the requests themselves, a page at a time, as
// LiteLLM's and 9router's logs. An account is named by its label, which the
// control plane keeps, not the gateway.

// GatewayUsage is what the Usage page reads of the gateway.
type GatewayUsage interface {
	Usage(ctx context.Context, period string, offset time.Duration) (modelgateway.UsageReport, error)
	UsageRequests(
		ctx context.Context, period string, failedOnly bool, limit, offset int,
	) ([]modelgateway.Usage, bool, error)
}

// WithGatewayUsage lets the owner read the gateway's usage.
func (s *Server) WithGatewayUsage(u GatewayUsage) *Server {
	s.gatewayUsage = u

	return s
}

// GetGatewayUsage answers a period's usage.
func (s *Server) GetGatewayUsage(ctx context.Context, req openapi.GetGatewayUsageRequestObject,
) (openapi.GetGatewayUsageResponseObject, error) {
	if s.gatewayUsage == nil {
		return nil, fmt.Errorf("%w: this control plane runs no model gateway", orchestration.ErrUnavailable)
	}

	offset := time.Duration(val(req.Params.UtcOffsetMinutes)) * time.Minute

	report, err := s.gatewayUsage.Usage(ctx, string(req.Params.Period), offset)
	if err != nil {
		return nil, gatewayKeyError(err)
	}

	accounts := s.accountNames(ctx)

	out := openapi.GetGatewayUsage200JSONResponse{
		Since: report.Since, Until: report.Until, BucketSeconds: int(report.Bucket.Seconds()),
		Totals:  toUsageCounts(report.Totals),
		Buckets: make([]openapi.GatewayUsageBucket, 0, len(report.Buckets)),
		ByModel: toUsageGroups(report.ByModel, nil), ByKey: s.nameKeyGroups(ctx, toUsageGroups(report.ByKey, nil)),
		ByAccount:  toUsageGroups(report.ByAccount, accounts),
		ByProvider: toPerformance(report.ByProvider, nil), SpeedByAccount: toPerformance(report.SpeedByAccount, accounts),
		Ledger: report.Ledger,
	}

	for _, b := range report.Buckets {
		out.Buckets = append(out.Buckets, openapi.GatewayUsageBucket{Start: b.Start, Counts: toUsageCounts(b.UsageCounts)})
	}

	return out, nil
}

// ListGatewayRequests answers a page of a period's requests.
func (s *Server) ListGatewayRequests(ctx context.Context, req openapi.ListGatewayRequestsRequestObject,
) (openapi.ListGatewayRequestsResponseObject, error) {
	if s.gatewayUsage == nil {
		return nil, fmt.Errorf("%w: this control plane runs no model gateway", orchestration.ErrUnavailable)
	}

	rows, more, err := s.gatewayUsage.UsageRequests(ctx, string(req.Params.Period), val(req.Params.Failed),
		val(req.Params.Limit), val(req.Params.Offset))
	if err != nil {
		return nil, gatewayKeyError(err)
	}

	accounts := s.accountNames(ctx)

	out := openapi.ListGatewayRequests200JSONResponse{Data: make([]openapi.GatewayRequest, 0, len(rows)), HasMore: more}
	for _, u := range rows {
		item := openapi.GatewayRequest{
			Id: u.ID, CreatedAt: u.CreatedAt, KeyId: u.KeyID, KeyName: u.KeyName, Model: u.Model,
			Provider: u.Provider, UpstreamModel: u.UpstreamModel, Stream: u.Stream, Attempts: u.Attempts,
			Outcome: openapi.GatewayRequestOutcome(u.Outcome), Status: u.Status,
			PromptTokens: u.Tokens.Prompt, CachedTokens: u.Tokens.Cached, CompletionTokens: u.Tokens.Completion,
			ReasoningTokens: u.Tokens.Reasoning, Estimated: u.Tokens.Estimated, DurationMs: u.Duration.Milliseconds(),
		}

		if u.TTFT != nil {
			item.TtftMs = ptr(u.TTFT.Milliseconds())
		}

		if tps, ok := u.TokensPerSecond(); ok {
			item.TokensPerSecond = ptr(float32(tps))
		}

		if u.AccountID != "" {
			item.AccountId = &u.AccountID
			if a, ok := accounts[u.AccountID]; ok {
				item.AccountLabel = &a.label
			}
		}

		if u.Error != "" {
			item.Error = &u.Error
		}

		item.CacheWriteTokens, item.CacheWrite1hTokens = u.Tokens.CacheWrite, u.Tokens.CacheWrite1h
		item.CostMicros = u.CostMicros
		if u.Audio > 0 {
			item.AudioSeconds = ptr(float32(u.Audio.Seconds()))
		}

		if u.Characters > 0 {
			item.Characters = ptr(u.Characters)
		}

		if u.WebSearches > 0 {
			item.WebSearches = ptr(u.WebSearches)
		}

		if u.Images > 0 {
			item.Images = ptr(u.Images)
		}

		if u.PricedModel != "" {
			item.PricedModel = &u.PricedModel
		}

		out.Data = append(out.Data, item)
	}

	return out, nil
}

type accountName struct{ label, runtime string }

// accountNames names the accounts by id; none when they cannot be read, as a
// page of usage is still worth showing without them.
func (s *Server) accountNames(ctx context.Context) map[string]accountName {
	out := map[string]accountName{}

	accounts, err := s.listAccounts(ctx)
	if err != nil {
		return out
	}

	for _, a := range accounts {
		out[a.ID] = accountName{label: a.Label, runtime: a.Runtime}
	}

	return out
}

// The groups of the usage by key that are not one of the owner's keys (issue #626): every chat's keys of a Bot,
// every worker's, and the chats' keys whose Bot is not known; and in a period read from the daily ledger, which
// names no key, the owner's keys together (issue #856).
const (
	usageWorkers = "workers"
	usageChats   = "chats"
)

// nameKeyGroups names the usage by key's groups that are not the owner's keys: a Bot's by the Bot's name, the
// workers' and the unknown chats' in words. A Bot deleted since keeps its id.
func (s *Server) nameKeyGroups(ctx context.Context, groups []openapi.GatewayUsageGroup) []openapi.GatewayUsageGroup {
	bots := map[string]string{}

	if list, err := s.svc.ListBots(ctx); err == nil {
		for _, b := range list {
			bots[b.ID] = b.Name
		}
	}

	for i, g := range groups {
		switch {
		case g.Id == usageWorkers:
			groups[i].Name = "Workers"
		case g.Id == usageChats:
			groups[i].Name = "Chats of a Bot not known"
		case g.Id == modelgateway.CallerOwner && g.Name == "":
			groups[i].Name = "Your keys"
		case g.Name == "":
			groups[i].Name = "Bot " + cmp.Or(bots[g.Id], g.Id)
		}
	}

	return groups
}

func toUsageCounts(c modelgateway.UsageCounts) openapi.GatewayUsageCounts {
	return openapi.GatewayUsageCounts{
		Requests: c.Requests, Failed: c.Failed, PromptTokens: c.Prompt, CachedTokens: c.Cached,
		EstimatedRequests: c.EstimatedRequests, EstimatedPromptTokens: c.EstimatedPrompt,
		MeasuredPromptTokens: c.MeasuredPrompt,
		CompletionTokens:     c.Completion, ReasoningTokens: c.Reasoning, CostMicros: c.CostMicros, Unpriced: c.Unpriced,
		WebSearches: c.WebSearches, AudioSeconds: float32(c.Audio.Seconds()), Characters: c.Characters,
		Images: c.Images,
	}
}

func toUsageGroups(groups []modelgateway.UsageGroup, accounts map[string]accountName) []openapi.GatewayUsageGroup {
	out := make([]openapi.GatewayUsageGroup, 0, len(groups))

	for _, g := range groups {
		item := openapi.GatewayUsageGroup{Id: g.ID, Name: g.Name, Counts: toUsageCounts(g.UsageCounts)}

		if a, ok := accounts[g.ID]; ok {
			runtime := a.runtime
			item.Name, item.Runtime = a.label, &runtime
		}

		out = append(out, item)
	}

	return out
}

// toPerformance is the speed comparison, an account named by its label.
func toPerformance(
	groups []modelgateway.PerformanceGroup, accounts map[string]accountName,
) []openapi.GatewayPerformance {
	out := make([]openapi.GatewayPerformance, 0, len(groups))

	ms := func(d *time.Duration) *int64 {
		if d == nil {
			return nil
		}

		return ptr(d.Milliseconds())
	}

	for _, g := range groups {
		item := openapi.GatewayPerformance{
			Provider: g.Provider, Model: g.Model, Requests: g.Requests, Ok: g.OK, Failed: g.Failed,
			ClientGone: g.ClientGone, CostMicros: g.CostMicros, Exact: g.Exact,
			TtftP50Ms: ms(g.TTFTP50), TtftP95Ms: ms(g.TTFTP95), LatencyP50Ms: ms(g.LatencyP50),
			LatencyP95Ms: ms(g.LatencyP95), TtftAvgMs: ms(g.TTFTAvg), LatencyAvgMs: ms(g.LatencyAvg),
		}

		if rate, ok := g.SuccessRate(); ok {
			item.SuccessRate = ptr(float32(rate))
		}

		if g.TokensPerSecond != nil {
			item.TokensPerSecond = ptr(float32(*g.TokensPerSecond))
		}

		if g.AccountID != "" {
			item.AccountId = ptr(g.AccountID)
			if a, ok := accounts[g.AccountID]; ok {
				item.AccountLabel, item.Runtime = ptr(a.label), ptr(a.runtime)
			}
		}

		out = append(out, item)
	}

	return out
}
