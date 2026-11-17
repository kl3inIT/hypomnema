package meetings

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/meetingstore"
)

// What the notes must hold before they are kept, whatever the model wrote: MemoryOS's MinutesChecks
// (memoryos-core/src/main/java/io/memoryos/ai/MinutesChecks.java), which it enforces after measuring the model break
// each rule on 27 meetings. An owner is one person or nobody; a quote is said on its line or near it, else dropped;
// an item said twice is kept once; a decision that restates an action on its line is dropped.

// checkedItem is a decision or an action as the checks see it: Line 0 is none.
type checkedItem struct {
	Text  string
	Owner string
	Due   string
	Quote string
	Line  int
}

const (
	// sameLineOverlap: items of one kind on one line are one item when their words overlap this much.
	sameLineOverlap = 0.6
	// sameWordsOverlap: items of one kind anywhere are one item when their words overlap this much.
	sameWordsOverlap = 0.8
	minAbridgedWords = 3
)

// nearestFirst is where a quote is looked for around its cited line, up to two lines either side.
var nearestFirst = []int{0, -1, 1, -2, 2}

// label is how the transcript names a voice nobody named (MinutesChecks.LABEL, "Speaker N" here).
var label = regexp.MustCompile(`^(người nói|speaker) \S{1,16}$`)

// groupWords make an owner a group rather than a person (MinutesChecks.GROUP), in the languages notes are written in.
var groupWords = []string{
	"nhóm", "team", "đội", "phòng", "ban", "bộ phận", "các bạn", "các em", "các cháu", "các anh", "các chị",
	"bọn em", "bọn anh", "bọn mình", "chúng em", "chúng tôi", "chúng ta", "anh em", "mọi người", "cả hai",
	"tất cả", "đơn vị", "nhà thầu", "công ty", "everyone", "both", "we",
}

// severalWords join two people in one owner (MinutesChecks.SEVERAL).
var severalWords = []string{"và", "với", "and"}

// checkItems applies the checks to the decisions and actions in the order the model wrote them.
func checkItems(decisions, actions []checkedItem, lines []meetingstore.Line, speakers []string,
) ([]checkedItem, []checkedItem) {
	originals := make([]string, len(lines))
	texts := make([]string, len(lines))

	for i, l := range lines {
		originals[i] = l.Text
		texts[i] = bare(l.Text)
	}

	voices := map[string]bool{}
	for _, s := range speakers {
		voices[meetingstore.Fold(s)] = true
	}

	var keptActions []checkedItem

	for _, a := range actions {
		a.Quote, a.Line = cite(a.Quote, a.Line, originals, texts)
		a.Owner = owner(a.Owner, voices)

		twin := -1

		for i, k := range keptActions {
			if sameAction(k, a) {
				twin = i

				break
			}
		}

		if twin < 0 {
			keptActions = append(keptActions, a)
		} else {
			keptActions[twin] = merged(keptActions[twin], a)
		}
	}

	var keptDecisions []checkedItem

	for _, d := range decisions {
		d.Quote, d.Line = cite(d.Quote, d.Line, originals, texts)
		d.Owner, d.Due = "", ""

		if restatesAny(keptActions, d) {
			continue
		}

		twin := -1

		for i, k := range keptDecisions {
			if same(k.Text, k.Line, d.Text, d.Line) {
				twin = i

				break
			}
		}

		switch {
		case twin < 0:
			keptDecisions = append(keptDecisions, d)
		case keptDecisions[twin].Quote == "" && d.Quote != "":
			keptDecisions[twin].Quote, keptDecisions[twin].Line = d.Quote, d.Line
		}
	}

	return keptDecisions, keptActions
}

// sameAction: the same work, given to the same person or to nobody named.
func sameAction(kept, other checkedItem) bool {
	if kept.Owner != "" && other.Owner != "" && meetingstore.Fold(kept.Owner) != meetingstore.Fold(other.Owner) {
		return false
	}

	return same(kept.Text, kept.Line, other.Text, other.Line)
}

// merged is the first action's words, with what only the repeat said: its owner, its deadline, its quote and line.
func merged(kept, other checkedItem) checkedItem {
	ownCitation := kept.Quote != "" || other.Quote == ""
	cited, uncited := kept, other

	if !ownCitation {
		cited, uncited = other, kept
	}

	line := cited.Line
	if line <= 0 {
		line = uncited.Line
	}

	out := checkedItem{Text: kept.Text, Owner: kept.Owner, Due: kept.Due, Quote: cited.Quote, Line: line}
	if out.Owner == "" {
		out.Owner = other.Owner
	}

	if out.Due == "" {
		out.Due = other.Due
	}

	return out
}

func restatesAny(actions []checkedItem, d checkedItem) bool {
	for _, a := range actions {
		if restates(a, d) {
			return true
		}
	}

	return false
}

// restates: a decision resting on the sentence an action rests on, or saying the same thing on its line.
func restates(a, d checkedItem) bool {
	if a.Line <= 0 || a.Line != d.Line {
		return false
	}

	if a.Quote != "" && d.Quote != "" {
		x, y := bare(a.Quote), bare(d.Quote)
		if holds(x, y) || holds(y, x) {
			return true
		}
	}

	return overlap(a.Text, d.Text) >= sameLineOverlap
}

