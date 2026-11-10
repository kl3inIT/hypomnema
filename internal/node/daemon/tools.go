package daemon

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// ToolRetryEvery is how often the node tries again a tool it could not
// install, as it tries the git hosts again (hosts.go).
const ToolRetryEvery = 5 * time.Minute

// toolWatch has the tools the control plane names installed on this machine
// (issue #103). The control plane names them at hello and with every
// heartbeat's answer, as it names git hosts; the node installs a missing one,
// tries a failed one again every ToolRetryEvery, and says what it has in its
// heartbeat.
type toolWatch struct {
	install func(ctx context.Context, tool nodeproto.Tool) error
	every   time.Duration

	mu     sync.Mutex
	asked  []nodeproto.Tool
	states []nodeproto.ToolState
	tried  bool
	wake   chan struct{}
}

func newToolWatch(install func(ctx context.Context, tool nodeproto.Tool) error) *toolWatch {
	return &toolWatch{install: install, every: ToolRetryEvery, wake: make(chan struct{}, 1)}
}

// ask sets the tools to have. An empty list changes nothing: a control plane
// that names none, or one from before it named them, takes no tool away.
func (w *toolWatch) ask(tools []nodeproto.Tool) {
	if w == nil || len(tools) == 0 {
		return
	}

	sorted := slices.Clone(tools)
	slices.SortFunc(sorted, func(a, b nodeproto.Tool) int { return strings.Compare(a.Name, b.Name) })

	w.mu.Lock()
	changed := !slices.Equal(w.asked, sorted)
	w.asked = sorted
	w.mu.Unlock()

	if changed {
		select {
		case w.wake <- struct{}{}:
		default:
		}
	}
}

// said is what a heartbeat reports: what the node has of each tool, or nil
// before the first try.
func (w *toolWatch) said() *[]nodeproto.ToolState {
	if w == nil {
		return nil
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if !w.tried {
		return nil
	}

	out := slices.Clone(w.states)

	return &out
}

// run installs the tools until ctx ends.
func (w *toolWatch) run(ctx context.Context) {
	tick := time.NewTicker(w.every)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-w.wake:
		case <-tick.C:
		}

		w.try(ctx)
	}
}

func (w *toolWatch) try(ctx context.Context) {
	w.mu.Lock()
	asked := slices.Clone(w.asked)
	w.mu.Unlock()

	if len(asked) == 0 {
		return
	}

	// One after another: an install is a download.
	states := make([]nodeproto.ToolState, 0, len(asked))

	for _, tool := range asked {
		state := nodeproto.ToolState{Name: tool.Name, Version: tool.Version}
		if err := w.install(ctx, tool); err != nil {
			state.Version, state.Error = "", err.Error()
		}

		states = append(states, state)
	}

	if ctx.Err() != nil {
		return
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	// A list that changed while installing is tried again by its own wake; this result is of the old one.
	if slices.Equal(w.asked, asked) {
		w.states, w.tried = states, true
	}
}
