package clientapi

import (
	"math"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// Scenario: a long chat is read from its end a page at a time (issue #502): the newest page first, then each
// older one until the first message, and the stream follows from the newest seq read.
func TestAChatIsReadFromItsEndAPageAtATime(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	owner, _ := w.token("web", bearertoken.Owner)
	ctx := t.Context()

	chat, err := w.svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	var seqs []int64

	for _, text := range []string{"one", "two", "three", "four", "five"} {
		m, err := w.svc.AddChatMessage(ctx, chat.ID, orchestration.ChatOwner, text)
		require.NoError(t, err)

		seqs = append(seqs, m.Seq)
	}

	texts := func(page *openapi.ChatMessageList) []string {
		var out []string
		for _, m := range page.Data {
			out = append(out, m.Text)
		}

		return out
	}
	limit := 2
	before := int64(math.MaxInt64)

	var got [][]string

	for {
		page, err := owner.ListChatMessagesWithResponse(ctx, chat.ID,
			&openapi.ListChatMessagesParams{Before: &before, Limit: &limit})
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, page.StatusCode(), string(page.Body))

		if len(got) == 0 {
			assert.Equal(t, seqs[4], page.JSON200.NextAfter, "the stream follows from the newest seq read")
		}

		got = append(got, texts(page.JSON200))

		if !page.JSON200.HasMore {
			break
		}

		require.NotNil(t, page.JSON200.NextBefore)
		before = *page.JSON200.NextBefore
	}

	assert.Equal(t, [][]string{{"four", "five"}, {"two", "three"}, {"one"}}, got, "each page oldest first")
}
