package modelgateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway/internal/store"
)

// What the Usage page reads (issue #199, step 4): the requests of a period,
// counted in total, a day or an hour at a time, by model, by key and by the
// account that served them, as 9router, T3 Code and Omnara show usage; and the
// requests themselves, newest first, as LiteLLM's and 9router's logs.

// served is the account that took a request's last attempt, filled by
// underAccounts on the request's context, as 9router-go's tracker takes the
// connection that served (usagetracker/tracker.go).
type served struct{ id string }

type servedKey struct{}

// withServed gives a request a place to note the account that serves it.
func withServed(r *http.Request) (*http.Request, *served) {
	s := &served{}

	return r.WithContext(context.WithValue(r.Context(), servedKey{}, s)), s
}

// noteServed notes the account of an attempt; "" for a target that is not a
// subscription, so an earlier target's account is not kept.
func noteServed(ctx context.Context, id string) {
	if s, ok := ctx.Value(servedKey{}).(*served); ok {
		s.id = id
	}
}

// maxErrorText is how much of a provider's error message a usage row keeps.
const maxErrorText = 500

// errorText is the message of a provider's error body: OpenAI's and
// Anthropic's error.message, a bare error string or message, else the body.
func errorText(body []byte) string {
	var shape struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
	}

	text := strings.TrimSpace(string(body))

	if json.Unmarshal(body, &shape) == nil {
		var nested struct {
			Message string `json:"message"`
		}

		var plain string

		switch {
		case json.Unmarshal(shape.Error, &nested) == nil && nested.Message != "":
			text = nested.Message
		case json.Unmarshal(shape.Error, &plain) == nil && plain != "":
			text = plain
		case shape.Message != "":
			text = shape.Message
		}
	}

	return clip(text, maxErrorText)
}

func clip(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}

	return string([]rune(text)[:limit]) + "…"
}

// defaultError is what a failed row says when the provider said nothing.
func defaultError(o Outcome) string {
	switch o {
	case OutcomeTransportError:
		return "no provider of the model could be reached"
	case OutcomeTruncated:
		return "the answer ended before its end"
	case OutcomeUpstreamError:
		return "the provider answered with an error"
	default:
		return ""
	}
}

// UsageCounts are the counts of a set of requests.
type UsageCounts struct {
	Requests, Failed                      int64
	Prompt, Cached, Completion, Reasoning int64
	// CostMicros is the cost of the requests the price book prices, in
	// micro-dollars; Unpriced counts those with tokens it has no price for
	// (Coder's has_unpriced_usage).
	CostMicros, Unpriced int64
	// WebSearches, Audio, Characters and Images are the units counted beside
	// the tokens (issue #723): searches, seconds heard, characters spoken and
	// images made.
	WebSearches, Characters, Images int64
	Audio                           time.Duration
}

// UsageGroup is the counts of the requests of one model, key or account.
type UsageGroup struct {
	// ID is the key's or the account's id; for a model, its name.
	ID   string
	Name string
	UsageCounts
}

// UsageBucket is the counts of an hour or a day.
type UsageBucket struct {
	Start time.Time
	UsageCounts
}

// UsageReport is what the gateway served in a period.
type UsageReport struct {
	Since, Until time.Time
	Bucket       time.Duration
	Totals       UsageCounts
	Buckets      []UsageBucket
	ByModel      []UsageGroup
	ByKey        []UsageGroup
	ByAccount    []UsageGroup
	// ByProvider and SpeedByAccount compare the text models' speed and
	// reliability (issue #856): by provider and the model it served, and by
	// subscription account.
	ByProvider     []PerformanceGroup
	SpeedByAccount []PerformanceGroup
	// Ledger says the period was read from the daily ledger, which outlives
	// the request rows: its days are UTC days, and it has no percentiles.
	Ledger bool
}

// UsagePeriod is a period the page offers: how long, the size of a bar, and
// whether it is read from the daily ledger. Monthly bars are calendar months.
type UsagePeriod struct {
	Span, Bucket time.Duration
	Ledger       bool
	Monthly      bool
}

// UsagePeriods are the periods the page offers, with the size of a bar: an
// hour for a day, a day for a week or a month (T3 Code usageShortcuts.ts), and
// for the periods longer than the requests are kept (issue #856), a day for
// 90 days and a month for 12 months, from the daily ledger.
var UsagePeriods = map[string]UsagePeriod{
	"24h": {Span: 24 * time.Hour, Bucket: time.Hour},
	"7d":  {Span: 7 * 24 * time.Hour, Bucket: 24 * time.Hour},
	"30d": {Span: 30 * 24 * time.Hour, Bucket: 24 * time.Hour},
	"90d": {Span: 90 * 24 * time.Hour, Bucket: 24 * time.Hour, Ledger: true},
	"12m": {Span: 365 * 24 * time.Hour, Bucket: 30 * 24 * time.Hour, Ledger: true, Monthly: true},
}

