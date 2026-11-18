package lark

import (
	"testing"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/channels"
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
	assert.Equal(t, []channels.Group{{ChatID: "oc_a", Name: "Maps team"}, {ChatID: "oc_b", Name: "Ops"}}, groups)

	pages[""] = page(true, "", chat("oc_a", "Maps team", "normal"))
	_, err = groupsOf(read)
	require.ErrorContains(t, err, "more follow", "a list that says more follow without a token is not whole")

	pages[""] = &larkim.ListChatResp{}
	_, err = groupsOf(read)
	require.ErrorContains(t, err, "no list")
}

// Scenario: a reply quotes a document that mentions everyone, and a Routine
// prints a mention into what the owner is told. Lark's mention tags are
// written out on every card, so that nobody is notified (MemoryOS,
// LarkReplyCard.inert).
func TestWhatTheBotSendsMentionsNobody(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "&lt;at id=all>&lt;/at> read this.", inert("<at id=all></at> read this."))
	assert.Equal(t, "&lt;Person id=x>", inert("<Person id=x>"), "any case")
	assert.Equal(t, "a <b>bold</b> line", inert("a <b>bold</b> line"), "other tags stay")

	raw, err := cardJSON(channels.Card{
		Title: "A routine keeps failing", Tone: channels.ToneAlert,
		Text: `Routine "depots" failed 2 times in a row. <at id=all>`, Note: "<at id=ou_x></at>",
	})
	require.NoError(t, err)
	assert.NotContains(t, raw, "<at", "what a routine printed mentions nobody")
	assert.Contains(t, raw, `"template":"red"`)
}

// A card's tone is one of Lark's header colors.
func TestACardsToneIsALarkColor(t *testing.T) {
	t.Parallel()

	for tone, color := range map[channels.Tone]string{
		channels.ToneAsking: "orange", channels.ToneAnswered: "green", channels.ToneAlert: "red",
		channels.ToneClosed: "grey", "": "blue",
	} {
		assert.Equal(t, color, colorOf(tone), tone)
	}
}

// The marks are the emoji OpenClaw and Hermes put on a message: Typing while
// the Bot works on it, a cross when it failed.
func TestTheMarksAreLarksEmoji(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "Typing", emoji[channels.MarkWorking])
	assert.Equal(t, "CrossMark", emoji[channels.MarkFailed])
}

// A file is filed by Lark under the kind its bytes are (issue #312): a
// workbook as xls, a "PDF" that is not one as stream.
func TestAFileIsFiledUnderTheKindItsBytesAre(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "xls", fileType("Báo cáo.xlsx", []byte("PK\x03\x04 five sheets")))
	assert.Equal(t, "stream", fileType("notes.pdf", []byte("plain text")))
	assert.Equal(t, "pdf", fileType("Report.PDF", []byte("%PDF-1.7")))
	assert.Equal(t, "stream", fileType("notes.txt", []byte("%PDF-1.7")), "a name alone makes nothing a PDF")
}

// Scenario (issue #723): a Lark voice message is read as its recording's
// file_key, which the Channels download and hear; a text message keeps its
// text and has no recording.
func TestAVoiceMessageIsReadAsItsRecordingsKey(t *testing.T) {
	t.Parallel()

	event := func(kind, content string) *larkim.P2MessageReceiveV1 {
		return &larkim.P2MessageReceiveV1{Event: &larkim.P2MessageReceiveV1Data{
			Message: &larkim.EventMessage{
				MessageId: ptr("om_1"), ChatId: ptr("oc_1"), ChatType: ptr("p2p"), MessageType: ptr(kind),
				Content: ptr(content),
			},
			Sender: &larkim.EventSender{SenderType: ptr("user"), SenderId: &larkim.UserId{OpenId: ptr("ou_1")}},
		}}
	}

	voice := messageOf(event("audio", `{"file_key":"file_v3_abc","duration":4000}`))
	assert.Equal(t, "file_v3_abc", voice.Voice)
	assert.False(t, voice.IsText)
	assert.Empty(t, voice.Text)

	text := messageOf(event("text", `{"text":"hello"}`))
	assert.Equal(t, "hello", text.Text)
	assert.True(t, text.IsText)
	assert.Empty(t, text.Voice)
}

