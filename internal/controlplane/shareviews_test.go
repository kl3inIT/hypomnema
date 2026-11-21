package controlplane

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
	"github.com/kl3inIT/hypomnema/internal/shared/artifactpack"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

// Scenario (issue #996, review round 3 of #999): a share link is opened and then left. Its opening is written by
// the next tick, with no later opening to carry it; one more opening just before the control plane stops is written
// as it stops, before the next tick.
func TestTheViewsOfALinkAreWrittenWithoutALaterOpeningAndAsItStops(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	db, err := sqlite.Open(ctx, sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	keyring, err := vault.ParseKeyring(keygen(t, "test"), "test")
	require.NoError(t, err)

	svc := orchestration.New(db, platform.SystemClock{}, platform.NewID)
	svc.WithShareVault(vault.New(db, platform.SystemClock{}, platform.NewID, keyring, nil).Copies())

	chat, err := svc.CreateChat(ctx, "", "")
	require.NoError(t, err)
	pack, err := artifactpack.Check([]artifactpack.File{{Path: "index.html", Data: []byte("<p>sales")}})
	require.NoError(t, err)
	a, _, err := svc.ShowArtifact(ctx, orchestration.ArtifactMaker{Bot: chat.Bot, Chat: chat.ID},
		orchestration.ShowArtifact{Title: "Sales", Pack: pack})
	require.NoError(t, err)
	sh, err := svc.CreateArtifactShare(ctx, a.ID, "Laura")
	require.NoError(t, err)

	stored := func() int64 {
		var views int64
		if err := db.QueryRowContext(ctx, "SELECT views FROM artifact_shares WHERE id = ?", sh.ID).
			Scan(&views); err != nil {
			return -1
		}

		return views
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	loopCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})

	go func() {
		defer close(done)

		writeShareViews(loopCtx, svc, logger, 20*time.Millisecond)
	}()

	svc.CountArtifactShareView(sh.ID)
	assert.Eventually(t, func() bool { return stored() == 1 }, 5*time.Second, 10*time.Millisecond,
		"written by the tick")

	// The last opening before the stop; a tick an hour away is not what writes it.
	stop()
	<-done

	again, stopAgain := context.WithCancel(ctx)
	doneAgain := make(chan struct{})

	go func() {
		defer close(doneAgain)

		writeShareViews(again, svc, logger, time.Hour)
	}()

	svc.CountArtifactShareView(sh.ID)
	stopAgain()
	<-doneAgain

	assert.Equal(t, int64(2), stored(), "written as it stops")
}