const periodsText = "the period is 24h, 7d, 30d, 90d or 12m"

// Usage reports a period ending now. offset is the viewer's distance from
// UTC, so that a day's bar starts at the viewer's midnight; a period read from
// the ledger has UTC days.
func (s *Service) Usage(ctx context.Context, period string, offset time.Duration) (UsageReport, error) {
	p, ok := UsagePeriods[period]
	if !ok {
		return UsageReport{}, fmt.Errorf("%w: %s", ErrInvalid, periodsText)
	}

	if p.Ledger {
		return s.ledgerUsage(ctx, p)
	}

	now := s.clock.Now()
	count := int(p.Span / p.Bucket)
	// The last bar holds now; the first starts count-1 bars before it.
	last := now.Add(offset).Truncate(p.Bucket).Add(-offset)
	origin := last.Add(-time.Duration(count-1) * p.Bucket)

	q := store.New(s.db)
	report := UsageReport{Since: origin, Until: now, Bucket: p.Bucket}

	totals, err := q.UsageTotals(ctx, origin.UnixMilli())
	if err != nil {
		return UsageReport{}, fmt.Errorf("reading usage totals: %w", err)
	}

	report.Totals = UsageCounts{
		Requests: totals.Requests, Failed: totals.Failed, Prompt: totals.PromptTokens, Cached: totals.CachedTokens,
		Completion: totals.CompletionTokens, Reasoning: totals.ReasoningTokens,
		CostMicros: totals.CostMicros, Unpriced: totals.Unpriced,
		WebSearches: totals.WebSearches, Audio: time.Duration(totals.AudioMs) * time.Millisecond,
		Characters: totals.Characters, Images: totals.Images,
	}

	buckets, err := q.UsageBuckets(ctx, store.UsageBucketsParams{
		Origin: origin.UnixMilli(), BucketMs: p.Bucket.Milliseconds(),
	})
	if err != nil {
		return UsageReport{}, fmt.Errorf("reading usage a %s: %w", p.Bucket, err)
	}

	byBucket := make(map[int64]store.UsageBucketsRow, len(buckets))
	for _, b := range buckets {
		byBucket[b.Bucket] = b
	}

	report.Buckets = make([]UsageBucket, count)
	for i := range report.Buckets {
		b := byBucket[int64(i)]
		report.Buckets[i] = UsageBucket{
			Start: origin.Add(time.Duration(i) * p.Bucket).UTC(),
			UsageCounts: UsageCounts{
				Requests: b.Requests, Failed: b.Failed, Prompt: b.PromptTokens, Cached: b.CachedTokens,
				Completion: b.CompletionTokens, Reasoning: b.ReasoningTokens, CostMicros: b.CostMicros, Unpriced: b.Unpriced,
				WebSearches: b.WebSearches, Audio: time.Duration(b.AudioMs) * time.Millisecond,
				Characters: b.Characters, Images: b.Images,
			},
		}
	}

	if report.ByModel, err = s.usageByModel(ctx, q, origin); err != nil {
		return UsageReport{}, err
	}

	if report.ByKey, err = s.usageByKey(ctx, q, origin); err != nil {
		return UsageReport{}, err
	}

	if report.ByAccount, err = s.usageByAccount(ctx, q, origin); err != nil {
		return UsageReport{}, err
	}

	if report.ByProvider, report.SpeedByAccount, err = s.performance(ctx, q, origin); err != nil {
		return UsageReport{}, err
	}

	return report, nil
}

// performance compares the text models' requests since a moment, each
// request counted, by provider and model and by account.
func (s *Service) performance(
	ctx context.Context, q *store.Queries, since time.Time,
) ([]PerformanceGroup, []PerformanceGroup, error) {
	rows, err := q.ListPerformanceSamples(ctx, since.UnixMilli())
	if err != nil {
		return nil, nil, fmt.Errorf("reading the requests' speed: %w", err)
	}

	byProvider, byAccount := newPerformanceGroups(), newPerformanceGroups()

	for _, r := range rows {
		sample := performanceSample{
			provider: r.Provider, model: r.UpstreamModel, account: r.AccountID, outcome: Outcome(r.Outcome),
			duration: time.Duration(r.DurationMs) * time.Millisecond, ttft: ttftOf(r.TtftMs),
			completion: r.CompletionTokens, estimated: r.Estimated == 1, costMicros: r.CostMicros.Int64,
		}

		byProvider.get(r.Provider+"\x00"+r.UpstreamModel, func() PerformanceGroup {
			return PerformanceGroup{Provider: r.Provider, Model: r.UpstreamModel}
		}).add(sample)

		if r.AccountID != "" {
			byAccount.get(r.AccountID, func() PerformanceGroup {
				return PerformanceGroup{Provider: r.Provider, AccountID: r.AccountID}
			}).add(sample)
		}
	}

	return byProvider.list(true), byAccount.list(true), nil
}

