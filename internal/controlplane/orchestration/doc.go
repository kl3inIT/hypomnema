// Package orchestration is the control plane's record of Runs, Tasks and their
// dependencies, and of Dispatches (attempts at a Task) with their
// Conversations, receipts, effects and resources, and of questions and Gates;
// the mailbox and events follow (issue #19). It runs nothing. It is the only writer of this state:
// every change is a named operation in one BEGIN IMMEDIATE transaction, and
// every status change is checked against the transition tables in
// transitions.go. Timers and limits are in limits.go.
//
// The model is Orca's orchestration database
// (src/main/runtime/orchestration/db/); the named transitions checked against
// one table follow Coder's chat state machine (coderd/x/chatd/chatstate). The
// Dispatch lifecycle also draws on Omnara's machine-daemon reconciliation and
// Omnigent's handling of lost hosts (docs/network-resilience.md).
package orchestration
