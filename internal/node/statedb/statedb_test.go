package statedb

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

func openDB(t *testing.T) *DB {
	t.Helper()

	db, err := Open(t.Context(), filepath.Join(t.TempDir(), "state.db"), time.Now)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	return db
}

// Scenario: an offer seen twice is recorded once with one accept; the accept
// and the final report wait in the outbox in order until acknowledged.
func TestOfferAcceptAndEndGoThroughTheOutbox(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db := openDB(t)
	offer := nodeproto.Offer{DispatchID: "dsp_1", Spec: "x", Worktree: "/srv/wt/r/t"}
	accept := nodeproto.Report{ReportID: "rep_1", Kind: nodeproto.ReportAccept, DispatchID: "dsp_1"}

	added, err := db.AddOffer(ctx, offer, accept)
	require.NoError(t, err)
	assert.True(t, added)

	added, err = db.AddOffer(ctx, offer, nodeproto.Report{ReportID: "rep_2", Kind: nodeproto.ReportAccept})
	require.NoError(t, err)
	assert.False(t, added, "an offer seen again queues nothing")

	require.NoError(t, db.Accept(ctx, "dsp_1", nodeproto.Accepted{Epoch: 1, CLISessionRef: "sess_1"}))
	require.NoError(t, db.SetProcess(ctx, "dsp_1", "pid:42@1"))
	require.NoError(t, db.End(ctx, "dsp_1", nodeproto.Report{ReportID: "rep_3", Kind: nodeproto.ReportFinished,
		DispatchID: "dsp_1", Epoch: 1}))

	d, err := db.Get(ctx, "dsp_1")
	require.NoError(t, err)
	assert.Equal(t, StateEnded, d.State)
	assert.Equal(t, 1, d.Epoch)
	assert.Equal(t, "pid:42@1", d.ProcessRef)
	assert.Equal(t, offer, d.Offer)
	require.NotNil(t, d.Accepted)
	assert.Equal(t, "sess_1", d.Accepted.CLISessionRef)

	pending, err := db.Pending(ctx)
	require.NoError(t, err)
	require.Len(t, pending, 2)
	assert.Equal(t, []string{"rep_1", "rep_3"}, []string{pending[0].ReportID, pending[1].ReportID})

	require.NoError(t, db.Done(ctx, "rep_1"))
	require.NoError(t, db.Remove(ctx, "dsp_1"))

	_, err = db.Get(ctx, "dsp_1")
	require.ErrorIs(t, err, ErrNotFound)

	_, err = db.Report(ctx, "rep_1")
	require.ErrorIs(t, err, ErrNotFound)

	r, err := db.Report(ctx, "rep_3")
	require.NoError(t, err)
	assert.Equal(t, nodeproto.ReportFinished, r.Kind)
}
