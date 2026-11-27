package orchestration

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/profilestore"
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

// Scenario: the seeded reviewer's instruction rewritten in place: 00111
// tells it to answer CodeRabbit's findings (issue #766), and 00115, on top of
// that, to run the repository's checks (issue #718). In both, a reviewer the
// owner applied keeps its own version while the seed the owner left gets the
// new text, the same version ApplyProfile makes from that YAML; going down
// takes it back. (The sample YAML now carries 00115's text.)
func TestMigrationTellsTheSeededReviewerWhatChanged(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		before   int64
		after    int64
		seed     string
		previous string
		added    []string
		kept     []string
	}{
		{
			name: "answer CodeRabbit's findings", before: 110, after: 111, seed: "pfv_seed7_reviewer",
			previous: "pfv_seed5_reviewer",
			added: []string{
				"gh api repos/<repo>/pulls/<n>/comments --paginate",
				"still open (then it is a P1 finding of yours)",
			},
			kept: []string{"List every finding that meets these rules"},
		},
		{
			name: "run the repository's checks", before: 114, after: 115, seed: "pfv_seed8_reviewer",
			previous: "pfv_seed7_reviewer",
			added: []string{
				"Run the repository's checks on the pull request's code",
				"say in the report which one and why",
			},
			kept: []string{"still open (then it is a P1 finding of yours)"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			for _, owned := range []bool{false, true} {
				_, db := migratedTo(t, tc.before)
				ctx := t.Context()

				want := tc.seed
				if owned {
					putVersion(ctx, t, db, "pfv_owner", "reviewer", "name: reviewer", "claude-subscription",
						"claude-opus-5-5", "high")

					want = "pfv_owner"
				}

				p, err := provider(db)
				require.NoError(t, err)
				_, err = p.UpTo(ctx, tc.after)
				require.NoError(t, err)
				assert.Equal(t, want, currentVersions(ctx, t, db)["reviewer"], "owned: %v", owned)

				if owned {
					continue
				}

				var source, digest, instruction string
				require.NoError(t, db.QueryRowContext(ctx,
					`SELECT source, digest, instruction FROM profile_versions WHERE id = ?`, tc.seed).
					Scan(&source, &digest, &instruction))

				for _, text := range tc.added {
					assert.Contains(t, instruction, text)
				}

				for _, text := range tc.kept {
					assert.Contains(t, instruction, text)
				}

				assert.Equal(t, profilestore.SourceDigest(source), digest, "what ApplyProfile writes for that YAML")

				seeded, err := profilestore.DecodeSource(source)
				require.NoError(t, err)
				assert.Equal(t, seeded.Instruction, instruction, "the instruction is the source's")

				_, err = p.DownTo(ctx, tc.before)
				require.NoError(t, err)
				assert.Equal(t, tc.previous, currentVersions(ctx, t, db)["reviewer"])
			}
		})
	}
}
