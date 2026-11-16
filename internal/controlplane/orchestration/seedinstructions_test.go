package orchestration

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Scenario: the starting profiles' instructions rewritten (issue #672). An
// implementer the owner applied keeps its own version while the seeds the
// owner left get the new text; going down takes the new versions back.
// (TestTheStartingSetIsThere checks the new versions on a fresh install.)
func TestMigrationGivesTheSeedsTheirNewInstructions(t *testing.T) {
	t.Parallel()

	_, db := migratedTo(t, 101)
	ctx := t.Context()
	putVersion(ctx, t, db, "pfv_owner", "implementer", "name: implementer", "claude-subscription",
		"claude-opus-5-5", "")

	p, err := provider(db)
	require.NoError(t, err)
	_, err = p.UpTo(ctx, 102)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"implementer": "pfv_owner", "reviewer": "pfv_seed5_reviewer", "explorer": "pfv_seed5_explorer",
	}, currentVersions(ctx, t, db), "the owner's version stays")

	var instruction string
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT instruction FROM profile_versions WHERE id = 'pfv_seed5_reviewer'`).Scan(&instruction))
	assert.Contains(t, instruction, "List every finding that meets these rules, not only the first")

	_, err = p.DownTo(ctx, 101)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"implementer": "pfv_owner", "reviewer": "pfv_seed4_reviewer", "explorer": "pfv_seed4_explorer",
	}, currentVersions(ctx, t, db))
}