func (s *Service) usageByModel(ctx context.Context, q *store.Queries, since time.Time) ([]UsageGroup, error) {
	rows, err := q.UsageByModel(ctx, since.UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("reading usage by model: %w", err)
	}

	out := make([]UsageGroup, 0, len(rows))
	for _, r := range rows {
		out = append(out, UsageGroup{ID: r.Name, Name: r.Name, UsageCounts: UsageCounts{
			Requests: r.Requests, Failed: r.Failed, Prompt: r.PromptTokens, Cached: r.CachedTokens,
			Completion: r.CompletionTokens, Reasoning: r.ReasoningTokens, CostMicros: r.CostMicros, Unpriced: r.Unpriced,
			WebSearches: r.WebSearches, Audio: time.Duration(r.AudioMs) * time.Millisecond,
			Characters: r.Characters, Images: r.Images,
		}})
	}

	return out, nil
}

func (s *Service) usageByKey(ctx context.Context, q *store.Queries, since time.Time) ([]UsageGroup, error) {
	rows, err := q.UsageByKey(ctx, since.UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("reading usage by key: %w", err)
	}

	out := make([]UsageGroup, 0, len(rows))
	for _, r := range rows {
		out = append(out, UsageGroup{ID: r.ID, Name: r.Name, UsageCounts: UsageCounts{
			Requests: r.Requests, Failed: r.Failed, Prompt: r.PromptTokens, Cached: r.CachedTokens,
			Completion: r.CompletionTokens, Reasoning: r.ReasoningTokens, CostMicros: r.CostMicros, Unpriced: r.Unpriced,
			WebSearches: r.WebSearches, Audio: time.Duration(r.AudioMs) * time.Millisecond,
			Characters: r.Characters, Images: r.Images,
		}})
	}

	return out, nil
}

// usageByAccount leaves the name empty: the accounts are the control plane's,
// which names them.
func (s *Service) usageByAccount(ctx context.Context, q *store.Queries, since time.Time) ([]UsageGroup, error) {
	rows, err := q.UsageByAccount(ctx, since.UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("reading usage by account: %w", err)
	}

	out := make([]UsageGroup, 0, len(rows))
	for _, r := range rows {
		out = append(out, UsageGroup{ID: r.ID, UsageCounts: UsageCounts{
			Requests: r.Requests, Failed: r.Failed, Prompt: r.PromptTokens, Cached: r.CachedTokens,
			Completion: r.CompletionTokens, Reasoning: r.ReasoningTokens, CostMicros: r.CostMicros, Unpriced: r.Unpriced,
			WebSearches: r.WebSearches, Audio: time.Duration(r.AudioMs) * time.Millisecond,
			Characters: r.Characters, Images: r.Images,
		}})
	}

	return out, nil
}

// UsageRequests returns a page of a period's requests, newest first, and
// whether older ones follow (LiteLLM's has_more).
func (s *Service) UsageRequests(
	ctx context.Context, period string, failedOnly bool, limit, offset int,
) ([]Usage, bool, error) {
	p, ok := UsagePeriods[period]
	if !ok {
		return nil, false, fmt.Errorf("%w: %s", ErrInvalid, periodsText)
	}

	if p.Ledger {
		return nil, false, fmt.Errorf("%w: the requests are kept 31 days; the period is 24h, 7d or 30d", ErrInvalid)
	}

	if limit <= 0 || limit > 200 {
		limit = 50
	}

	rows, err := store.New(s.db).ListUsageRequests(ctx, store.ListUsageRequestsParams{
		Since: s.clock.Now().Add(-p.Span).UnixMilli(), FailedOnly: flag(failedOnly),
		RowLimit: int64(limit + 1), RowOffset: int64(max(offset, 0)),
	})
	if err != nil {
		return nil, false, fmt.Errorf("listing usage: %w", err)
	}

	more := len(rows) > limit
	if more {
		rows = rows[:limit]
	}

	out := make([]Usage, 0, len(rows))
	for _, r := range rows {
		out = append(out, usageOf(store.ListGatewayUsageRow(r)))
	}

	return out, more, nil
}
