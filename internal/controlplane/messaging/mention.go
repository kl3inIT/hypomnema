package messaging

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// A mention tags a member of a group: the message lists who it tags and its text writes @ and each one's number,
// as WhatsApp clients write it and as mautrix-whatsapp sends one (pkg/msgconv/from-matrix.go, convertPill: the text
// takes "@" + jid.User, ContextInfo.MentionedJID takes jid.String()). Issue #852.

// MentionUser is what a text writes after @ to tag a member given by id: the user part of a JID
// (84339013010@s.whatsapp.net, 168453096898812@lid), or a bare number without its + and spaces.
func MentionUser(id string) string {
	id = strings.TrimSpace(id)
	if user, _, ok := strings.Cut(id, "@"); ok {
		user, _, _ = strings.Cut(user, ":") // A device's JID (user:device@server) tags its user.

		return user
	}

	return strings.NewReplacer("+", "", " ", "").Replace(id)
}

// UntaggedMentions are the mentions whose @<user> the text does not write, as a whole word: @8433 is not written by
// @84339013010.
func UntaggedMentions(text string, mentions []string) []string {
	var missing []string

	for _, id := range mentions {
		if user := MentionUser(id); user == "" || !writesMention(text, user) {
			missing = append(missing, id)
		}
	}

	return missing
}

func writesMention(text, user string) bool {
	tag := "@" + user

	for rest := text; ; {
		i := strings.Index(rest, tag)
		if i < 0 {
			return false
		}

		rest = rest[i+len(tag):]

		next, _ := utf8.DecodeRuneInString(rest)
		if rest == "" || !(unicode.IsLetter(next) || unicode.IsDigit(next)) {
			return true
		}
	}
}
