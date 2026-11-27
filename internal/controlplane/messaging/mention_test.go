package messaging_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kl3inIT/hypomnema/internal/controlplane/messaging"
)

// A member is tagged where the text writes @ and their id's number as a whole word, whether the id is a number's
// JID, a LID or a bare number (issue #852).
func TestAMentionIsTaggedOnlyWhereTheTextWritesIt(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		text, id string
		tagged   bool
	}{
		{"@84339013010 xem nhé", "84339013010@s.whatsapp.net", true},
		{"anh @168453096898812, xem nhé", "168453096898812@lid", true},
		{"cc @84339013010", "+84 339013010", true},
		{"cc @84339013010", "84339013010:12@s.whatsapp.net", true},
		{"Lan xem nhé", "84339013010@s.whatsapp.net", false},
		{"@8433901301099 xem nhé", "84339013010@s.whatsapp.net", false},
		{"84339013010 xem nhé", "84339013010@s.whatsapp.net", false},
		{"@ xem", "", false},
	} {
		missing := messaging.UntaggedMentions(tc.text, []string{tc.id})
		assert.Equal(t, tc.tagged, len(missing) == 0, "%q tags %q", tc.text, tc.id)
	}
}

// Scenario: issue #1200. A draft tags a member where it writes "@" and a name he goes by in the chat: the longest
// name first, in any case, ending where a word ends, so "@Duy" is not found inside "@Duyên" and "@Duy Tran" is
// Duy Tran, not Duy. A name nobody, or two people, go by stays text, never a shorter name's tag; an address's "@"
// tags nobody.
func TestADraftTagsOnlyTheOnePersonANameIs(t *testing.T) {
	t.Parallel()

	people := []messaging.Named{
		{ID: "u-lan", Names: []string{"Lan Anh"}},
		{ID: "u-tuan", Names: []string{"Anh Tuấn"}},
		{ID: "u-minh", Names: []string{"Anh Minh"}},
		{ID: "u-duy", Names: []string{"Duy"}},
		{ID: "u-duytran", Names: []string{"Duy Tran", "duy tran"}},
		{ID: "u-ha1", Names: []string{"Hà"}},
		{ID: "u-ha2", Names: []string{"Hà"}},
		{ID: "u-hai", Names: []string{"Hà Linh"}},
		{ID: "u-hai2", Names: []string{"Hà Linh"}},
		{Names: []string{"Nobody"}},
	}

	for _, tc := range []struct {
		text string
		want []messaging.Tag
	}{
		{"@Lan Anh dạ vâng chị", []messaging.Tag{{ID: "u-lan", At: 0, Len: 8, Name: "Lan Anh"}}},
		{"dạ @lan anh, em gửi", []messaging.Tag{{ID: "u-lan", At: 5, Len: 8, Name: "lan anh"}}},
		{"@Anh dạ", nil},
		{"@Duyên ơi", nil},
		{"@Duy ơi", []messaging.Tag{{ID: "u-duy", At: 0, Len: 4, Name: "Duy"}}},
		{"@Duy Tran và @Duy", []messaging.Tag{
			{ID: "u-duytran", At: 0, Len: 9, Name: "Duy Tran"}, {ID: "u-duy", At: 14, Len: 4, Name: "Duy"},
		}},
		{"@Hà ơi", nil},
		{"@Hà Linh ơi", nil},
		{"mail lan@Lan Anh", nil},
		{"@Nobody", nil},
		{"Lan Anh dạ", nil},
	} {
		assert.Equal(t, tc.want, messaging.FindTags(tc.text, people), "%q", tc.text)
	}
}
