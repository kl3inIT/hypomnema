// Package listening finds what people say about a topic, for every agent (issue #1287, step 2 of #781): the
// control plane's fourth gateway capability, beside web search, images and Muse, served to a worker and a Bot's pi as
// the tool listen_search. The calling model writes the plan, its subqueries, sources and window; this package reads
// each (subquery, source) stream through the source's backends, removes duplicates, ranks, clusters, and answers with
// the items, the clusters and what each source covered. It writes no prose: the model does (last30days SKILL.md LAW 7,
// "you are the planner"; obsei uses a model only to enrich and ask).
//
// What it does is learned from the projects that collect social posts (docs/research/2026-11-27-social-listening.md):
//
//   - a source, a platform, is an ordered chain of backends; each declares a side-effect-free probe, whether it is
//     opt-in, and the site it calls; the first usable backend serves, and the answer names it and every backend
//     tried, with its failure code: Agent-Reach channels/base.py:30-50 and runtime/dispatch.py:211-322, last30days
//     lib/backends.py:97-160 and 779-855 (registry.go);
//   - a failure is one of a closed set of codes (failure.go), and only a clean zero, no_results, says nobody
//     talked; any other is "not read" with its cause: last30days lib/schema.py:383-554 and
//     references/synthesis.md, last30days-cn SKILL.md §5 ("never present a failed or empty platform as nobody is
//     talking about it") (coverage.go);
//   - a backend that was refused, rate limited or spent its quota rests, and is skipped until then: last30days
//     lib/reddit_health.py:10-17 (a 403 skips a path for 6 hours), Agent-Reach runtime/dispatch.py:27-30
//     and 89-120 (rest.go);
//   - one call at a time per site, with a minimum gap between two: Agent-Reach runtime/browser_binding.py and
//     dispatch.py _pace_browser, linkedin-mcp-server's sequential middleware, last30days lib/http.py:2321-2444
//     (pace.go);
//   - items are deduplicated by (source, native id), then by their normalized address, then by near-duplicate text
//     within a source at Jaccard 0.7: obsei core/record.py:74-76, last30days lib/fusion.py:35-63 and
//     lib/dedupe.py:75-235 (dedupe.go);
//   - streams are fused by weighted reciprocal rank, engagement normalized per source on a log scale: last30days
//     lib/fusion.py:850-1039, lib/signals.py:225-582, lib/rerank.py:974-1274 (rank.go);
//   - items are grouped by greedy clustering, each cluster tagged single-source or thin-evidence: last30days
//     lib/cluster.py:61-346, with the topic's own words removed before comparing, last30days-cn lib/dedupe.py
//     strip_query_terms and harken analyze/insights.py (cluster.go).
//
// It stores nothing: what a backend rests for is kept in memory, the control plane being one process that lives
// across calls (last30days, a process per run, writes it to a file).
package listening

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/websearch"
)

// Errors callers match with errors.Is.
var (
	// ErrInvalid: the plan cannot be run as written; the error says why.
	ErrInvalid = errors.New("invalid plan")
)

// The plan's bounds.
const (
	// MaxSubqueries is the most subqueries a plan has: last30days' planner gives at most 5 (planner.py:1479-1496).
	MaxSubqueries = 5
	// DefaultWindowDays is the window a plan that names none reads: last30days' 30 days.
	DefaultWindowDays = 30
	// MaxWindowDays is the longest window: a year, Reddit's widest bucket below "all" (last30days reddit.py).
	MaxWindowDays = 365
	// DefaultResults and MaxResults bound what each (subquery, source) stream keeps: last30days' default and deep
	// depths, 25 and 50 threads (reddit_public.py:39-44, DEPTH_LIMITS); its quick depth's 10 is for a quick run,
	// which a plan asks for with max_results. A web search answers 20 at most (websearch.MaxResults).
	DefaultResults = 25
	MaxResults     = 50
	// PoolSize is how many items the answer lists, the best first: last30days' default pool (fusion.py:537-543).
	PoolSize = 40
)

