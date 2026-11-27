package githubapp_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/url"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/githubapp"
	"github.com/kl3inIT/hypomnema/internal/controlplane/githubapp/githubapptest"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
	"github.com/kl3inIT/hypomnema/internal/shared/ciwait"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

// registered is an App registered and installed on kl3inIT/hypomnema, over a migrated database at path. It is made
// inside the test's bubble, so its database's goroutines are the bubble's.
func registered(t *testing.T, path string, gh *githubapptest.GitHub) *githubapp.App {
	t.Helper()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: path})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	entry, err := vault.GenerateKey("k1")
	require.NoError(t, err)
	keys, err := vault.ParseKeyring(entry, "k1")
	require.NoError(t, err)

	svc := orchestration.New(db, platform.SystemClock{}, platform.NewID)
	secrets := vault.New(db, platform.SystemClock{}, platform.NewID, keys, svc.ProvidersUsingKeyRef)
	app := githubapp.New(svc, secrets, "https://hypomnema.example", gh.Client(), slog.New(slog.DiscardHandler))

	registration, _, err := app.StartRegistration()
	require.NoError(t, err)

	u, err := url.Parse(registration)
	require.NoError(t, err)
	require.NoError(t, app.CompleteRegistration(t.Context(), u.Query().Get("state"), "the-manifest-code"))
	require.NoError(t, app.SyncInstallations(t.Context()))

	return app
}

// Scenario (issue #1262): no event comes for a pull request someone waits for, as when GitHub did not send one or
// the control plane was down when it did. The wait reads the checks when it begins, then once five minutes later,
// which finds them done: it still finishes, having asked GitHub twice in all.
func TestWithNoEventTheWaitReadsTheChecksOnceInFiveMinutesAndStillFinishes(t *testing.T) {
	t.Parallel()

	path := sqlitetest.Migrated(t, orchestration.Migrate)
	gh := githubapptest.New(t)

	synctest.Test(t, func(t *testing.T) {
		app := registered(t, path, gh)
		pr := "https://github.com/kl3inIT/hypomnema/pull/1262"
		start := time.Now()

		ask := func(ctx context.Context, hold time.Duration) (ciwait.State, error) {
			st, err := app.WaitCI(ctx, pr, hold)
			if gh.Reads() == 1 && time.Since(start) >= 4*time.Minute {
				// The check ends while nobody tells the control plane.
				gh.Conclude("SUCCESS")
			}

			return st, err
		}

		var out bytes.Buffer

		st, err := ciwait.Wait(t.Context(), ask, time.Hour, &out)
		require.NoError(t, err)

		assert.Equal(t, "passed", st.State)
		assert.Equal(t, 2, gh.Reads(), "read when the wait began and once after five minutes without an event")
		assert.Equal(t, 5*time.Minute, time.Since(start), "read again five minutes after the first read")
		assert.Contains(t, out.String(), pr+"\tpending")
		assert.Contains(t, out.String(), pr+"\tpassed")
	})
}
