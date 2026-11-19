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
