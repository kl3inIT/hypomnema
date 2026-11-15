package browser

import "github.com/kl3inIT/hypomnema/internal/controlplane/ownerhold"

// Who drives the browser (issue #219): the agents, or the owner who took
// control. The profile is shared, so the owner holding any tab holds the
// whole browser; the rules are package ownerhold's, which the phone shares.

// The limits of the owner's hold (ownerhold).
const (
	TakeoverWait = ownerhold.TakeoverWait
	HoldGrace    = ownerhold.Grace
	HoldIdle     = ownerhold.Idle
)

// Refusal is what an agent's call is answered while the owner holds the
// browser.
const Refusal = "Browser tools are paused while the owner controls this browser. Wait for them to hand control " +
	"back; do not try another browser or another way to reach the page."
