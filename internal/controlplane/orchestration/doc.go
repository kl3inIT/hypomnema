// Package orchestration is the control plane's record of Runs, Tasks and their
// dependencies, and of Dispatches (attempts at a Task) with their
// Conversations, receipts, effects and resources, of questions and Gates, the
// event log, and nodes, accounts and the scheduler that places ready Tasks on
// them (issue #19). It runs no worker itself. It is the only writer of this state:
// every change is a named operation in one BEGIN IMMEDIATE transaction, and
// every status change is checked against the transition tables in
// transitions.go. Timers and limits are in limits.go.
//
// The model is Orca's orchestration database
// (src/main/runtime/orchestration/db/); the named transitions checked against
// one table follow Coder's chat state machine (coderd/x/chatd/chatstate). The
// Dispatch lifecycle also draws on Omnara's machine-daemon reconciliation and
// Omnigent's handling of lost hosts (docs/network-resilience.md). The
// scheduler follows Orca's coordinator tick and Coder's concurrent limiter
// (coderd/x/chatd/ARCHITECTURE.md); node registration and liveness follow
// Omnara's machines (docs/machines/connect.mdx, docs/agents/failure-handling.mdx).
package orchestration
