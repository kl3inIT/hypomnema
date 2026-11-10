package daemon

import (
	"context"
	"slices"
	"sync"
	"time"
)

// ReachEvery is how often the node tries the git hosts again (inferred: no
// reference places work by which host a machine reaches).
const ReachEvery = time.Minute

// hostWatch knows which of the git hosts the control plane named this machine
// reaches. The control plane asks at hello and with every heartbeat's answer;
// the node tries each host when the list changes and every ReachEvery, and
// says what it found in its heartbeat.
type hostWatch struct {
	reach func(ctx context.Context, host string) bool
	every time.Duration

	mu      sync.Mutex
	asked   []string
	reached []string
	tried   bool
	wake    chan struct{}
}

func newHostWatch(reach func(ctx context.Context, host string) bool) *hostWatch {
	return &hostWatch{reach: reach, every: ReachEvery, wake: make(chan struct{}, 1)}
}

// ask sets the hosts to try. An empty list changes nothing: a control plane
// that could not read its hosts, or one from before it named them, sends none.
func (w *hostWatch) ask(hosts []string) {
	if w == nil || len(hosts) == 0 {
		return
	}

	sorted := slices.Clone(hosts)
	slices.Sort(sorted)

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

// said is what a heartbeat reports: the hosts reached, or nil before the
// first try.
func (w *hostWatch) said() *[]string {
	if w == nil {
		return nil
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if !w.tried {
		return nil
	}

	out := slices.Clone(w.reached)

	return &out
}

// run tries the hosts until ctx ends.
func (w *hostWatch) run(ctx context.Context) {
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

func (w *hostWatch) try(ctx context.Context) {
	w.mu.Lock()
	asked := slices.Clone(w.asked)
	w.mu.Unlock()

	if len(asked) == 0 {
		return
	}

	// Hosts are tried together: one that does not answer costs its timeout once, not once a host.
	found := make([]bool, len(asked))

	var wg sync.WaitGroup

	for i, host := range asked {
		wg.Go(func() { found[i] = w.reach(ctx, host) })
	}

	wg.Wait()

	if ctx.Err() != nil {
		return
	}

	reached := []string{}

	for i, host := range asked {
		if found[i] {
			reached = append(reached, host)
		}
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	// A list that changed while trying is tried again by its own wake; this result is of the old one.
	if slices.Equal(w.asked, asked) {
		w.reached, w.tried = reached, true
	}
}
