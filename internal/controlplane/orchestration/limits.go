package orchestration

import "time"

// Timers and limits of the Dispatch lifecycle (issue #19, PR 2 design). Where
// a value comes from a reference it is named; the others are inferred.
const (
	// QueueTimeout: a Dispatch no node accepted by then expires (inferred).
	QueueTimeout = 5 * time.Minute
	// LeaseDuration: a node renews its lease about every LeaseDuration/3
	// (Omnigent pings every 30 s and drops a host after three missed).
	LeaseDuration = 90 * time.Second
	// StopTimeout: a stop the node acknowledged but did not confirm by then
	// becomes stop_unknown (inferred).
	StopTimeout = 30 * time.Second
	// StallAfter: a running Dispatch with no progress for this long is
	// escalated, never killed (inferred; Coder cancels a silent model stream
	// after 10 min). Progress is a tool that succeeded, read from the
	// transcript, not any sign of life (issue #359, OpenMausBot's
	// okToolCalls); the timer is the backstop behind the two guards below.
	StallAfter = 15 * time.Minute
	// StalledErrors: a running Dispatch whose worker called the same tool
	// with the same input and got the same error this many times in a row is
	// stopped and ends failed (issue #359; OpenHands' action_error threshold
	// of 3, OpenMausBot's "3 identical errors").
	StalledErrors = 3
	// StalledAttempts: a Task whose last this many Dispatches each ended
	// failed with tool results and none of them a success fails for good
	// instead of being retried (issue #359; OpenMausBot's two continuations
	// without a successful tool call).
	StalledAttempts = 2
	// MaxDuration: a Dispatch running this long is escalated (inferred).
	MaxDuration = 4 * time.Hour
	// BreakerFailures: the counted failure that fails the Task (Orca
	// DISPATCH_CIRCUIT_BREAK_FAILURES).
	BreakerFailures = 3
	// BackoffBase and BackoffMax bound the wait before a retry: BackoffBase ×
	// 2^(n−1), at most BackoffMax, ±BackoffJitter (inferred).
	BackoffBase   = 30 * time.Second
	BackoffMax    = 10 * time.Minute
	BackoffJitter = 0.2
	// RateLimitCooldown: how long an account cools down when a rate limit gives
	// no reset time (inferred).
	RateLimitCooldown = time.Minute
	// ExpiriesBeforeUnavailable: this many expiries in a row on one node
	// escalate node_unavailable (inferred).
	ExpiriesBeforeUnavailable = 3
	// ReceiptRetention and ReceiptKeep bound the receipts kept (Orca).
	ReceiptRetention = 30 * 24 * time.Hour
	ReceiptKeep      = 10000
	// ItemRetention: a Dispatch's transcript items are dropped this long after it
	// settled (OpenClaw's pruneAfter of 30 days, applied to items; the Task,
	// its result and its evidence stay).
	ItemRetention = 30 * 24 * time.Hour
	// ReminderFirst and ReminderEvery: an open question to the owner is
	// escalated after ReminderFirst, then every ReminderEvery (inferred; no
	// reference reminds). A Gate never expires (Orca, Omnara).
	ReminderFirst = time.Hour
	ReminderEvery = 24 * time.Hour
	// MaxQuestionText, MaxAnswerText and MaxOptions bound a question and its
	// answer (inferred; Omnara caps an answer at 50 KB).
	MaxQuestionText = 4000
	MaxAnswerText   = 4000
	MaxOptions      = 10
	// EventPageDefault and EventPageMax bound one page of the event log (Omnara).
	EventPageDefault = 100
	EventPageMax     = 500
	// RunPageDefault and RunPageMax bound one page of Runs (Omnara's PageLimit).
	RunPageDefault = 50
	RunPageMax     = 100
	// MaxActiveDispatches: Dispatches that have not settled, across every node
	// (Orca MAX_CONCURRENT_DEFAULT; Coder admits inside the transaction).
	MaxActiveDispatches = 4
	// NodeDispatchesDefault: a node's own limit unless set (inferred).
	NodeDispatchesDefault = 2
	// NodeOfflineAfter: a node not heard from for this long is not connected
	// (Omnara, docs/agents/failure-handling.mdx).
	NodeOfflineAfter = 30 * time.Second
	// ScheduleEvery: the scheduler's pass when nothing wakes it (Orca DEFAULT_POLL_MS).
	ScheduleEvery = 2 * time.Second
	// UnplaceableAfter: a ready Task with no node or account for this long is
	// escalated (inferred).
	UnplaceableAfter = 5 * time.Minute
	// AccountCoolingAlert: a rate limit this long, with every other account of
	// the runtime also cooling, is escalated (inferred).
	AccountCoolingAlert = time.Hour
)

// backoff is the wait before the n-th retry; r is a random number in [0, 1).
func backoff(n int, r float64) time.Duration {
	d := BackoffBase
	for i := 1; i < n && d < BackoffMax; i++ {
		d *= 2
	}

	d = min(d, BackoffMax)

	return time.Duration(float64(d) * (1 - BackoffJitter + 2*BackoffJitter*r))
}
