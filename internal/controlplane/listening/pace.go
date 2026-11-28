package listening

import (
	"context"
	"sync"
	"time"
)

// Pacer lets one call at a time go to a host, at least a gap after the last one there: linkedin-mcp-server runs one
// tool call at a time against its account (sequential_tool_middleware.py) and Agent-Reach waits a set interval
// between two searches of a site (runtime/dispatch.py _pace_browser, BROWSER_SEARCH_INTERVAL) and queues a site's
// calls (runtime/browser_binding.py query_slot); last30days paces its keyless Reddit calls at 1 a second
// (lib/http.py:2321-2444). Agent-Reach lets two reads of a site run at once (SITE_SLOTS); one is taken here, the
// rule of linkedin-mcp-server and of the research note's pacing row for a signed-in account.
type Pacer struct {
	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) error

	mu    sync.Mutex
	hosts map[string]*hostTurn
}

// hostTurn is a host's turn: a slot of one, and when its last call ended.
type hostTurn struct {
	slot chan struct{}
	last time.Time
}

// NewPacer returns a Pacer on a clock and a sleep; nil: the wall clock and a timer.
func NewPacer(now func() time.Time, sleep func(ctx context.Context, d time.Duration) error) *Pacer {
	if now == nil {
		now = time.Now
	}

	if sleep == nil {
		sleep = timerSleep
	}

	return &Pacer{now: now, sleep: sleep, hosts: map[string]*hostTurn{}}
}

func timerSleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Wait waits for host's turn and for gap after its last call, and gives the function that ends the turn. An empty
// host is not paced.
func (p *Pacer) Wait(ctx context.Context, host string, gap time.Duration) (func(), error) {
	if host == "" {
		return func() {}, nil
	}

	p.mu.Lock()

	turn, ok := p.hosts[host]
	if !ok {
		turn = &hostTurn{slot: make(chan struct{}, 1)}
		p.hosts[host] = turn
	}
	p.mu.Unlock()

	select {
	case turn.slot <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	if !turn.last.IsZero() {
		if wait := turn.last.Add(gap).Sub(p.now()); wait > 0 {
			if err := p.sleep(ctx, wait); err != nil {
				<-turn.slot

				return nil, err
			}
		}
	}

	var once sync.Once

	return func() {
		once.Do(func() {
			turn.last = p.now()
			<-turn.slot
		})
	}, nil
}
