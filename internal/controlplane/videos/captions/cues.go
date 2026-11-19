// Ported from aidemo (https://github.com/tandryukha/aidemo) at commit b67235a, src/captions.ts: DEFAULT_SEG (line
// 50), CONJUNCTIONS (line 61), cueText (line 588), wordUnits (line 598), endsClause (line 604), endsSentence (line
// 608), splitRun (line 687), mergeShort (line 735) and groupWords (line 751). Issue #818, row 6.
//
// What differs, and why: the words of one scene are grouped at a time, transcribed from that scene's own clip, so
// a cue never crosses a scene (aidemo's groupWords cuts runs by a word's start and takes a cue's end from its last
// word, which may cross); the CJK splitting and knownTerms are left out (the voice is Vietnamese, spaced by words,
// and no glossary is given); a run fits only when it wraps into maxLines lines of maxCharsPerLine (wrap), where
// aidemo leaves the wrap to the browser that draws its captions; and Vietnamese conjunctions join the list, as
// aidemo lists each language's.
//
// MIT License
//
// Copyright (c) 2026 Andrii Taran
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.

// Package captions makes a video's captions from the words its voice says
// (issue #818, row 6): each scene's words, timed on the video, grouped into
// cues by aidemo's rules, and written as an ASS file that libass burns in,
// the word being spoken highlighted as demotape does.
package captions

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Word is one word said, its times in seconds on the video.
type Word struct {
	Word  string
	Start float64
	End   float64
}

// Cue is one caption: the words shown together, from StartMs to EndMs on the video.
type Cue struct {
	Index   int
	StartMs int64
	EndMs   int64
	Text    string
	Words   []Word
}

// Config is how cues are cut (aidemo's SegConfig).
type Config struct {
	MaxWords        int
	MaxCueMs        float64
	MinCueMs        float64
	MaxLines        int
	MaxCharsPerLine int
}

// Default is aidemo's DEFAULT_SEG: about 2 lines of 38 characters, 1-6 s a cue, at most 12 words.
var Default = Config{MaxWords: 12, MaxCueMs: 6000, MinCueMs: 1000, MaxLines: 2, MaxCharsPerLine: 38}

// conjunctions make a decent break point, by language: aidemo's, and Vietnamese's.
var conjunctions = map[string]bool{}

func init() {
	for _, w := range []string{
		// en
		"and", "but", "so", "or", "then", "because", "while", "when", "which", "that", "if",
		// et
		"ja", "aga", "või", "siis", "kui", "sest", "mis", "ning", "kuid",
		// de / nl / sv
		"und", "aber", "oder", "dann", "weil", "wenn", "och", "men", "eller", "en", "maar",
		// fr / es / it / pt
		"et", "mais", "ou", "donc", "parce", "quand", "y", "pero", "o", "entonces", "porque",
		"e", "ma", "oppure", "quindi", "perché", "mas", "então",
		// vi
		"và", "nhưng", "hoặc", "rồi", "vì", "khi", "nếu", "thì", "mà", "để", "nên",
	} {
		conjunctions[w] = true
	}
}

// ErrNoWordTimes reports a scene whose transcript has no word with its times.
var ErrNoWordTimes = errors.New("the transcript has no word times")

// Scene is one scene's words, timed from the start of its own clip, and
// where the scene is on the video.
type Scene struct {
	StartMs, EndMs int64
	Words          []Word
}

// Build is the cues of a video: each scene's words moved to the video's time
// and kept within their scene, grouped scene by scene, so that no cue
// crosses a scene. A scene with no word fails with ErrNoWordTimes.
func Build(scenes []Scene, cfg Config) ([]Cue, error) {
	var out []Cue

	for k, sc := range scenes {
		from, to := float64(sc.StartMs)/1000, float64(sc.EndMs)/1000

		var timed []Word

		for _, w := range sc.Words {
			if strings.TrimSpace(w.Word) == "" {
				continue
			}

			start := clamp(from+w.Start, from, to)
			end := clamp(from+w.End, start, to)
			timed = append(timed, Word{Word: w.Word, Start: start, End: end})
		}

		if len(timed) == 0 {
			return nil, fmt.Errorf("scene %d: %w", k+1, ErrNoWordTimes)
		}

		for _, c := range groupWords(timed, cfg) {
			c.Index = len(out) + 1
			out = append(out, c)
		}
	}

	return out, nil
}

func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(v, hi)) }

// groupWords is aidemo's: sentence ends are hard breaks, and inside those runs
// the split is clause-aware. Its words are one scene's.
func groupWords(words []Word, cfg Config) []Cue {
	var (
		runs [][]Word
		cur  []Word
	)

	cut := func() {
		if len(cur) > 0 {
			runs = append(runs, cur)
		}

		cur = nil
	}

	for _, w := range words {
		cur = append(cur, w)
		if endsSentence(w) {
			cut()
		}
	}

	cut()

	var split [][]Word
	for _, r := range runs {
		split = append(split, splitRun(r, cfg)...)
	}

	merged := mergeShort(split, cfg)
	out := make([]Cue, 0, len(merged))

	for i, ws := range merged {
		out = append(out, Cue{
			Index:   i + 1,
			StartMs: int64(math.Round(ws[0].Start * 1000)),
			EndMs:   int64(math.Round(ws[len(ws)-1].End * 1000)),
			Text:    cueText(ws),
			Words:   ws,
		})
	}

	return out
}

