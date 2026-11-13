package clientapi

import (
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
		ByModel: toUsageGroups(report.ByModel, nil), ByKey: toUsageGroups(report.ByKey, nil),
		ByAccount: toUsageGroups(report.ByAccount, accounts),
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

		if u.AccountID != "" {
			item.AccountId = &u.AccountID
			if a, ok := accounts[u.AccountID]; ok {
				item.AccountLabel = &a.label
			}
		}

		if u.Error != "" {
			item.Error = &u.Error
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

func toUsageCounts(c modelgateway.UsageCounts) openapi.GatewayUsageCounts {
	return openapi.GatewayUsageCounts{
		Requests: c.Requests, Failed: c.Failed, PromptTokens: c.Prompt, CachedTokens: c.Cached,
		CompletionTokens: c.Completion, ReasoningTokens: c.Reasoning,
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
