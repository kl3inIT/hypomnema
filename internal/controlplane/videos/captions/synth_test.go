// Ported from demotape (https://github.com/gabosarmiento/demotape) at commit b268f1b,
// Tests/DemoTapeTests/CaptionStyleTests.swift: SynthesizedWordTimingTests (lines 138-197). MIT License, Copyright
// (c) 2026 DemoTape contributors; the permission notice is in synth.go.

package captions

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWordsArePackedAtTheStartOfAnOverlongCue(t *testing.T) {
	t.Parallel()

	words := SynthesizeWords("y el CTO lo aprobó.", 0, 10.8, CharactersPerSecond, 0)
	require.Len(t, words, 5)
	assert.Less(t, words[4].End, 3.0, "said in the first seconds, not stretched over ten")
	assert.InDelta(t, 0, words[0].Start, 0.001)
}

func TestWordByWordFloorKeepsShortWordsOnScreenLongEnough(t *testing.T) {
	t.Parallel()

	words := SynthesizeWords("y el CTO lo aprobó.", 0, 10.8, CharactersPerSecond, WordByWordMinSeconds)
	for _, w := range words {
		assert.GreaterOrEqual(t, w.End-w.Start, WordByWordMinSeconds-0.001, "%s too brief", w.Word)
	}

	assert.Less(t, words[len(words)-1].Start, 3.0)
}

func TestFloorScalesDownOnlyWhenTheCueIsGenuinelyTooShort(t *testing.T) {
	t.Parallel()

	words := SynthesizeWords("one two three four five", 0, 1.0, CharactersPerSecond, 0.42)
	assert.InDelta(t, 1.0, words[len(words)-1].End, 0.02)
	assert.InDelta(t, 0, words[0].Start, 0.001)
}

func TestEvenSpreadWhenTheSpeechFillsTheCue(t *testing.T) {
	t.Parallel()

	words := SynthesizeWords("this cue is completely full of spoken words already", 5, 8, CharactersPerSecond, 0)
	assert.InDelta(t, 8, words[len(words)-1].End, 0.05)
	assert.InDelta(t, 5, words[0].Start, 0.001)
}

func TestLongerWordsGetLongerOnScreen(t *testing.T) {
	t.Parallel()

	words := SynthesizeWords("a extraordinarily", 0, 10, CharactersPerSecond, 0)
	assert.Greater(t, words[1].End-words[1].Start, 3*(words[0].End-words[0].Start), "share follows word length")
}

func TestTimingsAreOrderedAndInsideTheCue(t *testing.T) {
	t.Parallel()

	words := SynthesizeWords("one two three four", 2, 4, CharactersPerSecond, 0)
	for k := 1; k < len(words); k++ {
		assert.LessOrEqual(t, words[k-1].End, words[k].Start+0.001)
	}

	assert.GreaterOrEqual(t, words[0].Start, 2.0)
	assert.LessOrEqual(t, words[len(words)-1].End, 4.001)
}

func TestEmptyTextProducesNoWords(t *testing.T) {
	t.Parallel()

	assert.Empty(t, SynthesizeWords("   ", 0, 1, CharactersPerSecond, 0))
}
