package captions

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// timed spreads text's words over durSec by their length, as aidemo's test/issue-96.mjs timed() does.
func timed(text string, durSec float64) []Word {
	tokens := strings.Fields(text)
	total := 0
	for _, t := range tokens {
		total += utf8.RuneCountInString(t) + 1
	}

	at := 0.0
	out := make([]Word, 0, len(tokens))

	for _, t := range tokens {
		d := float64(utf8.RuneCountInString(t)+1) / float64(total) * durSec
		out = append(out, Word{Word: t, Start: at, End: at + d})
		at += d
	}

	return out
}

// Ported from aidemo test/issue-96.mjs, "space-delimited cues are unchanged without knownTerms": the cues give the
// text back whole, in order.
func TestCuesGiveTheTextBack(t *testing.T) {
	t.Parallel()

	text := "Sign in, open the billing page, and download the latest invoice. Then close the tab."
	cues := groupWords(timed(text, 6), Default)

	texts := make([]string, 0, len(cues))
	for _, c := range cues {
		texts = append(texts, c.Text)
	}

	assert.Equal(t, text, strings.Join(texts, " "))
	assert.True(t, strings.HasSuffix(cues[0].Text, "invoice."), "a sentence end is a hard break: %q", texts)
}

// The cap of 12 words (aidemo's knownTerms test, without the glossary): eleven short words then more are cut at
// the cap, and no cue has more than 12.
func TestCuesKeepToTwelveWords(t *testing.T) {
	t.Parallel()

	words := timed("one two three four five six seven eight nine ten eleven Messaging API key here", 5)
	cues := groupWords(words, Default)

	require.Greater(t, len(cues), 1)

	for _, c := range cues {
		assert.LessOrEqual(t, len(c.Words), 12, c.Text)
	}
}

func TestCuesBreakAtAClauseOrAConjunction(t *testing.T) {
	t.Parallel()

	// Vietnamese, two clauses: a comma, then "và", past what one cue holds.
	text := "Mở ứng dụng bản đồ trên điện thoại, chọn ô tìm kiếm ở phía trên màn hình và gõ tên " +
		"điểm đến mà bạn muốn đi tới hôm nay"
	cues := groupWords(timed(text, 9), Default)

	require.Greater(t, len(cues), 1)
	assert.True(t, strings.HasSuffix(cues[0].Text, ","), "the first break is at the comma: %q", cues[0].Text)

	for _, c := range cues {
		lines := Wrap(c.Text, Default.MaxCharsPerLine)
		assert.LessOrEqual(t, len(lines), 2, c.Text)

		for _, l := range lines {
			assert.LessOrEqual(t, utf8.RuneCountInString(l), 38, "a line of 38 characters at most, diacritics counted "+
				"as one: %q", l)
		}

		assert.LessOrEqual(t, c.EndMs-c.StartMs, int64(6000))
	}
}

func TestWrapBalancesTwoLines(t *testing.T) {
	t.Parallel()

	lines := Wrap("Chạm vào nút Đăng nhập ở góc phải để tiếp tục", 38)
	require.Len(t, lines, 2)
	assert.Equal(t, "Chạm vào nút Đăng nhập", lines[0])
	assert.Equal(t, "ở góc phải để tiếp tục", lines[1])
	assert.Equal(t, []string{"Xin chào"}, Wrap("Xin chào", 38))
}

// Words are moved to the video's time and kept within their scene; a cue never crosses a scene.
func TestBuildClampsWordsToTheirScene(t *testing.T) {
	t.Parallel()

	cues, err := Build([]Scene{
		{StartMs: 0, EndMs: 2000, Words: []Word{{"Một", 0.1, 0.5}, {"hai", 0.6, 3.5}}},
		{StartMs: 2000, EndMs: 3000, Words: []Word{{"ba.", -0.2, 0.4}}},
	}, Default)
	require.NoError(t, err)
	require.Len(t, cues, 2)

	assert.Equal(t, Cue{Index: 1, StartMs: 100, EndMs: 2000, Text: "Một hai",
		Words: []Word{{"Một", 0.1, 0.5}, {"hai", 0.6, 2}}}, cues[0])
	assert.Equal(t, int64(2000), cues[1].StartMs, "a word before its scene starts with it")
	assert.Equal(t, int64(2400), cues[1].EndMs)
	assert.Equal(t, 2, cues[1].Index)
}

