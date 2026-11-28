package githubapp_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kl3inIT/hypomnema/internal/controlplane/githubapp"
)

// slug is a made-up App's handle; the code reads the real one from the App's record, never a fixed word.
const slug = "acme-bot"

// Scenario (issue #1232, rows 1 and 4): a comment names the App only by its handle as a word, as Omnara's
// githubMentionsBot reads it; the request is the caller's words after it on that line only (review round 1 of PR
// #1323).
func TestAMentionIsTheAppsHandleAsAWord(t *testing.T) {
	t.Parallel()

	for body, want := range map[string]string{
		"@acme-bot review PR này":               "review PR này",
		"Thanks! @Acme-Bot, fix issue này\nok":  "fix issue này",
		"context\n@acme-bot review\n\nmerge it": "review",
		"@acme-bot review\rmerge it":            "review",
		"@acme-bot review\r\nmerge it":          "review",
		"@acme-bot[bot] review":                 "review",
		"cc @acme-bot.":                         "",
		"@acme-bot":                             "",
	} {
		assert.True(t, githubapp.Mentions(body, slug), body)
		assert.Equal(t, want, githubapp.Request(body, slug), body)
	}

	for _, body := range []string{"@acme-bot-old review", "mail me@acme-bot.dev", "@acme-bots review",
		"acme-bot review", "@acme-bot[x]"} {
		assert.False(t, githubapp.Mentions(body, slug), body)
		assert.Empty(t, githubapp.Request(body, slug), body)
	}
}

// Scenario (issue #1232's comment of 2026-11-28): only the App's own slug calls it. A shorter handle that is someone
// else's GitHub account, the slug without its suffix, is not a call, even beside text that reads like a request.
func TestOnlyTheAppsOwnSlugCallsIt(t *testing.T) {
	t.Parallel()

	assert.False(t, githubapp.Mentions("@acme review", slug), "the slug without -bot is another account")
	assert.Empty(t, githubapp.Request("@acme review", slug))

	assert.True(t, githubapp.Mentions("@acme-bot review", slug))
	assert.Equal(t, "review", githubapp.Request("@acme-bot review", slug))

	assert.False(t, githubapp.Mentions("@acme-bot review", ""), "an App without a slug is called by nothing")
}
