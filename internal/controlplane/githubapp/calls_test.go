package githubapp_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kl3inIT/hypomnema/internal/controlplane/githubapp"
)

// slug is the registered App's handle (issue #1232's comment of 2026-11-28: the App is hypo-bot); the code reads it
// from the App's record, never a fixed word.
const slug = "hypo-bot"

// Scenario (issue #1232, row 1): a comment names the App only by its handle as a word, as Omnara's githubMentionsBot
// reads it; the request is the caller's words after it.
func TestAMentionIsTheAppsHandleAsAWord(t *testing.T) {
	t.Parallel()

	for body, want := range map[string]string{
		"@hypo-bot review PR này":              "review PR này",
		"Thanks! @Hypo-Bot, fix issue này\nok": "fix issue này\nok",
		"@hypo-bot[bot] review":                "review",
		"cc @hypo-bot.":                        "",
		"@hypo-bot":                            "",
	} {
		assert.True(t, githubapp.Mentions(body, slug), body)
		assert.Equal(t, want, githubapp.Request(body, slug), body)
	}

	for _, body := range []string{"@hypo-bot-old review", "mail me@hypo-bot.dev", "@hypo-bots review",
		"hypo-bot review", "@hypo-bot[x]"} {
		assert.False(t, githubapp.Mentions(body, slug), body)
		assert.Empty(t, githubapp.Request(body, slug), body)
	}
}

// Scenario (issue #1232's comment of 2026-11-28): only the App's own slug calls it. A shorter handle that is someone
// else's GitHub account, the slug without its suffix, is not a call, even beside text that reads like a request.
func TestOnlyTheAppsOwnSlugCallsIt(t *testing.T) {
	t.Parallel()

	assert.False(t, githubapp.Mentions("@hypo review", slug), "@hypo is another account, not the App")
	assert.Empty(t, githubapp.Request("@hypo review", slug))

	assert.True(t, githubapp.Mentions("@hypo-bot review", slug))
	assert.Equal(t, "review", githubapp.Request("@hypo-bot review", slug))

	assert.False(t, githubapp.Mentions("@hypo-bot review", ""), "an App without a slug is called by nothing")
}
