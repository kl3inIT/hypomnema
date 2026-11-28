package listening

import (
	"sync"
	"time"
)

// Run is one search as its streams share it: when it began, how many streams read each source, and what a backend
// keeps for the whole search (the time Muse may take, muse.go), as last30days shares one X lane budget across a
// run's calls (grok_x.LANE_BUDGET_SECONDS, pipeline.py:6911-6916).
type Run struct {
	Began   time.Time
	streams map[string]int

	mu     sync.Mutex
	values map[string]any
}

// NewRun is a search begun at began, with how many streams read each source.
func NewRun(began time.Time, streams map[string]int) *Run {
	return &Run{Began: began, streams: streams, values: map[string]any{}}
}

// Streams is how many streams of the search read source.
func (r *Run) Streams(source string) int { return r.streams[source] }

// Value is what the search keeps under key, made by init the first time.
func (r *Run) Value(key string, init func() any) any {
	r.mu.Lock()
	defer r.mu.Unlock()

	v, ok := r.values[key]
	if !ok {
		v = init()
		r.values[key] = v
	}

	return v
}
