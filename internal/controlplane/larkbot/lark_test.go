package larkbot

import (
	"testing"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// page is one page of Lark's list of the bot's chats.
func page(more bool, token string, chats ...*larkim.ListChat) *larkim.ListChatResp {
	return &larkim.ListChatResp{Data: &larkim.ListChatRespData{Items: chats, HasMore: &more, PageToken: &token}}
}

func chat(id, name, status string) *larkim.ListChat {
	return &larkim.ListChat{ChatId: &id, Name: &name, ChatStatus: &status}
}

// The bot's groups are read page by page; a group no longer in use is left
// out. A list Lark did not give whole is no list, so the roster is not
// changed by it (issue #246, as MemoryOS reads them).
func TestTheBotsGroupsAreAWholeListOrNone(t *testing.T) {
	t.Parallel()

	pages := map[string]*larkim.ListChatResp{
		"":   page(true, "p2", chat("oc_a", " Maps team ", "normal"), chat("oc_gone", "Old", "dissolved")),
		"p2": page(false, "", chat("oc_b", "Ops", "")),
	}
	read := func(token string) (*larkim.ListChatResp, error) { return pages[token], nil }

	groups, err := groupsOf(read)
	require.NoError(t, err)
	assert.Equal(t, []Group{{ChatID: "oc_a", Name: "Maps team"}, {ChatID: "oc_b", Name: "Ops"}}, groups)

	pages[""] = page(true, "", chat("oc_a", "Maps team", "normal"))
	_, err = groupsOf(read)
	require.ErrorContains(t, err, "more follow", "a list that says more follow without a token is not whole")

	pages[""] = &larkim.ListChatResp{}
	_, err = groupsOf(read)
	require.ErrorContains(t, err, "no list")
}
