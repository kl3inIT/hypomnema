package controlplane

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

// zenReadings serves OpenCode Zen's union of Console lists, read anew at each step.
type zenReadings struct {
	names []string
	read  time.Time
}

func (r *zenReadings) ServedList(_ context.Context, runtime string) ([]string, time.Time, bool) {
	return r.names, r.read, runtime == orchestration.OpenCodeZenRuntime
}

// Scenario (review of PR #1000): a Bot uses the Zen model glm-5.3, and the workspaces' Console lists now name only a
// new model. The new model is added at once; after two readings without them, each model no list names is dropped
// on its own, and glm-5.3, which the Bot still uses, keeps only itself.
func TestAZenModelInUseKeepsOnlyItselfWhenItsListDropsIt(t *testing.T) {
	ctx := t.Context()

	db, err := sqlite.Open(ctx, sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	orch := orchestration.New(db, platform.SystemClock{}, platform.NewID)
	_, err = orch.PutBot(ctx, orchestration.Bot{Name: "zen", Provider: "opencode-zen", Model: "glm-5.3"})
	require.NoError(t, err)

	served := &zenReadings{names: []string{"new-paid-model"}, read: readAt}
	v := &vendorModels{subs: served, providers: orch, logger: slog.New(slog.DiscardHandler)}

	models := func() []string {
		p, err := orch.GetProvider(ctx, "opencode-zen")
		require.NoError(t, err)

		return p.Models
	}

	v.update(ctx)
	assert.Contains(t, models(), "new-paid-model", "a new model is added at once")
	assert.Contains(t, models(), "kimi-k3", "one reading without a model does not drop it")

	served.read = readAt.Add(time.Hour)
	v.update(ctx)
	assert.ElementsMatch(t, []string{"glm-5.3", "new-paid-model"}, models())
}
