package speech

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Words timed from a speech provider's per-character times, for Studio's captions (issue #1285, row 2), built on the
// one splitter both providers share, alignmentWords (elevenlabs.go, issue #1283, row 3): the characters cut into words
// at whitespace, a bracketed audio tag dropped and splitting the words beside it, a word from its first character's
// start to its last's end. Soniox's characters are of the text as it normalized it, with no map back to the input
// (docs.soniox.com /tts/rt/timestamps, "Preprocessed text"), so its words are then matched back to the line's own:
// a longest common subsequence of the words compared by their letters and digits, lower-cased and composed (NFC),
// so that a Vietnamese word written with combining marks still matches; a run of the line's words left between two
// matched ones, such as "59%" spoken as "năm mươi chín phần trăm", shares the time of the spoken words between the
// same two, by its letters. The matching is Hypomnema's own: no reference maps the normalized words back, and the
// issue's row 2 asks for the line's spelling.

// Alignment is the time of each character a provider spoke, in seconds from the start of its audio.
type Alignment struct {
	Characters []string
	Starts     []float64
	Ends       []float64
}

// Add appends a part of the alignment, as a provider streams it.
func (a *Alignment) Add(part Alignment) {
	n := min(len(part.Characters), len(part.Starts), len(part.Ends))
	a.Characters = append(a.Characters, part.Characters[:n]...)
	a.Starts = append(a.Starts, part.Starts[:n]...)
	a.Ends = append(a.Ends, part.Ends[:n]...)
}

// AlignedWords are the words of text, without its audio tags and stresses, each timed from the characters spoken;
// none when nothing was spoken.
func AlignedWords(text string, a Alignment) []Word {
	spoken := alignmentWords(a.Characters, a.Starts, a.Ends)
	if len(spoken) == 0 {
		return nil
	}

	line := lineWords(text)
	if len(line) == 0 {
		return spoken
	}

	return matchWords(line, spoken)
}

// lineWords are the words of a line as it was written: its tags and stresses left out.
func lineWords(text string) []string {
	var (
		out   []string
		clean strings.Builder
		depth int
	)

	for _, r := range text {
		switch {
		case r == '[':
			depth++
		case r == ']' && depth > 0:
			depth--

			clean.WriteRune(' ')
		case depth > 0, r == '*':
		default:
			clean.WriteRune(r)
		}
	}

	out = append(out, strings.Fields(clean.String())...)

	return glue(out)
}

// glue puts a word that has no letter or digit, such as a dash, on the word before it, or the one after when it
// is first, so that every word can be matched.
func glue(words []string) []string {
	var out []string

	pending := ""

	for _, w := range words {
		switch {
		case key(w) != "" && pending != "":
			out = append(out, pending+" "+w)
			pending = ""
		case key(w) != "":
			out = append(out, w)
		case len(out) > 0:
			out[len(out)-1] += " " + w
		default:
			pending = strings.TrimSpace(pending + " " + w)
		}
	}

	if pending != "" {
		out = append(out, pending)
	}

	return out
}

// key is a word compared: its letters and digits, composed and lower-cased.
func key(w string) string {
	var b strings.Builder

	for _, r := range norm.NFC.String(w) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}

	return b.String()
}

// matchWords times the line's words from the spoken ones.
func matchWords(line []string, spoken []Word) []Word {
	pairs := commonWords(line, spoken)

	out := make([]Word, 0, len(line))
	li, si := 0, 0

	// spread gives the line's words from li up to lEnd the time of the spoken words from si up to sEnd, or, when
	// none was spoken between, the moment between the matched words around them.
	spread := func(lEnd, sEnd int) {
		if li >= lEnd {
			return
		}

		var from, to float64

		switch {
		case si < sEnd:
			from, to = spoken[si].Start.Seconds(), spoken[sEnd-1].End.Seconds()
		case si > 0:
			from = spoken[si-1].End.Seconds()
			to = from

			if si < len(spoken) {
				to = max(from, spoken[si].Start.Seconds())
			}
		default:
			from = spoken[0].Start.Seconds()
			to = from
		}

		total := 0
		for _, w := range line[li:lEnd] {
			total += max(1, len([]rune(key(w))))
		}

		at := from

		for _, w := range line[li:lEnd] {
			share := (to - from) * float64(max(1, len([]rune(key(w))))) / float64(total)
			out = append(out, Word{Word: w, Start: seconds(at), End: seconds(at + share)})
			at += share
		}
	}

	for _, p := range pairs {
		spread(p[0], p[1])
		out = append(out, Word{Word: line[p[0]], Start: spoken[p[1]].Start, End: spoken[p[1]].End})
		li, si = p[0]+1, p[1]+1
	}

	spread(len(line), len(spoken))

	return out
}

// commonWords is a longest common subsequence of the line's words and the spoken ones, compared by key: the pairs
// of their places, in order.
func commonWords(line []string, spoken []Word) [][2]int {
	lk := make([]string, len(line))
	for i, w := range line {
		lk[i] = key(w)
	}

	sk := make([]string, len(spoken))
	for j, w := range spoken {
		sk[j] = key(w.Word)
	}

	// lengths[i][j] is the length of the longest common subsequence of lk[i:] and sk[j:].
	lengths := make([][]int, len(lk)+1)
	for i := range lengths {
		lengths[i] = make([]int, len(sk)+1)
	}

	for i := len(lk) - 1; i >= 0; i-- {
		for j := len(sk) - 1; j >= 0; j-- {
			if lk[i] != "" && lk[i] == sk[j] {
				lengths[i][j] = lengths[i+1][j+1] + 1
			} else {
				lengths[i][j] = max(lengths[i+1][j], lengths[i][j+1])
			}
		}
	}

	var pairs [][2]int

	for i, j := 0, 0; i < len(lk) && j < len(sk); {
		switch {
		case lk[i] != "" && lk[i] == sk[j]:
			pairs = append(pairs, [2]int{i, j})
			i++
			j++
		case lengths[i+1][j] >= lengths[i][j+1]:
			i++
		default:
			j++
		}
	}

	return pairs
}
