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
	// MaxFollowUpText bounds a follow-up's note (issue #666; inferred: a
	// review's findings pasted whole run longer than an answer, so it takes
	// the client API's bound of an answer's text, 20,000).
	MaxFollowUpText = 20000
	// EventPageDefault and EventPageMax bound one page of the event log (Omnara).
	EventPageDefault = 100
	EventPageMax     = 500
	// RunPageDefault and RunPageMax bound one page of Runs (Omnara's PageLimit).
	RunPageDefault = 50
	RunPageMax     = 100
	// BotGuaranteeDefault: a Bot's guaranteed share, in unsettled Dispatches
	// across every node, unless the Bot sets its own (issue #731; the Bot's
	// limit, BotDispatchesDefault, until 2026-11-17). The owner's Runs without
	// a Bot are one more group with this guarantee (inferred: no reference has
	// that case). From: Kueue's nominalQuota (clusterqueue_types.go:318-329),
	// YARN's capacity; 4 the owner's, 2026-11-16.
	BotGuaranteeDefault = 4
	// MaxDispatchesLimit bounds a node's optional cap and a Bot's guarantee, as
	// the client API bounds a new node's (api/openapi.yaml, NewNode).
	MaxDispatchesLimit = 64
	// DefaultCPURequestMillis and DefaultMemoryRequest: what a worker asks of a
	// node when its profile asks for nothing, sized for building work: the
	// repository's checks peaked at 2.7 GiB on stc-hy-app, plus a CLI; CPU is
	// elastic, so only the mean is asked (issue #731, measurements of
	// 2026-11-17). From: a Kubernetes container's requests
	// (helper/resource_fit.go:159-178) and a Nomad task's default resources
	// (nomad/structs/structs.go:2388-2394); 1 core and 3 GiB the owner's, 2026-11-17.
	DefaultCPURequestMillis = 1000
	DefaultMemoryRequest    = 3 << 30
	// NodeOfflineAfter: a node not heard from for this long is not connected
	// (Omnara, docs/agents/failure-handling.mdx).
	NodeOfflineAfter = 30 * time.Second
	// ScheduleEvery: the scheduler's pass when nothing wakes it (Orca DEFAULT_POLL_MS).
	ScheduleEvery = 2 * time.Second
	// NodeMemoryFloor: a node whose fresh stats show less memory available
	// than this takes no new Conversation (issue #731). From the kubelet's hard
	// eviction threshold memory.available < 100Mi, which taints the node
	// memory-pressure:NoSchedule (kubelet/eviction/defaults_linux.go:22-27);
	// 1 GiB, room for one worker CLI and its build, the owner's, 2026-11-17.
	NodeMemoryFloor = 1 << 30
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
