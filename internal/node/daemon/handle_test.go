package daemon

import (
	"encoding/json"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/node/statedb"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

func TestFinishIsRetriedAfterDisconnect(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db, err := statedb.Open(ctx, filepath.Join(t.TempDir(), "state.db"), time.Now)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	d := New(Config{}, db, nil, slog.New(slog.DiscardHandler))
	offer := nodeproto.Offer{DispatchID: "dsp_1"}
	accept := nodeproto.Report{ReportID: "rep_accept", Kind: nodeproto.ReportAccept, DispatchID: offer.DispatchID}
	added, err := db.AddOffer(ctx, offer, accept)
	require.NoError(t, err)
	require.True(t, added)
	require.NoError(t, db.Accept(ctx, offer.DispatchID, nodeproto.Accepted{Epoch: 1}))
	require.NoError(t, db.Done(ctx, accept.ReportID))

	finished := nodeproto.Finished{Summary: "finished after Bash", Exit: nodeproto.Exit{Kind: "exited"}}
	require.NoError(t, d.worker(offer.DispatchID).Finished(ctx, finished))

	out := make(chan nodeproto.Message, 2)
	d.setOut(out)
	require.NoError(t, d.replay(ctx))
	require.NoError(t, d.replay(ctx))
	require.Len(t, out, 2)
	first := <-out
	assert.Equal(t, first, <-out)
	assert.Equal(t, nodeproto.TypeReport, first.Type)
	var report nodeproto.Report
	require.NoError(t, json.Unmarshal(first.Payload, &report))
	assert.Equal(t, nodeproto.ReportFinished, report.Kind)
	assert.Equal(t, offer.DispatchID, report.DispatchID)
	var payload nodeproto.Finished
	require.NoError(t, json.Unmarshal(report.Payload, &payload))
	assert.Equal(t, finished, payload)
	require.NoError(t, d.acked(ctx, nodeproto.ReportAck{ReportID: report.ReportID, Status: nodeproto.AckCommitted}))
	require.NoError(t, d.replay(ctx))
	assert.Empty(t, out)
}

// Scenario: review of PR #1228. A Dispatch offered by a control plane that keeps several decisions (issue #1219)
// reports after a reconnect to an earlier one, which keeps one question's text and labels. The capability is the
// connection's: on that connection its Gate and its question are sent folded into one that holds every decision,
// header and description; the outbox keeps them whole, and a control plane that says it keeps them is sent them so.
func TestAQuestionIsFoldedForAControlPlaneThatKeepsOne(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db, err := statedb.Open(ctx, filepath.Join(t.TempDir(), "state.db"), time.Now)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	d := New(Config{}, db, nil, slog.New(slog.DiscardHandler))
	offer := nodeproto.Offer{DispatchID: "dsp_1", QuestionParts: true}
	accept := nodeproto.Report{ReportID: "rep_accept", Kind: nodeproto.ReportAccept, DispatchID: offer.DispatchID}
	added, err := db.AddOffer(ctx, offer, accept)
	require.NoError(t, err)
	require.True(t, added)
	require.NoError(t, db.Accept(ctx, offer.DispatchID, nodeproto.Accepted{Epoch: 1}))
	require.NoError(t, db.Done(ctx, accept.ReportID))

	options := []nodeproto.Option{{Label: "Yes", Description: "Do it now."}, {Label: "Other", AllowsText: true}}
	gate := nodeproto.Ask{
		Header: "Items", Text: "One item each?", Options: options, To: "owner",
		More: []nodeproto.Part{{Header: "Server", Text: "Fix it now?", Options: options}},
	}
	finished := nodeproto.Finished{Summary: "planned", Gate: &gate, Exit: nodeproto.Exit{Kind: "exited"}}
	require.NoError(t, d.worker(offer.DispatchID).Finished(ctx, finished))

	sent := func(parts bool) nodeproto.Finished {
		t.Helper()

		d.mu.Lock()
		d.questionParts = parts
		d.mu.Unlock()

		out := make(chan nodeproto.Message, 1)
		d.setOut(out)
		require.NoError(t, d.replay(ctx))

		var report nodeproto.Report
		require.NoError(t, json.Unmarshal((<-out).Payload, &report))

		var payload nodeproto.Finished
		require.NoError(t, json.Unmarshal(report.Payload, &payload))

		return payload
	}

	old := sent(false)
	require.NotNil(t, old.Gate)
	assert.Equal(t, nodeproto.Ask{To: "owner", Text: "Answer each decision in turn:\n"+
		"1. Items: One item each?\n   - Yes: Do it now.\n   - Other\n"+
		"2. Server: Fix it now?\n   - Yes: Do it now.\n   - Other"}, *old.Gate, "every decision is in the text")
	assert.Equal(t, "planned", old.Summary)

	assert.Equal(t, finished, sent(true), "the outbox keeps the Gate whole")

	raw, err := json.Marshal(gate)
	require.NoError(t, err)

	var asked nodeproto.Ask
	folded := oneQuestionOf(nodeproto.Report{Kind: nodeproto.ReportQuestion, Payload: raw})
	require.NoError(t, json.Unmarshal(folded.Payload, &asked))
	assert.Equal(t, *old.Gate, asked, "a question the worker waits on is folded the same")
}

// Scenario: issue #1219. A question of one decision with a header and descriptions keeps its options for a control
// plane that keeps one question; its header and descriptions go in its text.
func TestOneDecisionKeepsItsOptionsWhenFolded(t *testing.T) {
	t.Parallel()

	got := nodeproto.Ask{
		Header: "Items", Text: "One item each?", To: "owner",
		Options: []nodeproto.Option{{Label: "Yes", Description: "Do it now."}, {Label: "Other", AllowsText: true}},
	}.OneQuestion()
	assert.Equal(t, nodeproto.Ask{
		Text: "Items: One item each?\n1. Yes: Do it now.", To: "owner",
		Options: []nodeproto.Option{{Label: "Yes"}, {Label: "Other", AllowsText: true}},
	}, got)
	assert.False(t, needsFolding(got))
}
