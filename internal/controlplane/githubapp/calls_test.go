package githubapp_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kl3inIT/hypomnema/internal/controlplane/githubapp"
)

// Scenario (issue #1232, row 1): a comment names the App only by its handle as a word, as Omnara's githubMentionsBot
// reads it; the request is the caller's words after it.
func TestAMentionIsTheAppsHandleAsAWord(t *testing.T) {
	t.Parallel()

	for body, want := range map[string]string{
		"@hypo review PR này":              "review PR này",
		"Thanks! @Hypo, fix issue này\nok": "fix issue này\nok",
		"@hypo[bot] review":                "review",
		"cc @hypo.":                        "",
		"@hypo":                            "",
	} {
		assert.True(t, githubapp.Mentions(body, "hypo"), body)
		assert.Equal(t, want, githubapp.Request(body, "hypo"), body)
	}

	for _, body := range []string{"@hypo-old review", "mail me@hypo.dev", "@hypomnema review", "hypo review",
		"@hypo[x]"} {
		assert.False(t, githubapp.Mentions(body, "hypo"), body)
		assert.Empty(t, githubapp.Request(body, "hypo"), body)
	}
}
