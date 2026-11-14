package larkbot_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/artifactpack"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// Scenario: on Lark the owner asks for a dashboard. Lark shows no page, so the Bot's reply after it names the
// artifact with a link to it on the web, before the link to the chat (issue #402, row 12); the reply after that
// names nothing. A bracket in the title does not break the link.
func TestAReplyOnLarkLinksTheArtifactBeforeIt(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()
	w.web = "https://hyp.example/"
	w.start()
	w.setUp(orchestration.LarkChannelOwner)

	w.lark.say(direct("om_1", ownerDM, ownerID, "a dashboard of this week's sales"))
	chat := w.chatOf(ownerDM)

	c, err := w.orch.GetChat(ctx, chat)
	require.NoError(t, err)

	p, err := artifactpack.Check([]artifactpack.File{{Path: "index.html", Data: []byte("<p>sales")}})
	require.NoError(t, err)

	a, _, err := w.orch.ShowArtifact(ctx, orchestration.ArtifactMaker{Bot: c.Bot, Chat: chat},
		orchestration.ShowArtifact{Title: "Sales [week 46]", Pack: p})
	require.NoError(t, err)

	link := "\n\n[Open in Hypomnema](https://hyp.example/chats/" + chat + ")"

	w.answer(chat, "Here it is.")
	w.answer(chat, "Anything else?")
	require.Eventually(t, func() bool {
		return assert.ObjectsAreEqual([]string{
			ownerDM + ": Here it is.\n\n📎 [Sales (week 46)](https://hyp.example/artifacts/" + a.ID + ")" + link,
			ownerDM + ": Anything else?" + link,
		}, w.lark.sent())
	}, eventual, tick)
}