// Plan is what the calling model asks: its subqueries, the sources each reads, and the window (last30days QueryPlan
// and SubQuery, lib/schema.py:46-80).
type Plan struct {
	// Topic is what the plan is about, in a few words; its words are not counted when items are clustered, since
	// every item names it (last30days-cn strip_query_terms). Empty: the first subquery's query.
	Topic      string     `json:"topic,omitempty"`
	Subqueries []Subquery `json:"subqueries"`
	// Sources are read by every subquery that names none.
	Sources []string `json:"sources,omitempty"`
	// SourceWeights multiply a source's streams in the ranking; 1 when not given (last30days source_weights).
	SourceWeights map[string]float64 `json:"source_weights,omitempty"`
	// WindowDays is how many days back an item may be dated; DefaultWindowDays when 0.
	WindowDays int `json:"window_days,omitempty"`
	// MaxResults is how many items each stream reads; DefaultResults when 0.
	MaxResults int `json:"max_results,omitempty"`
	// Backends pins a source to one of its backends, the only one then asked: how an opt-in backend is used
	// (last30days pins, lib/backends.py:799-833).
	Backends map[string]string `json:"backends,omitempty"`
}

// Subquery is one search of the plan.
type Subquery struct {
	// Label names it in the answer; "q1", "q2"… when empty.
	Label string `json:"label,omitempty"`
	Query string `json:"query"`
	// Sources it reads; the plan's when empty.
	Sources []string `json:"sources,omitempty"`
	// Weight multiplies its streams in the ranking; 1 when 0.
	Weight float64 `json:"weight,omitempty"`
}

// Item is one thing someone said: the item of last30days' SourceItem (lib/schema.py:83-106), obsei's Record and
// harken's Mention.
type Item struct {
	// ID is stable for the same post: a hash of its source and native id (obsei Record.make_id).
	ID string `json:"id"`
	// Source is the platform; SeenIn the other sources that found the same address.
	Source string   `json:"source"`
	SeenIn []string `json:"seen_in,omitempty"`
	// Backend is the backend that read it.
	Backend string `json:"backend"`
	// NativeID is the source's own id: a Reddit post's id, a web page's normalized address.
	NativeID string `json:"native_id"`
	URL      string `json:"url"`
	Title    string `json:"title,omitempty"`
	Author   string `json:"author,omitempty"`
	// Container is where it was said: a subreddit, a group, a site.
	Container string `json:"container,omitempty"`
	// PublishedAt is its date, when the source gives one; DateConfidence is high when it is the source's own date
	// inside the window, low otherwise (last30days dates.get_date_confidence).
	PublishedAt    *time.Time `json:"published_at,omitempty"`
	DateConfidence string     `json:"date_confidence"`
	// Text is what was said, cut at maxTextRunes.
	Text string `json:"text,omitempty"`
	// Engagement is the source's own counts (a Reddit post's score and comments); EngagementScore is them on a
	// 0-100 log scale of that source, absent for a source whose counts are not readers' (a web page).
	Engagement      map[string]float64 `json:"engagement,omitempty"`
	EngagementScore *int               `json:"engagement_score,omitempty"`
	// Parent is the native id of the post a comment answers.
	Parent string `json:"parent,omitempty"`
	// Subqueries are the labels of the subqueries that found it.
	Subqueries []string  `json:"subqueries"`
	FetchedAt  time.Time `json:"fetched_at"`
	// Score orders the answer's items, higher first; Cluster is the cluster it is in.
	Score   float64 `json:"score"`
	Cluster string  `json:"cluster,omitempty"`

	// signals for the ranking, never answered.
	relevance float64
	freshness int
	rrf       float64
}

// Answer is what a search found, and what it could not read.
type Answer struct {
	// From and To are the window, as dates.
	From string `json:"from"`
	To   string `json:"to"`
	// Coverage says, for each source, what was read and what was not, and why.
	Coverage []Coverage `json:"coverage"`
	Clusters []Cluster  `json:"clusters"`
	// Items are the best PoolSize, the best first; Omitted counts those left out.
	Items   []Item `json:"items"`
	Omitted int    `json:"omitted,omitempty"`
}

// WebSearch asks the control plane's web search chain, recorded on the caller's gateway key.
type WebSearch func(ctx context.Context, query string, limit int) (websearch.Answer, error)

// Config is what a Service needs.
type Config struct {
	// Platforms are the sources, in the order they are answered; nil: Sources(Endpoints{}).
	Platforms []Platform
	// Now is the clock; nil: the wall clock.
	Now func() time.Time
	// Sleep waits between two calls to one site; nil: a timer.
	Sleep  func(ctx context.Context, d time.Duration) error
	Logger *slog.Logger
}

