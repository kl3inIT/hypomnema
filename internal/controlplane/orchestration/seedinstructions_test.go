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

// Scenario: the seeded reviewer runs the repository's checks (issue #718). A
// reviewer the owner applied keeps its own version; the seed the owner left
// gets the new text, the same version ApplyProfile makes from that YAML;
// going down takes it back.
func TestMigrationTellsTheSeededReviewerToRunTheChecks(t *testing.T) {
	t.Parallel()

	for _, owned := range []bool{false, true} {
		_, db := migratedTo(t, 107)
		ctx := t.Context()

		want := "pfv_seed6_reviewer"
		if owned {
			putVersion(ctx, t, db, "pfv_owner", "reviewer", "name: reviewer", "claude-subscription",
				"claude-opus-5-5", "high")

			want = "pfv_owner"
		}

		p, err := provider(db)
		require.NoError(t, err)
		_, err = p.UpTo(ctx, 108)
		require.NoError(t, err)
		assert.Equal(t, want, currentVersions(ctx, t, db)["reviewer"], "owned: %v", owned)

		if owned {
			continue
		}

		var source, digest, instruction string
		require.NoError(t, db.QueryRowContext(ctx,
			`SELECT source, digest, instruction FROM profile_versions WHERE id = 'pfv_seed6_reviewer'`).
			Scan(&source, &digest, &instruction))
		assert.Contains(t, instruction, "Run the repository's checks on the pull request's code")
		assert.Contains(t, instruction, "say in the report which one and why")
		assert.Equal(t, sourceDigest(source), digest, "what ApplyProfile writes for that YAML")

		_, err = p.DownTo(ctx, 107)
		require.NoError(t, err)
		assert.Equal(t, "pfv_seed5_reviewer", currentVersions(ctx, t, db)["reviewer"])
	}
}
