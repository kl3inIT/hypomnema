package clientcli

import (
	"io/fs"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// coordinatorText is every file of the coordinator skill, each with its whitespace folded to single spaces so that a
// phrase is found across the files' line wraps.
func coordinatorText(t *testing.T) map[string]string {
	t.Helper()

	files := map[string]string{}
	walk := func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		raw, err := ownSkills.ReadFile(name)
		files[name] = strings.Join(strings.Fields(string(raw)), " ")

		return err
	}
	require.NoError(t, fs.WalkDir(ownSkills, "skills/hypomnema-coordinator", walk))
	require.NotEmpty(t, files)

	return files
}

// Scenario (issue #1051): each review rule has one home. The coordinator's skill, printed into its turns, holds no
// source comments and does not restate the reviewer profile's rules; it holds the coordinator's own two rules once
// each, and docs/role-routing.md names their sources.
func TestTheCoordinatorSkillHoldsEachReviewRuleOnce(t *testing.T) {
	t.Parallel()

	files := coordinatorText(t)

	for name, text := range files {
		assert.NotContains(t, text, "<!--", "%s: sources live in docs/role-routing.md", name)

		for _, reviewers := range []string{
			"proof at the head",
			"supported use",
			"P2 and P3 never block",
			"`Evidence: <level>`",
			"`coderabbitai[bot]`",
		} {
			assert.NotContains(t, text, reviewers, "%s restates the reviewer profile's rule", name)
		}
	}

	all := strings.Join(slices.Collect(maps.Values(files)), "\n")

	for _, once := range []string{
		"Copy an owner's acceptance condition into the spec word for word, as one line.",
		"Never restate it as a general property for the reviewer to hunt",
		"a `make migration-fix` rename of the change's migrations or wording needs green CI only, no new review.",
		"Do not restate the reviewer's rules in the spec",
	} {
		assert.Equal(t, 1, strings.Count(all, once), once)
	}

	raw, err := os.ReadFile("../../../docs/role-routing.md")
	require.NoError(t, err)

	for rule, from := range map[string]string{
		`"Copy an owner's acceptance condition into the spec …"`: "pstack skills/poteto-mode/worker.md:4",
		`"Size the review to the change. Round 1 …"`:             "departs from pstack's single verifier for a rebase",
		`"Do not restate the reviewer's rules in …"`:             "references/handoffs.md:130-140",
	} {
		row := sourceRow(string(raw), rule)
		assert.Contains(t, row, from, rule)
	}
}

func sourceRow(doc, rule string) string {
	for line := range strings.SplitSeq(doc, "\n") {
		if strings.Contains(line, rule) {
			return line
		}
	}

	return ""
}