// fits says whether a run is one cue: few enough words, short enough, and
// its text wraps into the lines a cue has.
func fits(run []Word, cfg Config) bool {
	text := cueText(run)

	return len(run) <= cfg.MaxWords &&
		utf8.RuneCountInString(text) <= cfg.MaxLines*cfg.MaxCharsPerLine &&
		len(Wrap(text, cfg.MaxCharsPerLine)) <= cfg.MaxLines &&
		(run[len(run)-1].End-run[0].Start)*1000 <= cfg.MaxCueMs
}

// splitRun splits a run of words into cues at the most readable boundaries:
// sentence ends first, then clause punctuation, then a conjunction, and only
// then on pure length. Never leaves a 2-3 word tail dangling when the
// previous boundary can absorb it.
func splitRun(ws []Word, cfg Config) [][]Word {
	if len(ws) == 0 {
		return nil
	}

	if fits(ws, cfg) {
		return [][]Word{ws}
	}

	// The furthest index that still fits, then walk back to a real boundary.
	end := 1
	for end < len(ws) && fits(ws[:end+1], cfg) {
		end++
	}

	floor := max(1, int(math.Ceil(float64(end)*0.45)))
	cut := end

	for i := end - 1; i >= floor; i-- {
		if endsSentence(ws[i]) || endsClause(ws[i]) {
			cut = i + 1

			break
		}
	}

	if cut == end {
		for i := end - 1; i >= floor; i-- {
			if i+1 < len(ws) && conjunctions[letters(ws[i+1].Word)] {
				cut = i + 1

				break
			}
		}
	}

	return append([][]Word{ws[:cut]}, splitRun(ws[cut:], cfg)...)
}

// mergeShort merges a too-short cue into the neighbor it still fits in.
func mergeShort(runs [][]Word, cfg Config) [][]Word {
	var out [][]Word

	for _, run := range runs {
		durMs := (run[len(run)-1].End - run[0].Start) * 1000

		if n := len(out); durMs < cfg.MinCueMs && n > 0 {
			both := append(append([]Word(nil), out[n-1]...), run...)
			if fits(both, cfg) {
				out[n-1] = both

				continue
			}
		}

		out = append(out, run)
	}

	return out
}

var spaceBeforePunct = regexp.MustCompile(`\s+([,.!?;:])`)

// cueText is a run's text, punctuation attached again.
func cueText(ws []Word) string {
	pieces := make([]string, 0, len(ws))
	for _, w := range ws {
		pieces = append(pieces, strings.TrimSpace(w.Word))
	}

	return strings.TrimSpace(spaceBeforePunct.ReplaceAllString(strings.Join(pieces, " "), "$1"))
}

// endsClause says whether a word ends a clause (punctuation): the best place to break.
func endsClause(w Word) bool {
	r, _ := utf8.DecodeLastRuneInString(strings.TrimSpace(w.Word))

	return strings.ContainsRune(",;:—–，、；：", r)
}

var sentenceEnd = regexp.MustCompile(`[.!?。！？]["'”’)」』）]?$`)

func endsSentence(w Word) bool { return sentenceEnd.MatchString(strings.TrimSpace(w.Word)) }

// letters is a word lowercased with only its letters, as a conjunction is looked up.
func letters(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) {
			return unicode.ToLower(r)
		}

		return -1
	}, strings.TrimSpace(s))
}

// Wrap breaks a cue's text into lines of at most width characters at its
// spaces, as evenly as it can: the fewest lines, then the longest line the
// shortest. A word longer than width is a line of its own.
func Wrap(text string, width int) []string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return nil
	}

	for lines := 1; lines <= len(words); lines++ {
		if best := balanced(words, lines, width); best != nil {
			return best
		}
	}

	return words
}

// balanced splits words into n lines of at most width, the longest line the
// shortest it can be; nil when they do not fit.
func balanced(words []string, n, width int) []string {
	if n == 1 {
		line := strings.Join(words, " ")
		if utf8.RuneCountInString(line) <= width {
			return []string{line}
		}

		return nil
	}

	var (
		best    []string
		longest = math.MaxInt
	)

	for cut := 1; cut <= len(words)-n+1; cut++ {
		first := strings.Join(words[:cut], " ")
		if utf8.RuneCountInString(first) > width {
			break
		}

		rest := balanced(words[cut:], n-1, width)
		if rest == nil {
			continue
		}

		l := utf8.RuneCountInString(first)
		for _, r := range rest {
			l = max(l, utf8.RuneCountInString(r))
		}

		if l < longest {
			best, longest = append([]string{first}, rest...), l
		}
	}

	return best
}

// Piece is a piece of a transcript with its times in seconds: a word, or, from
// a provider that cuts words into tokens, a part of one.
type Piece struct {
	Text       string
	Start, End float64
}

// Words joins a transcript's pieces into words. With tokens (Soniox's), a
// piece that does not start with a space goes on the word before it; either
// way, a piece that is only punctuation does.
func Words(pieces []Piece, tokens bool) []Word {
	var out []Word

	for _, p := range pieces {
		text := strings.TrimSpace(p.Text)
		if text == "" {
			continue
		}

		glued := tokens && !strings.HasPrefix(p.Text, " ") && !strings.HasPrefix(p.Text, "\t")
		if n := len(out); n > 0 && (glued || letters(text) == "" && !hasDigit(text)) {
			out[n-1].Word += text
			out[n-1].End = math.Max(out[n-1].End, p.End)

			continue
		}

		out = append(out, Word{Word: text, Start: p.Start, End: math.Max(p.Start, p.End)})
	}

	return out
}

func hasDigit(s string) bool { return strings.ContainsFunc(s, unicode.IsDigit) }
