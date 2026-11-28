package listening

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"time"
)

// The semantic pass (issue #1306): after the code has ranked and cut the shortlist (layer 1, free), a cheap model
// scores each shortlisted item's relevance in one batched call and reviews the clusters in a second (layer 2); then a
// model with a long context reads the final items in full and extracts what the report cites (layer 2b). The Bot's
// own model still writes the report (layer 3, last30days SKILL.md LAW 7). With no model, or on a model's failure or
// an answer that is not what was asked, the code's ranking and clusters stand and the answer says so: last30days
// rerank_candidates falls back to its local score (lib/rerank.py:316-337), jev-social to its deterministic evidence
// report (src/report.js:133-165).

// The depths a plan asks for, and what each lets through: last30days DEPTH_SETTINGS (pool_limit and rerank_limit,
// pipeline.py:119-123), whose quick, default and deep runs are trend-opportunity-radar's quick, standard and deep
// modes (references/sampling-contract.md); the full reads are radar's detail pages per mode, 4-8, 12-18 and 20-40
// (its upper bounds, deep's lower one: twenty pages of up to websearch.MaxContent characters keep the read inside
// gpt-6-luna's 272k-token context).
const (
	DepthQuick    = "quick"
	DepthStandard = "standard"
	DepthDeep     = "deep"
)

// Limits are a search's bounds, as the answer shows them (last30days #716: caps can be overridden, and the run says
// which it used).
type Limits struct {
	Depth string `json:"depth"`
	// Pool is how many items the answer lists, the best first.
	Pool int `json:"pool"`
	// Shortlist is how many of them the model scores; the rest keep the code's score (last30days rerank_limit).
	Shortlist int `json:"shortlist"`
	// Read is how many of the clusters' representatives are read in full.
	Read int `json:"read"`
}

var depths = map[string]Limits{
	DepthQuick:    {Depth: DepthQuick, Pool: 15, Shortlist: 12, Read: 8},
	DepthStandard: {Depth: DepthStandard, Pool: PoolSize, Shortlist: PoolSize, Read: 18},
	DepthDeep:     {Depth: DepthDeep, Pool: MaxPool, Shortlist: MaxPool, Read: MaxRead},
}

const (
	// MaxPool is deep's pool, the most a plan may ask: about 60 items of some 150 tokens make the review's one call
	// of about 10k tokens (issue #1306's estimate).
	MaxPool = 60
	// MaxRead is deep's full reads, the most a plan may ask.
	MaxRead = 20
)

// The model steps' deadlines and attempts. The review's two calls are last30days' bound on a remote model step: 30
// seconds and 2 attempts (planner.py:550-556, pipeline.py:175; PR #1618, after an unbounded call took about 480 s,
// #1602). The full read is jev-social's report synthesis: one call of 60 seconds (src/report.js:177).
const (
	reviewDeadline = 30 * time.Second
	reviewAttempts = 2
	readDeadline   = 60 * time.Second
	readAttempts   = 1
)

// The steps' states.
const (
	// StepModel: the model's judgment is in the answer.
	StepModel = "model"
	// StepFallback: the model was not used or failed; the code's ranking and clusters stand, and Reason says why.
	StepFallback = "fallback"
	// StepOff: the control plane's configuration turned the step off.
	StepOff = "off"
)

// Pass reports the semantic pass: each step's state, model and what it cost (issue #1306's cost line, last30days
// PR #1243: tokens reported after the run, unknown when the count is incomplete).
type Pass struct {
	// Rerank scored the shortlist's relevance; Clusters reviewed the clusters; Read read the final items in full.
	Rerank   Step `json:"rerank"`
	Clusters Step `json:"clusters"`
	Read     Step `json:"read"`
}

// Step is one step of the pass.
type Step struct {
	State string `json:"state"`
	Model string `json:"model,omitempty"`
	// Reason says why the step fell back, or what the model's answer lost.
	Reason string `json:"reason,omitempty"`
	// Detail says what the step did: how many items it judged, read, or found in the cache.
	Detail string `json:"detail,omitempty"`
	Calls  int    `json:"calls"`
	// Cached counts the items whose judgment came from the cache, without a call.
	Cached int `json:"cached,omitempty"`
	// PromptTokens and CompletionTokens are the calls' tokens; absent when a call did not count them.
	PromptTokens     *int    `json:"prompt_tokens,omitempty"`
	CompletionTokens *int    `json:"completion_tokens,omitempty"`
	Seconds          float64 `json:"seconds"`
}

// DepthLimits are a depth's bounds; zero for a depth that is not one.
func DepthLimits(depth string) Limits { return depths[depth] }

// limits are a checked plan's bounds: its depth's, with what the plan overrides.
func (p Plan) limits() Limits {
	l := depths[p.Depth]
	if p.Pool > 0 {
		l.Pool, l.Shortlist = p.Pool, p.Pool
	}

	if p.Read > 0 {
		l.Read = p.Read
	}

	l.Shortlist = min(l.Shortlist, l.Pool)

	return l
}

// modelFor is the model a step asks: the configured one, ModelOff, or the default.
func modelFor(configured, fallback string) string {
	if configured == "" {
		return fallback
	}

	return configured
}

// semantic runs the pass over the cut pool, ordered best first, and answers the items, ordered again, their clusters
// and the pass's report.
func (s *Service) semantic(ctx context.Context, plan Plan, items []Item, c Caller, l Limits) ([]Item, []Cluster,
	Pass,
) {
	var pass Pass

	topic := plan.topic()

	items, pass.Rerank = s.rerank(ctx, plan, items, c, l)
	lexical := clusterItems(items, topic)

	clusters, review := s.reviewClusters(ctx, topic, items, lexical, c)
	pass.Clusters = review

	pass.Read = s.read(ctx, topic, items, clusters, s.request(plan, c), c, l)

	return items, clusters, pass
}

// step runs one model step with its model: off when the configuration turned it off, a fallback when the caller
// has no model, else run with the model, then the default model when the configured one is not served (last30days
// falls back to its default model with a warning and goes on, lib/providers.py:473-490, PR #1439).
func (s *Service) step(ctx context.Context, name, configured, fallback string, c Caller,
	run func(model string, t *tally, st *Step) error,
) Step {
	model := modelFor(configured, fallback)
	if model == ModelOff {
		return Step{State: StepOff}
	}

	if c.Model == nil {
		return Step{State: StepFallback, Model: model, Reason: "no gateway model can be asked for this caller"}
	}

	started := s.now()

	var t tally

	st := Step{State: StepModel, Model: model}

	err := run(model, &t, &st)
	if errors.Is(err, ErrModelNotServed) && model != fallback {
		s.logger.WarnContext(ctx, "a listening model is not served; its default is asked",
			slog.String("event", "listening.model_not_served"), slog.String("step", name),
			slog.String("model", model), slog.String("default", fallback))

		st.Reason = fmt.Sprintf("the gateway serves no model %s; %s was asked instead", model, fallback)
		st.Model = fallback
		err = run(fallback, &t, &st)
	}

	if err != nil {
		s.logger.WarnContext(ctx, "a listening model step fell back", slog.String("event", "listening.model_fallback"),
			slog.String("step", name), slog.String("model", st.Model), slog.String("error", err.Error()))

		st.State = StepFallback
		st.Reason = joinReason(st.Reason, oneLine(err.Error()))
	}

	t.report(&st)
	st.Seconds = math.Round(s.now().Sub(started).Seconds()*10) / 10

	return st
}

func joinReason(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	default:
		return a + "; " + b
	}
}
