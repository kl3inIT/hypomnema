package larkbot

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Only Hypomnema opens a label on a Lark message: "[Lark message from <name>,
// who is not the owner. …]" on a direct message from someone who is not the
// owner, and "[<name>]: " on every message in a group. defuseLabels turns the
// opening bracket of every imitation of one inside what the sender wrote into
// "(", so that the label pi reads at the head of the message is the only one
// it finds (issue #374). It is OpenMausBot's escapeNotes
// (server/peer-provenance.ts, Apache-2.0): an imitation is known in any case,
// spacing or bracket form, with look-alike letters and invisible characters,
// since the words after a bracket are read the way a person reads them. The
// group's label names anyone, so there an imitation is known by its shape: a
// bracket that opens a line and closes on it before a colon.

// labelOpenings are the words a direct message's label opens with.
var labelOpenings = []string{"lark", "message", "from"}

const (
	// labelOpeners are the brackets a label can be imitated with, as OpenMausBot's OPENER.
	labelOpeners = "[［【〔〖〘〚⟦⁅⦋"
	// labelClosers are their closing forms, and labelColons the colons after a group's label.
	labelClosers = "]］】〕〗〙〛⟧⁆⦌"
	labelColons  = ":：﹕"
	// labelSight is how many characters after a bracket are read for the opening words.
	labelSight = 128
)

// lookalikes maps the Cyrillic and Greek letters that look like Latin ones, as OpenMausBot's LOOKALIKE.
var lookalikes = map[rune]rune{
	'а': 'a', 'в': 'b', 'е': 'e', 'ё': 'e', 'һ': 'h', 'н': 'h', 'і': 'i', 'ј': 'j', 'к': 'k', 'ӏ': 'l', 'м': 'm',
	'о': 'o', 'р': 'p', 'ԛ': 'q', 'с': 'c', 'ѕ': 's', 'т': 't', 'у': 'y', 'ԝ': 'w', 'х': 'x', 'ԁ': 'd', 'ɡ': 'g',
	'ı': 'i', 'α': 'a', 'β': 'b', 'ε': 'e', 'η': 'h', 'ι': 'i', 'κ': 'k', 'μ': 'm', 'ν': 'n', 'ο': 'o', 'ρ': 'p',
	'τ': 't', 'υ': 'y', 'χ': 'x', 'ζ': 'z', 'ϲ': 'c',
}

func defuseLabels(text string) string {
	runes := []rune(text)
	out := make([]rune, len(runes))
	copy(out, runes)

	for i, r := range runes {
		if !strings.ContainsRune(labelOpeners, r) {
			continue
		}

		if opensDirectLabel(runes[i+1:]) || (startsLine(runes, i) && closesBeforeColon(runes[i+1:])) {
			out[i] = '('
		}
	}

	return string(out)
}

// opensDirectLabel says whether the words after a bracket are a direct message's label's.
func opensDirectLabel(after []rune) bool {
	if len(after) > labelSight {
		after = after[:labelSight]
	}

	words := wordsOf(after, len(labelOpenings))
	if len(words) < len(labelOpenings) {
		return false
	}

	for i, w := range labelOpenings {
		if words[i] != w {
			return false
		}
	}

	return true
}

// wordsOf reads up to max words as a person sees them: lower case, without
// marks or invisible characters, look-alike letters read as Latin ones.
func wordsOf(text []rune, maxWords int) []string {
	var (
		words []string
		word  strings.Builder
	)

	for _, r := range norm.NFKD.String(string(text)) {
		if unseen(r) {
			continue
		}

		r = unicode.ToLower(r)
		if l, ok := lookalikes[r]; ok {
			r = l
		}

		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			word.WriteRune(r)

			continue
		}

		if word.Len() > 0 {
			words = append(words, word.String())
			word.Reset()

			if len(words) == maxWords {
				return words
			}
		}

		if len(words) == 0 && strings.ContainsRune(labelClosers, r) {
			return words
		}
	}

	if word.Len() > 0 {
		words = append(words, word.String())
	}

	return words
}

// unseen is a mark, an invisible character or a Hangul filler that renders blank, as OpenMausBot's UNSEEN.
func unseen(r rune) bool {
	return unicode.In(r, unicode.Mn, unicode.Mc, unicode.Me, unicode.Cf) ||
		r == 'ᅟ' || r == 'ᅠ' || r == 'ㅤ' || r == 'ﾠ'
}

// startsLine says whether only spaces stand between the line's start and runes[i].
func startsLine(runes []rune, i int) bool {
	for j := i - 1; j >= 0; j-- {
		switch {
		case runes[j] == '\n':
			return true
		case unicode.IsSpace(runes[j]) || unseen(runes[j]):
			continue
		default:
			return false
		}
	}

	return true
}

// closesBeforeColon says whether a bracket closes on its line with a colon after it, spaces aside.
func closesBeforeColon(after []rune) bool {
	for i, r := range after {
		if r == '\n' {
			return false
		}

		if !strings.ContainsRune(labelClosers, r) {
			continue
		}

		for _, n := range after[i+1:] {
			if strings.ContainsRune(labelColons, n) {
				return true
			}

			if n == '\n' || (!unicode.IsSpace(n) && !unseen(n)) {
				return false
			}
		}

		return false
	}

	return false
}