// owner is one person or nobody: a voice of the transcript is one person, named or not; a speaker label of no voice
// here, a group and several people are not an owner.
func owner(o string, voices map[string]bool) string {
	o = strings.TrimSpace(o)
	if o == "" {
		return ""
	}

	folded := meetingstore.Fold(o)
	if voices[folded] {
		return o
	}

	words := " " + bare(o) + " "

	if label.MatchString(folded) || strings.ContainsAny(folded, ",/&") {
		return ""
	}

	for _, w := range append(append([]string{}, groupWords...), severalWords...) {
		if strings.Contains(words, " "+w+" ") {
			return ""
		}
	}

	return o
}

// cite finds where a quote was said: its own line, a line up to two away, or two or three lines in a row starting
// there; the line's own words for a quote the model shortened. A quote nowhere near is dropped and the line kept; an
// item with no line takes the one place its quote matches, when there is exactly one.
func cite(quote string, line int, originals, texts []string) (string, int) {
	quote = strings.TrimSpace(quote)
	if quote == "" {
		return "", line
	}

	wanted := bare(quote)
	if wanted == "" {
		return "", line
	}

	count := len(texts)

	if line >= 1 && line <= count {
		if holds(texts[line-1], wanted) {
			return quote, line
		}

		for _, off := range nearestFirst {
			other := line + off
			if off != 0 && other >= 1 && other <= count && holds(texts[other-1], wanted) {
				return quote, other
			}
		}

		for _, off := range nearestFirst {
			for width := 2; width <= 3; width++ {
				start := line + off
				if start >= 1 && start+width-1 <= count &&
					holds(strings.Join(texts[start-1:start-1+width], " "), wanted) {
					return quote, start
				}
			}
		}

		for _, off := range nearestFirst {
			other := line + off
			if other < 1 || other > count {
				continue
			}

			if said, ok := abridged(quote, originals[other-1]); ok {
				return said, other
			}
		}

		return "", line
	}

	found := 0

	for other := 1; other <= count; other++ {
		if !holds(texts[other-1], wanted) {
			continue
		}

		if found > 0 {
			return "", line
		}

		found = other
	}

	if found > 0 {
		return quote, found
	}

	return "", line
}

type token struct {
	word       string
	start, end int
}

func tokens(s string) []token {
	var (
		out   []token
		start = -1
	)

	for i, r := range s {
		in := unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsMark(r)

		switch {
		case in && start < 0:
			start = i
		case !in && start >= 0:
			out = append(out, token{word: meetingstore.Fold(s[start:i]), start: start, end: i})
			start = -1
		}
	}

	if start >= 0 {
		out = append(out, token{word: meetingstore.Fold(s[start:]), start: start, end: len(s)})
	}

	return out
}

// abridged is the line's own words for a quote the model shortened: when every word of the quote is in the line in
// order, close together, the stretch of the line from its first to its last word.
func abridged(quote, line string) (string, bool) {
	wanted := tokens(quote)
	if len(wanted) < minAbridgedWords {
		return "", false
	}

	heard := tokens(line)
	from, to := -1, -1

	for first := range heard {
		if heard[first].word != wanted[0].word {
			continue
		}

		at, matched := first, 1

		for next := first + 1; next < len(heard) && matched < len(wanted); next++ {
			if heard[next].word == wanted[matched].word {
				matched++
				at = next
			}
		}

		if matched == len(wanted) && (from < 0 || at-first < to-from) {
			from, to = first, at
		}
	}

	if from < 0 || to-from+1 > max(len(wanted)*2, len(wanted)+8) {
		return "", false
	}

	return line[heard[from].start:heard[to].end], true
}

// holds says the words of wanted are in text as whole words; both bare.
func holds(text, wanted string) bool {
	return strings.Contains(" "+text+" ", " "+wanted+" ")
}

func same(text string, line int, otherText string, otherLine int) bool {
	o := overlap(text, otherText)

	return o >= sameWordsOverlap || (line > 0 && line == otherLine && o >= sameLineOverlap)
}

// overlap is the words two texts share over all their words.
func overlap(left, right string) float64 {
	a, b := words(left), words(right)
	if len(a) == 0 || len(b) == 0 {
		return 0
	}

	union := len(a)
	shared := 0

	for w := range b {
		if a[w] {
			shared++
		} else {
			union++
		}
	}

	return float64(shared) / float64(union)
}

func words(s string) map[string]bool {
	out := map[string]bool{}

	for _, w := range strings.Fields(bare(s)) {
		out[w] = true
	}

	return out
}

// bare is a text folded without punctuation, so a quote matches its line whatever the model did to commas and
// dashes (MinutesChecks.bare).
func bare(s string) string {
	return meetingstore.Fold(strings.Map(func(r rune) rune {
		if unicode.IsPunct(r) {
			return ' '
		}

		return r
	}, s))
}
