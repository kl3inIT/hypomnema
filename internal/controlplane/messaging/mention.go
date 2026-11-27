package messaging

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
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

// Tagging a member by name (issue #1200): a text written for a group names a member as "@" and his name as the chat
// shows it ("@Lan Anh dạ…"), and the tag is found as OpenClaw's Zalo plugin finds one (monas-team/zaloclaw
// src/parsing/mention-parser.ts, parseOutboundMentions, the bare "@Name" form): an "@" that does not follow a letter
// or a digit (an address's "a@b" tags nobody), then the longest member's name, in any case, that is not followed
// by a letter or a digit, so that "@Duy" is not found inside "@Duyên" and "@Duy Tran" wins over "@Duy" (the Hermes
// Zalo plugin's _build_outbound_mentions, adapter.py). A name two people of the chat go by tags neither: zaloclaw
// keeps only the names one member has, and here the "@" stays text rather than falling to a shorter name, since a
// tag guessed notifies the wrong person.

// Named is a person of a chat a text may tag by name: the id he is tagged by and the names the chat gives him.
type Named struct {
	ID    string
	Names []string
}

// Tag is a member a text tags by name: his id, and where the text writes "@" and his name, as a byte offset and
// length in the text; Name is the name as the text writes it.
type Tag struct {
	ID   string
	At   int
	Len  int
	Name string
}

// FindTags finds where a text tags the people of a chat by their names. A name no one or two people go by stays
// text.
func FindTags(text string, people []Named) []Tag {
	if !strings.Contains(text, "@") {
		return nil
	}

	names := tagNames(people)
	if len(names) == 0 {
		return nil
	}

	var tags []Tag

	for i := 0; i < len(text); {
		if text[i] != '@' {
			i++

			continue
		}

		if prev, _ := utf8.DecodeLastRuneInString(text[:i]); i > 0 && isNameRune(prev) {
			i++

			continue
		}

		n, ok := longestName(text[i+1:], names)
		if !ok || n.id == "" {
			i++

			continue
		}

		written := text[i+1 : i+1+n.bytes]
		tags = append(tags, Tag{ID: n.id, At: i, Len: 1 + n.bytes, Name: written})
		i += 1 + n.bytes
	}

	return tags
}

// tagName is a name some people of a chat go by: its runes, and the id of the one person who does, empty when two
// do.
type tagName struct {
	name  string
	runes int
	id    string
	// bytes is, once matched, how long the text's own writing of it is.
	bytes int
}

// tagNames are the names of a chat's people, the longest first, each with the one person who goes by it.
func tagNames(people []Named) []tagName {
	byKey := map[string]int{}
	owner := map[string]int{}

	var names []tagName

	for p, person := range people {
		if person.ID == "" {
			continue
		}

		for _, name := range person.Names {
			name = norm.NFC.String(strings.TrimSpace(name))
			if name == "" {
				continue
			}

			key := strings.ToLower(name)

			i, seen := byKey[key]
			if !seen {
				byKey[key], owner[key] = len(names), p
				names = append(names, tagName{name: name, runes: utf8.RuneCountInString(name), id: person.ID})

				continue
			}

			if owner[key] != p {
				names[i].id = ""
			}
		}
	}

	sort.SliceStable(names, func(a, b int) bool { return names[a].runes > names[b].runes })

	return names
}

// longestName is the longest name rest begins with, in any case and followed by no letter or digit.
func longestName(rest string, names []tagName) (tagName, bool) {
	for _, n := range names {
		end, ok := runeOffset(rest, n.runes)
		if !ok || !strings.EqualFold(rest[:end], n.name) {
			continue
		}

		if next, _ := utf8.DecodeRuneInString(rest[end:]); end < len(rest) && isNameRune(next) {
			continue
		}

		n.bytes = end

		return n, true
	}

	return tagName{}, false
}

// runeOffset is the byte offset of the n-th rune of s, false when s has fewer.
func runeOffset(s string, n int) (int, bool) {
	at := 0
	for range n {
		if at >= len(s) {
			return 0, false
		}

		_, size := utf8.DecodeRuneInString(s[at:])
		at += size
	}

	return at, true
}

// isNameRune is a rune that goes on a name: a letter, a digit, a combining mark or "_" (zaloclaw's isWordChar,
// [\p{L}\p{N}_], with the marks a decomposed Vietnamese letter has).
func isNameRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) || r == '_'
}
