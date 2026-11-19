package modelgateway

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway/internal/store"
)

// The periods longer than the requests are kept (issue #856) are read from the daily ledger, gateway_usage_daily,
// as LiteLLM's usage pages read its daily spend tables (LiteLLM_DailyUserSpend): its sums by UTC day, caller,
// model, provider and account. It names no key, so the usage by key counts the owner's keys together, and it keeps
// sums only, so the speed has averages and no percentiles.

// CallerOwner is the ledger's caller for the owner's own keys, which it does not tell apart.
const CallerOwner = "owner"

// ledgerUsage reports a ledger period ending now, in UTC days or calendar months.
func (s *Service) ledgerUsage(ctx context.Context, p UsagePeriod) (UsageReport, error) {
	now := s.clock.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)

	var starts []time.Time

	if p.Monthly {
		month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		for i := 11; i >= 0; i-- {
			starts = append(starts, month.AddDate(0, -i, 0))
		}
	} else {
		days := int(p.Span / p.Bucket)
		for i := days - 1; i >= 0; i-- {
			starts = append(starts, today.AddDate(0, 0, -i))
		}
	}

	origin := starts[0]

	rows, err := store.New(s.db).ListGatewayUsageDaily(ctx, origin.UnixMilli())
	if err != nil {
		return UsageReport{}, fmt.Errorf("reading the daily ledger: %w", err)
	}

	report := UsageReport{Since: origin, Until: s.clock.Now(), Bucket: p.Bucket, Ledger: true}
	report.Buckets = make([]UsageBucket, len(starts))

	for i, st := range starts {
		report.Buckets[i].Start = st
	}

	byModel, byKey, byAccount := newUsageGroups(), newUsageGroups(), newUsageGroups()
	byProvider, speedByAccount := newPerformanceGroups(), newPerformanceGroups()

	for _, r := range rows {
		c := ledgerCounts(r)
		report.Totals.add(c)

		day := time.UnixMilli(r.Day).UTC()
		if i := bucketOf(starts, day); i >= 0 {
			report.Buckets[i].add(c)
		}

		byModel.get(r.Model, r.Model).add(c)
		byKey.get(r.Caller, "").add(c)

		if r.AccountID != "" {
			byAccount.get(r.AccountID, "").add(c)
		}

		if !textModel(r.Model) {
			continue
		}

		sums := func(g *PerformanceGroup) {
			g.addSums(r.Requests, r.Ok, r.Failed, r.ClientGone, r.CostMicros, r.OkDurationMs, r.TtftMs,
				r.TtftCount, r.GenerationMs, r.GenerationTokens)
		}

		sums(byProvider.get(r.Provider+"\x00"+r.UpstreamModel, func() PerformanceGroup {
			return PerformanceGroup{Provider: r.Provider, Model: r.UpstreamModel}
		}))

		if r.AccountID != "" {
			sums(speedByAccount.get(r.AccountID, func() PerformanceGroup {
				return PerformanceGroup{Provider: r.Provider, AccountID: r.AccountID}
			}))
		}
	}

	report.ByModel, report.ByKey, report.ByAccount = byModel.list(), byKey.list(), byAccount.list()
	report.ByProvider, report.SpeedByAccount = byProvider.list(false), speedByAccount.list(false)

	return report, nil
}

// bucketOf is the index of the last start at or before t, -1 before the first.
func bucketOf(starts []time.Time, t time.Time) int {
	i, found := slices.BinarySearchFunc(starts, t, func(a, b time.Time) int { return a.Compare(b) })
	if found {
		return i
	}

	return i - 1
}

func ledgerCounts(r store.GatewayUsageDaily) UsageCounts {
	return UsageCounts{
		Requests: r.Requests, Failed: r.Failed, Prompt: r.PromptTokens, Cached: r.MeasuredCachedTokens,
		EstimatedRequests: r.EstimatedRequests, EstimatedPrompt: r.EstimatedPromptTokens,
		MeasuredPrompt: r.MeasuredPromptTokens,
		Completion:     r.CompletionTokens, Reasoning: r.ReasoningTokens, CostMicros: r.CostMicros, Unpriced: r.Unpriced,
		WebSearches: r.WebSearches, Audio: time.Duration(r.AudioMs) * time.Millisecond, Characters: r.Characters,
		Images: r.Images,
	}
}

func (c *UsageCounts) add(o UsageCounts) {
	c.Requests += o.Requests
	c.Failed += o.Failed
	c.Prompt += o.Prompt
	c.Cached += o.Cached
	c.EstimatedRequests += o.EstimatedRequests
	c.EstimatedPrompt += o.EstimatedPrompt
	c.MeasuredPrompt += o.MeasuredPrompt
	c.Completion += o.Completion
	c.Reasoning += o.Reasoning
	c.CostMicros += o.CostMicros
	c.Unpriced += o.Unpriced
	c.WebSearches += o.WebSearches
	c.Audio += o.Audio
	c.Characters += o.Characters
	c.Images += o.Images
}

// usageGroups gathers the ledger's rows into groups by id.
type usageGroups struct{ groups map[string]*UsageGroup }

func newUsageGroups() *usageGroups { return &usageGroups{groups: map[string]*UsageGroup{}} }

func (g *usageGroups) get(id, name string) *UsageCounts {
	if u, ok := g.groups[id]; ok {
		return &u.UsageCounts
	}

	u := &UsageGroup{ID: id, Name: name}
	g.groups[id] = u

	return &u.UsageCounts
}

// list is the groups, the most tokens first, as the request rows' queries order them.
func (g *usageGroups) list() []UsageGroup {
	out := make([]UsageGroup, 0, len(g.groups))
	for _, u := range g.groups {
		out = append(out, *u)
	}

	slices.SortFunc(out, func(a, b UsageGroup) int {
		return cmp.Or(cmp.Compare(b.Prompt+b.Completion, a.Prompt+a.Completion), cmp.Compare(a.Name, b.Name),
			cmp.Compare(a.ID, b.ID))
	})

	return out
}
