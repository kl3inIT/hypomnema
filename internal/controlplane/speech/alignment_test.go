package speech_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"golang.org/x/text/unicode/norm"

	"github.com/kl3inIT/hypomnema/internal/controlplane/speech"
)

// spokenAs is the alignment of text as a provider speaks it: each character a tenth of a second.
func spokenAs(text string) speech.Alignment {
	var a speech.Alignment

	for i, c := range []rune(text) {
		at := float64(i) / 10
		a.Add(speech.Alignment{Characters: []string{string(c)}, Starts: []float64{at}, Ends: []float64{at + 0.1}})
	}

	return a
}

func words(ws []speech.Word) []string {
	out := make([]string, 0, len(ws))
	for _, w := range ws {
		out = append(out, w.Word)
	}

	return out
}

// The words of a Studio line are timed from the characters spoken (issue #1285, row 2): an audio tag is spoken
// but is no word, a stress mark is no character, the Vietnamese diacritics stay, and a word starts at its first
// character and ends at its last.
func TestTheWordsAreTimedFromTheCharacters(t *testing.T) {
	t.Parallel()

	line := "[excited] Ủa, bánh mì *giảm* rồi!"
	got := speech.AlignedWords(line, spokenAs(line))

	assert.Equal(t, []string{"Ủa,", "bánh", "mì", "giảm", "rồi!"}, words(got))
	assert.Equal(t, speech.Word{Word: "Ủa,", Start: time.Second, End: 1300 * time.Millisecond}, got[0])
}

// The characters are of the text as the provider normalized it; the words are told in the line's own spelling:
// "59%" spoken as words takes their time, a word written with combining marks still matches its composed
// characters, and a tag in the middle is dropped.
func TestTheWordsAreToldInTheLinesSpelling(t *testing.T) {
	t.Parallel()

	line := "Giảm 59% [laughs] " + norm.NFD.String("rẻ") + " hơn!"
	spoken := "Giảm năm mươi chín phần trăm [laughs] rẻ hơn!"
	got := speech.AlignedWords(line, spokenAs(spoken))

	assert.Equal(t, []string{"Giảm", "59%", norm.NFD.String("rẻ"), "hơn!"}, words(got))

	// "năm" starts at character 5 and "trăm" ends at character 28.
	assert.Equal(t, 500*time.Millisecond, got[1].Start)
	assert.Equal(t, 2800*time.Millisecond, got[1].End)
	assert.Equal(t, 3800*time.Millisecond, got[2].Start, "after the tag")
}

// The characters come in parts, as a stream gives them; nothing spoken is no word.
func TestTheCharactersComeInParts(t *testing.T) {
	t.Parallel()

	var a speech.Alignment

	a.Add(speech.Alignment{Characters: []string{"H", "i"}, Starts: []float64{0, 0.1}, Ends: []float64{0.1, 0.2}})
	a.Add(speech.Alignment{Characters: []string{" ", "y", "o", "u"}, Starts: []float64{0.2, 0.3, 0.4, 0.5},
		Ends: []float64{0.3, 0.4, 0.5, 0.6}})

	assert.Equal(t, []string{"Hi", "you"}, words(speech.AlignedWords("Hi you", a)))
	assert.Empty(t, speech.AlignedWords("Hi", speech.Alignment{}))
}
