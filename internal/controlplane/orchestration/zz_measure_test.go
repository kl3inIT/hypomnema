package orchestration

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

// A measurement, not a test to keep: what one fresh, migrated database costs.
func TestMeasureMigrate(t *testing.T) {
	for i := range 5 {
		start := time.Now()

		db, err := sqlite.Open(t.Context(), sqlite.Config{Path: filepath.Join(t.TempDir(), "m.db")})
		require.NoError(t, err)

		opened := time.Since(start)

		require.NoError(t, Migrate(t.Context(), db))
		t.Logf("MEASURE run %d: open %s, migrate %s", i, opened.Round(time.Millisecond),
			(time.Since(start) - opened).Round(time.Millisecond))
		require.NoError(t, db.Close())
	}
}