func TestBuildFailsWithoutWordTimes(t *testing.T) {
	t.Parallel()

	_, err := Build([]Scene{{StartMs: 0, EndMs: 2000, Words: []Word{{"Một", 0, 1}}}, {StartMs: 2000, EndMs: 3000}},
		Default)
	require.ErrorIs(t, err, ErrNoWordTimes)
	assert.Contains(t, err.Error(), "scene 2")
}

func TestWordsJoinTokens(t *testing.T) {
	t.Parallel()

	// Soniox's tokens: a piece without a leading space is part of the word before it.
	got := Words([]Piece{{"Xin", 0, 0.2}, {" ch", 0.3, 0.4}, {"ào", 0.4, 0.6}, {",", 0.6, 0.6}, {" bạn", 0.7, 1}}, true)
	assert.Equal(t, []Word{{"Xin", 0, 0.2}, {"chào,", 0.3, 0.6}, {"bạn", 0.7, 1}}, got)

	// ElevenLabs's words are words; punctuation alone goes on the word before.
	got = Words([]Piece{{"Xin", 0, 0.2}, {"chào", 0.3, 0.6}, {"!", 0.6, 0.6}, {"2026", 0.7, 1}}, false)
	assert.Equal(t, []Word{{"Xin", 0, 0.2}, {"chào!", 0.3, 0.6}, {"2026", 0.7, 1}}, got)
}

// Ported from demotape Tests/DemoTapeTests/CaptionStyleTests.swift, WordTimingNormalizeTests.
func TestNormalize(t *testing.T) {
	t.Parallel()

	words := []Word{{"here's", 11.90, 12.08}, {"the", 12.08, 12.46}, {"agent", 12.46, 12.46}, {"itself", 12.46, 12.98}}
	out := Normalize(words, 0.42, 11.9, 20)
	require.Len(t, out, 4)

	for i, w := range out {
		assert.GreaterOrEqual(t, w.End-w.Start, 0.42-1e-6, w.Word)
		assert.Less(t, w.Start, w.End)

		if i > 0 {
			assert.LessOrEqual(t, out[i-1].End, w.Start+1e-6)
		}
	}

	for _, w := range Normalize([]Word{{"a", 0, 0}, {"b", 0, 0.02}}, HighlightMinSeconds, 0, 10) {
		assert.GreaterOrEqual(t, w.End-w.Start, HighlightMinSeconds-1e-6)
	}

	var six []Word
	for i := range 6 {
		six = append(six, Word{"w", float64(i) * 0.05, float64(i) * 0.05})
	}

	out = Normalize(six, 0.42, 0, 1)
	assert.LessOrEqual(t, out[5].End, 1+1e-6, "the run is scaled to stay inside the cue")
	assert.GreaterOrEqual(t, out[0].Start, 0.0)

	out = Normalize([]Word{{"one", 0, 1}, {"two", 2, 3}}, 0.42, 0, 5)
	assert.InDelta(t, 0, out[0].Start, 1e-6)
	assert.InDelta(t, 2, out[1].Start, 1e-6, "real onsets survive")
}

// The ASS file has an event for each stretch, the spoken word in the active color, and the lines broken with \N.
// event starts an ASS event's line.
const event = "Dialogue: " //nolint:misspell // ASS's own word.

func TestASSHighlightsTheSpokenWord(t *testing.T) {
	t.Parallel()

	cues, err := Build([]Scene{{StartMs: 1000, EndMs: 3000, Words: []Word{{"Xin", 0, 0.5}, {"chào", 0.6, 1.2}}}},
		Default)
	require.NoError(t, err)

	ass := ASS(cues, 720, 1280, Default)
	assert.Contains(t, ass, "PlayResX: 720\nPlayResY: 1280")
	assert.Contains(t, ass, "Style: Default,Be Vietnam Pro SemiBold,")
	assert.Contains(t, ass, event+`0,0:00:01.00,0:00:01.50,Default,,0,0,0,,{\c&H0000D4FF&}Xin{\c&H00FFFFFF&} chào`)
	assert.Contains(t, ass, event+`0,0:00:01.50,0:00:01.60,Default,,0,0,0,,Xin chào`)
	assert.Contains(t, ass, event+`0,0:00:01.60,0:00:02.20,Default,,0,0,0,,Xin {\c&H0000D4FF&}chào{\c&H00FFFFFF&}`)
	assert.NotContains(t, ass, "0:00:02.30", "the cue ends at its last word")
}