// Scenario (issue #829): a post is read as its title and rows of text, with
// its mentions kept as the placeholders the message's mentions name, a link
// as a link, and each image and file in it, once, to download; a post by
// language reads the same. An image, a file and a video message are the one
// file each is, and a sticker is none.
func TestAPostIsReadAsItsTextAndItsFiles(t *testing.T) {
	t.Parallel()

	event := func(kind, content string) *larkim.P2MessageReceiveV1 {
		return &larkim.P2MessageReceiveV1{Event: &larkim.P2MessageReceiveV1Data{
			Message: &larkim.EventMessage{
				MessageId: ptr("om_1"), ChatId: ptr("oc_1"), ChatType: ptr("group"), MessageType: ptr(kind),
				Content: ptr(content),
				Mentions: []*larkim.MentionEvent{{
					Key: ptr("@_user_1"), Name: ptr("Hypomnema"), Id: &larkim.UserId{OpenId: ptr("ou_bot")},
				}},
			},
			Sender: &larkim.EventSender{SenderType: ptr("user"), SenderId: &larkim.UserId{OpenId: ptr("ou_1")}},
		}}
	}

	post := `{"title":"Đơn vị","content":[` +
		`[{"tag":"at","user_id":"@_user_1","user_name":"Hypomnema"},{"tag":"text","text":" của mình có theo "},` +
		`{"tag":"a","text":"đơn vị","href":"https://example.com/d"},{"tag":"text","text":" kiểu này chưa"}],` +
		`[{"tag":"img","image_key":"img_1"}],` +
		`[{"tag":"code","text":"x=1"},{"tag":"emotion","emoji_type":"SMILE"}],` +
		`[{"tag":"media","file_key":"file_v","file_name":"clip.mp4"},{"tag":"img","image_key":"img_1"}]],` +
		`"files":[{"file_key":"file_d","file_name":"Báo cáo.xlsx"},{"file_key":"dir_1","is_folder":true}]}`

	m := messageOf(event("post", post))
	assert.True(t, m.IsText)
	assert.Equal(t, "Đơn vị\n@_user_1 của mình có theo [đơn vị](https://example.com/d) kiểu này chưa\n`x=1`:SMILE:",
		m.Text)
	assert.Equal(t, []channels.File{
		{Key: "img_1", Image: true}, {Key: "file_v", Name: "clip.mp4"}, {Key: "file_d", Name: "Báo cáo.xlsx"},
	}, m.Files)
	assert.Equal(t, "@_user_1", m.Mentions[0].Key, "the mention the text holds is the one the message names")

	byLanguage := messageOf(event("post", `{"zh_cn":{"title":"","content":[[{"tag":"text","text":"xin chào"}]]}}`))
	assert.Equal(t, "xin chào", byLanguage.Text)

	image := messageOf(event("image", `{"image_key":"img_2"}`))
	assert.False(t, image.IsText)
	assert.Equal(t, []channels.File{{Key: "img_2", Image: true}}, image.Files)

	file := messageOf(event("file", `{"file_key":"file_2","file_name":"notes.pdf"}`))
	assert.Equal(t, []channels.File{{Key: "file_2", Name: "notes.pdf"}}, file.Files)

	video := messageOf(event("media", `{"file_key":"file_3","image_key":"img_cover","file_name":"a.mp4"}`))
	assert.Equal(t, []channels.File{{Key: "file_3", Name: "a.mp4"}}, video.Files)

	sticker := messageOf(event("sticker", `{"file_key":"stk_1"}`))
	assert.Empty(t, sticker.Files)
	assert.False(t, sticker.IsText)
}

func ptr(s string) *string { return &s }
