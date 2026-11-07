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
	// after 10 min).
	StallAfter = 15 * time.Minute
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
	// RateLimitCooldown: how long a slot cools down when a rate limit gives no
	// reset time (inferred).
	RateLimitCooldown = time.Minute
	// ExpiriesBeforeUnavailable: this many expiries in a row on one node
	// escalate node_unavailable (inferred).
	ExpiriesBeforeUnavailable = 3
	// ReceiptRetention and ReceiptKeep bound the receipts kept (Orca).
	ReceiptRetention = 30 * 24 * time.Hour
	ReceiptKeep      = 10000
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
