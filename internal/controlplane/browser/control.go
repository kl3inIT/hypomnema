package browser

import (
	"sync"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/ownerhold"
)

// Who drives each tab of the browser (issues #219, #1113): its agent, or the
// owner who took control of it. Taking control holds only the tab taken, as
// T3 Code's SessionControl is one tab's (apps/server/src/preview/SessionControl.ts:
// an agent "may act on its own tab ... but never while a human controls the
// tab"): every tab here is one agent's own, pinned to its session, and no
// agent reaches another's. OpenMausBot holds a bot's whole profile instead
// (server/browser/control.ts, ProfileControl), where one bot drives every tab
// of its own browser and may bring any of them to the front; here many agents
// share one browser, and holding all of it paused a Bot in its own tab while
// the owner signed in in his (issue #1113). The rules of one tab's hold are
// package ownerhold's, which the phone shares.

// The limits of the owner's hold (ownerhold).
const (
	TakeoverWait = ownerhold.TakeoverWait
	HoldGrace    = ownerhold.Grace
	HoldIdle     = ownerhold.Idle
)

// Refusal is what an agent's call is answered while the owner holds its tab.
const Refusal = "Browser tools are paused while the owner controls your tab of this browser. Wait for them to " +
	"hand control back; do not try another browser or another way to reach the page."

// holds are the owner's holds, one a tab by the tab's ID (the agent's name in
// the browser, or OwnerTab), with a lock of their own: an agent's call enters
// its tab's hold before the browser is started, which the engine's mu is held
// for. The engine's mu, when both are held, is taken first.
type holds struct {
	mu  sync.Mutex
	all map[string]*ownerhold.Hold
	// views counts the owner's open views of every tab, at most maxViews.
	views int
}

// of is a tab's hold, made when it has none. The caller holds mu.
func (h *holds) of(tab string) *ownerhold.Hold {
	hold, ok := h.all[tab]
	if !ok {
		if h.all == nil {
			h.all = map[string]*ownerhold.Hold{}
		}

		hold = ownerhold.New()
		h.all[tab] = hold
	}

	return hold
}

// enter starts an agent's step in its tab: the tab's hold, to leave once the
// step ended, and false when the owner holds the tab.
func (h *holds) enter(tab string) (*ownerhold.Hold, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	hold := h.of(tab)

	return hold, hold.Enter()
}

// holding says whether the owner holds a tab, making no hold for it.
func (h *holds) holding(tab string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	hold, ok := h.all[tab]
	if !ok {
		return false
	}

	held, _ := hold.State()

	return held
}

// open counts a view of a tab in and returns the tab's hold; false when as
// many views are open as may be.
func (h *holds) open(tab string) (*ownerhold.Hold, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.views >= maxViews {
		return nil, false
	}

	h.views++

	hold := h.of(tab)
	hold.Opened(maxViews)

	return hold, true
}

// unopened takes back a view that did not open.
func (h *holds) unopened(hold *ownerhold.Hold) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.views--
	hold.Unopened()
}

// closed counts a view out: when it was its tab's last while the owner held
// the tab, the grace starts (ownerhold.Closed).
func (h *holds) closed(hold *ownerhold.Hold, now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.views--
	hold.Closed(now)
}

// held says, by tab, which tabs the owner holds.
func (h *holds) held() map[string]bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	held := map[string]bool{}

	for tab, hold := range h.all {
		if on, _ := hold.State(); on {
			held[tab] = true
		}
	}

	return held
}

// expire ends each hold that outlived its views or the owner's last input,
// and returns why by tab. A hold nothing uses, of a tab that is not open
// (open says which are), is dropped. The caller holds the engine's mu.
func (h *holds) expire(now time.Time, open func(tab string) bool) map[string]string {
	h.mu.Lock()
	defer h.mu.Unlock()

	ended := map[string]string{}

	for tab, hold := range h.all {
		if reason := hold.Expire(now); reason != "" {
			ended[tab] = reason
		}

		if hold.Unused() && !open(tab) {
			delete(h.all, tab)
		}
	}

	return ended
}
