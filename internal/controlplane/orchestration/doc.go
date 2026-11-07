// Package orchestration is the control plane's record of Runs, Tasks and their
// dependencies; Dispatches, Gates, the mailbox and events follow (issue #19).
// It runs nothing. It is the only writer of this state: every change is a
// named operation in one BEGIN IMMEDIATE transaction, and every status change
// is checked against the transition table in transitions.go.
//
// The model is Orca's orchestration database
// (src/main/runtime/orchestration/db/); the named transitions checked against
// one table follow Coder's chat state machine (coderd/x/chatd/chatstate).
package orchestration