// Service searches the sources.
type Service struct {
	platforms []Platform
	now       func() time.Time
	pacer     *Pacer
	logger    *slog.Logger

	mu sync.Mutex
	// resting is each backend's rest, by "<platform>/<backend>".
	resting map[string]*SourceError
}

// New returns a Service.
func New(cfg Config) *Service {
	if cfg.Platforms == nil {
		cfg.Platforms = Sources(Endpoints{}, http.DefaultClient)
	}

	if cfg.Now == nil {
		cfg.Now = time.Now
	}

	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}

	return &Service{
		platforms: cfg.Platforms, now: cfg.Now, pacer: NewPacer(cfg.Now, cfg.Sleep), logger: cfg.Logger,
		resting: map[string]*SourceError{},
	}
}

// SourceNames are the sources a plan may name, in order.
func (s *Service) SourceNames() []string {
	out := make([]string, 0, len(s.platforms))
	for _, p := range s.platforms {
		out = append(out, p.Name)
	}

	return out
}

// Search runs a plan; web asks the web search chain for the backends that use it (nil: they cannot be asked).
func (s *Service) Search(ctx context.Context, plan Plan, web WebSearch) (Answer, error) {
	plan, err := s.check(plan)
	if err != nil {
		return Answer{}, err
	}

	// The web searches a plan makes wait for their route's turn, one call at a time per key or keyless provider and
	// webGap apart, where the chain knows the route (websearch.WithPace).
	// Listening asks only the keyless providers, never the owner's keys or subscriptions: no paid service (owner,
	// 2026-11-27), as last30days reads Reddit through keyless web search (reddit_discover.py:322-350).
	ctx = websearch.KeylessOnly(ctx)
	ctx = websearch.WithPace(ctx, func(ctx context.Context, route string) (func(), error) {
		return s.pacer.Wait(ctx, "web:"+route, webGap)
	})

	now := s.now().UTC()
	from := now.AddDate(0, 0, -plan.WindowDays)

	// Each source runs apart, its subqueries one after another, so a site's refusal is known before its next
	// subquery asks it (last30days records a path's state for the rest of the run, reddit_health.py:250-292).
	type sourceRun struct {
		coverage Coverage
		streams  []stream
	}

	runs := make([]sourceRun, len(s.platforms))

	var wg sync.WaitGroup

	for i, p := range s.platforms {
		labels := plan.subqueriesOf(p.Name)
		if len(labels) == 0 {
			continue
		}

		wg.Go(func() {
			cov := Coverage{Source: p.Name}

			var streams []stream

			for _, sq := range labels {
				req := Request{
					Query: sq.Query, Topic: plan.topic(), Limit: plan.MaxResults, From: from, To: now, Web: web,
					Pace: s.pacer.Wait, Now: s.now,
					Resting: func(path string) *SourceError { return s.restOf("path:" + path) },
					Rest:    func(path string, f *SourceError) time.Time { return s.rest("path:"+path, f) },
				}
				st := s.runStream(ctx, p, plan.Backends[p.Name], sq, req)
				cov.Streams = append(cov.Streams, st.report)
				streams = append(streams, st)
			}

			runs[i] = sourceRun{coverage: cov.settle(), streams: streams}
		})
	}

	wg.Wait()

	answer := Answer{From: from.Format(time.DateOnly), To: now.Format(time.DateOnly)}

	var streams []stream

	for _, r := range runs {
		if r.coverage.Source == "" {
			continue
		}

		answer.Coverage = append(answer.Coverage, r.coverage)
		streams = append(streams, r.streams...)
	}

	items := rank(plan, streams, now)
	if len(items) > PoolSize {
		answer.Omitted = len(items) - PoolSize
		items = items[:PoolSize]
	}

	answer.Clusters = clusterItems(items, plan.topic())
	answer.Items = items

	if answer.Items == nil {
		answer.Items = []Item{}
	}

	if answer.Clusters == nil {
		answer.Clusters = []Cluster{}
	}

	return answer, nil
}

