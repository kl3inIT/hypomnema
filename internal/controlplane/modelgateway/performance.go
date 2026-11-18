package modelgateway

import (
	"cmp"
	"math"
	"slices"
	"strings"
	"time"
)

// How fast and how reliably each provider, model and account answered (issue #856), as new-api's perf_metrics
// (model/perf_metric.go:40-46), 9router-go's usage by provider and model (internal/db/usage.go:453-568) and
// OmniRoute's speed ranking (p95, tokens a second) compare them. Only the text models are compared: a search, a
// recording or an image has no first token.

// Performance is the speed and the reliability of a set of requests to the text models.
type Performance struct {
	// Requests counts them all; OK those answered, Failed those that failed, and ClientGone those whose client
	// left first, which count neither way.
	Requests, OK, Failed, ClientGone int64
	CostMicros                       int64
	// The percentiles and the median tokens a second need each request, so a period read from the daily ledger
	// has none, only the averages (Exact false). TTFT is over the answered streams; Latency, the whole call,
	// over the answered requests; TokensPerSecond over the answered streams whose tokens were reported.
	Exact                    bool
	TTFTP50, TTFTP95         *time.Duration
	LatencyP50, LatencyP95   *time.Duration
	TTFTAvg, LatencyAvg      *time.Duration
	TokensPerSecond          *float64
	ttfts, latencies         []time.Duration
	rates                    []float64
	ttftSum, latencySum      time.Duration
	ttftCount, latencyCount  int64
	generationMs, generation int64
}

// SuccessRate is the share of the requests answered, those whose client left left out; false when there is none.
func (p *Performance) SuccessRate() (float64, bool) {
	n := p.Requests - p.ClientGone
	if n <= 0 {
		return 0, false
	}

	return float64(p.OK) / float64(n), true
}

// PerformanceGroup is the performance of a provider's model, or of an account.
type PerformanceGroup struct {
	Provider, Model, AccountID string
	Performance
}

// performanceSample is one request as the comparison reads it.
type performanceSample struct {
	provider, model, account string
	outcome                  Outcome
	duration                 time.Duration
	ttft                     *time.Duration
	completion               int64
	estimated                bool
	costMicros               int64
}

// add counts one request, kept whole for the percentiles.
func (p *Performance) add(s performanceSample) {
	p.Requests++
	p.CostMicros += s.costMicros

	switch {
	case s.outcome == OutcomeOK:
		p.OK++
	case s.outcome == OutcomeClientGone:
		p.ClientGone++

		return
	case s.outcome.failed():
		p.Failed++

		return
	}

	p.latencies = append(p.latencies, s.duration)
	p.latencySum += s.duration
	p.latencyCount++

	if s.ttft != nil {
		p.ttfts = append(p.ttfts, *s.ttft)
		p.ttftSum += *s.ttft
		p.ttftCount++
	}

	if gen, ok := generation(s.duration.Milliseconds(), s.ttft, s.estimated); ok && s.completion > 0 {
		p.rates = append(p.rates, float64(s.completion)/(float64(gen)/1000))
		p.generationMs += gen
		p.generation += s.completion
	}
}

// addSums counts a ledger row: sums only.
func (p *Performance) addSums(requests, ok, failed, gone, cost, okMs, ttftMs, ttftCount, genMs, genTokens int64) {
	p.Requests += requests
	p.OK += ok
	p.Failed += failed
	p.ClientGone += gone
	p.CostMicros += cost
	p.latencySum += time.Duration(okMs) * time.Millisecond
	p.latencyCount += ok
	p.ttftSum += time.Duration(ttftMs) * time.Millisecond
	p.ttftCount += ttftCount
	p.generationMs += genMs
	p.generation += genTokens
}

// finish works out the figures once every request is counted.
func (p *Performance) finish(exact bool) {
	p.Exact = exact

	if p.ttftCount > 0 {
		p.TTFTAvg = ptrTo(p.ttftSum / time.Duration(p.ttftCount))
	}

	if p.latencyCount > 0 {
		p.LatencyAvg = ptrTo(p.latencySum / time.Duration(p.latencyCount))
	}

	if exact {
		p.TTFTP50, p.TTFTP95 = percentile(p.ttfts, 0.5), percentile(p.ttfts, 0.95)
		p.LatencyP50, p.LatencyP95 = percentile(p.latencies, 0.5), percentile(p.latencies, 0.95)

		if m := percentile(p.rates, 0.5); m != nil {
			p.TokensPerSecond = m
		}
	} else if p.generationMs > 0 {
		p.TokensPerSecond = ptrTo(float64(p.generation) / (float64(p.generationMs) / 1000))
	}

	p.ttfts, p.latencies, p.rates = nil, nil, nil
}

// percentile is the nearest-rank percentile of values, nil when there is none.
func percentile[T cmp.Ordered](values []T, q float64) *T {
	if len(values) == 0 {
		return nil
	}

	sorted := slices.Clone(values)
	slices.Sort(sorted)

	rank := max(int(math.Ceil(q*float64(len(sorted))))-1, 0)

	return ptrTo(sorted[min(rank, len(sorted)-1)])
}

func ptrTo[T any](v T) *T { return &v }

// performanceGroups gathers the groups key names, the most used first.
type performanceGroups struct {
	order  []string
	groups map[string]*PerformanceGroup
}

func newPerformanceGroups() *performanceGroups {
	return &performanceGroups{groups: map[string]*PerformanceGroup{}}
}

func (g *performanceGroups) get(key string, newGroup func() PerformanceGroup) *PerformanceGroup {
	if p, ok := g.groups[key]; ok {
		return p
	}

	p := newGroup()
	g.groups[key] = &p
	g.order = append(g.order, key)

	return &p
}

func (g *performanceGroups) list(exact bool) []PerformanceGroup {
	out := make([]PerformanceGroup, 0, len(g.order))

	for _, k := range g.order {
		p := g.groups[k]
		p.finish(exact)
		out = append(out, *p)
	}

	slices.SortStableFunc(out, func(a, b PerformanceGroup) int {
		return cmp.Or(cmp.Compare(b.Requests, a.Requests), cmp.Compare(a.Provider, b.Provider),
			cmp.Compare(a.Model, b.Model), cmp.Compare(a.AccountID, b.AccountID))
	})

	return out
}

// textModel says whether a public model name is a text model's, the ones compared.
func textModel(model string) bool {
	return !strings.HasPrefix(model, "web/") && !strings.HasPrefix(model, "audio/") &&
		!strings.HasPrefix(model, "image/")
}
