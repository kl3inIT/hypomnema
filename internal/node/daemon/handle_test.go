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