// check fills a plan's defaults and refuses one that cannot be run, saying why.
func (s *Service) check(plan Plan) (Plan, error) {
	known := s.SourceNames()

	if plan.WindowDays == 0 {
		plan.WindowDays = DefaultWindowDays
	}

	if plan.MaxResults == 0 {
		plan.MaxResults = DefaultResults
	}

	switch {
	case len(plan.Subqueries) == 0:
		return Plan{}, fmt.Errorf("%w: a plan needs at least one subquery", ErrInvalid)
	case len(plan.Subqueries) > MaxSubqueries:
		return Plan{}, fmt.Errorf("%w: a plan has at most %d subqueries", ErrInvalid, MaxSubqueries)
	case plan.WindowDays < 1 || plan.WindowDays > MaxWindowDays:
		return Plan{}, fmt.Errorf("%w: window_days is from 1 to %d", ErrInvalid, MaxWindowDays)
	case plan.MaxResults < 1 || plan.MaxResults > MaxResults:
		return Plan{}, fmt.Errorf("%w: max_results is from 1 to %d", ErrInvalid, MaxResults)
	}

	sourceErr := func(name string) error {
		if slices.Contains(known, name) {
			return nil
		}

		return fmt.Errorf("%w: %q is not a source; the sources are %s", ErrInvalid, name, strings.Join(known, ", "))
	}

	for _, name := range plan.Sources {
		if err := sourceErr(name); err != nil {
			return Plan{}, err
		}
	}

	for name, w := range plan.SourceWeights {
		if err := sourceErr(name); err != nil {
			return Plan{}, err
		}

		if w <= 0 {
			return Plan{}, fmt.Errorf("%w: a source's weight is above 0", ErrInvalid)
		}
	}

	for name, backend := range plan.Backends {
		if err := sourceErr(name); err != nil {
			return Plan{}, err
		}

		p := s.platform(name)
		if !slices.ContainsFunc(p.Backends, func(b Backend) bool { return b.Name == backend }) {
			return Plan{}, fmt.Errorf("%w: %s has no backend %q; its backends are %s", ErrInvalid, name, backend,
				strings.Join(p.backendNames(), ", "))
		}
	}

	labels := map[string]bool{}

	for i := range plan.Subqueries {
		sq := &plan.Subqueries[i]
		sq.Query = strings.TrimSpace(sq.Query)
		sq.Label = strings.TrimSpace(sq.Label)

		if sq.Label == "" {
			sq.Label = fmt.Sprintf("q%d", i+1)
		}

		if sq.Weight == 0 {
			sq.Weight = 1
		}

		if len(sq.Sources) == 0 {
			sq.Sources = plan.Sources
		}

		switch {
		case sq.Query == "":
			return Plan{}, fmt.Errorf("%w: subquery %s has no query", ErrInvalid, sq.Label)
		case labels[sq.Label]:
			return Plan{}, fmt.Errorf("%w: two subqueries are labeled %s", ErrInvalid, sq.Label)
		case sq.Weight < 0:
			return Plan{}, fmt.Errorf("%w: subquery %s's weight is above 0", ErrInvalid, sq.Label)
		case len(sq.Sources) == 0:
			return Plan{}, fmt.Errorf("%w: subquery %s reads no source; the sources are %s", ErrInvalid, sq.Label,
				strings.Join(known, ", "))
		}

		labels[sq.Label] = true

		for _, name := range sq.Sources {
			if err := sourceErr(name); err != nil {
				return Plan{}, err
			}
		}
	}

	return plan, nil
}

func (s *Service) platform(name string) Platform {
	for _, p := range s.platforms {
		if p.Name == name {
			return p
		}
	}

	return Platform{}
}

// subqueriesOf are the plan's subqueries that read a source, in order.
func (p Plan) subqueriesOf(source string) []Subquery {
	var out []Subquery

	for _, sq := range p.Subqueries {
		if slices.Contains(sq.Sources, source) {
			out = append(out, sq)
		}
	}

	return out
}

// topic is what every item names: the plan's topic, else its first subquery's query (last30days' primary subquery).
func (p Plan) topic() string {
	if strings.TrimSpace(p.Topic) != "" {
		return p.Topic
	}

	return p.Subqueries[0].Query
}

// weight is a stream's weight in the ranking: its subquery's times its source's (last30days fusion.py:891).
func (p Plan) weight(sq Subquery, source string) float64 {
	w := sq.Weight

	if sw, ok := p.SourceWeights[source]; ok {
		w *= sw
	}

	return w
}
