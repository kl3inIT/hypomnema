package meetingstore

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/storeutil"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/storeerr"
)

// The words to hear right (issue #747): one list the owner keeps, given to every transcription, as Nojoin keeps its
// glossary in its settings and OpenNoteTaker its DEEPGRAM_KEYTERMS; the names found elsewhere are read where they
// are kept (FoundWords). A word the owner fixes in a transcript joins the list (ReplaceWord), as silent-notetaker's
// corrections bar keeps its pairs (index.html:1784) and Lark Minutes replaces a word across a transcript
// (lark-cli shortcuts/minutes/minutes_word_replace.go).

const (
	// MaxWords bounds the owner's words, as MemoryOS bounds a meeting's terms (MeetingService.MAX_TERMS, 100).
	MaxWords = 100
	// MaxWord bounds one word, as MemoryOS's MAX_TERM.
	MaxWord = 100
	// maxLineText bounds what a line says after a fix: a line is cut at 400 characters when heard.
	maxLineText = 4000
)

// Words returns the owner's words, in the order they were added.
func (s *Store) Words(ctx context.Context) ([]string, error) {
	words, err := s.read().ListMeetingWords(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the words to hear right: %w", err)
	}

	return words, nil
}

// SetWords makes the owner's words these: a word kept keeps its place, a new one goes last.
func (s *Store) SetWords(ctx context.Context, words []string) error {
	clean, err := cleanList(words, MaxWords, MaxWord, "words to hear right")
	if err != nil {
		return err
	}

	return s.write(ctx, func(q *store.Queries, now int64) error {
		had, err := q.ListMeetingWords(ctx)
		if err != nil {
			return err
		}

		keep := map[string]bool{}
		for _, w := range clean {
			keep[w] = true
		}

		for _, w := range had {
			if !keep[w] {
				if err := q.DeleteMeetingWord(ctx, w); err != nil {
					return err
				}
			}
		}

		for i, w := range clean {
			// One millisecond apart, so the order given is the order kept.
			if err := q.InsertMeetingWord(ctx, store.InsertMeetingWordParams{Word: w, Now: now + int64(i)}); err != nil {
				return fmt.Errorf("keeping the word %q: %w", w, err)
			}
		}

		return nil
	})
}

// FoundWords are the words found where they are kept, not set by the owner.
type FoundWords struct {
	// Speakers are the names the owner gave voices, the most recently heard first (Nojoin's people library).
	Speakers []string
	// Bots and Repositories are the Bots' and the registered repositories' names.
	Bots         []string
	Repositories []string
}

// FoundWords reads the names the transcriptions are also given.
func (s *Store) FoundWords(ctx context.Context) (FoundWords, error) {
	q := s.read()

	speakers, err := q.MeetingSpeakerNames(ctx)
	if err != nil {
		return FoundWords{}, fmt.Errorf("reading the names of voices: %w", err)
	}

	bots, err := q.MeetingBotNames(ctx)
	if err != nil {
		return FoundWords{}, fmt.Errorf("reading the Bots' names: %w", err)
	}

	repos, err := q.MeetingRepoNames(ctx)
	if err != nil {
		return FoundWords{}, fmt.Errorf("reading the repositories' names: %w", err)
	}

	return FoundWords{Speakers: speakers, Bots: bots, Repositories: repos}, nil
}

// Fix is a word the owner says was heard wrong: From as it reads, To as it was said, in one line or, with Line 0,
// in the whole meeting.
type Fix struct {
	From string
	To   string
	Line int
}

// Fixed is what a fix did: how many times the word was replaced, and whether the right word is in the owner's
// list now (it is not when the list is full or the word too long for it).
type Fixed struct {
	Replaced int
	Learned  bool
}

// ReplaceWord replaces a word in a line or every line of a meeting, as a whole word in any case (silent-notetaker's
// corrections are case-insensitive; its lack of a word-boundary guard is named an imperfection in
// crates/silent-notes/src/corrections.rs, so a word inside a longer one is left alone here), and adds the right
// word to the owner's words. The notes are not written again: the owner asks for that.
func (s *Store) ReplaceWord(ctx context.Context, id string, fix Fix) (Fixed, error) {
	from := norm.NFC.String(strings.TrimSpace(fix.From))
	to := norm.NFC.String(strings.TrimSpace(fix.To))

	switch {
	case from == "" || to == "":
		return Fixed{}, fmt.Errorf("%w: a fix names the word heard and the word said", storeerr.ErrInvalid)
	case from == to:
		return Fixed{}, fmt.Errorf("%w: the word said is the word heard", storeerr.ErrInvalid)
	}

	for _, v := range []string{from, to} {
		if err := checkText(v, MaxLineWord, "a word"); err != nil {
			return Fixed{}, err
		}
	}

	var out Fixed

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		if _, err := q.GetMeeting(ctx, id); err != nil {
			return storeutil.NotFound(err, "meeting", id)
		}

		lines, err := q.ListMeetingLines(ctx, id)
		if err != nil {
			return fmt.Errorf("reading the transcript of meeting %s: %w", id, err)
		}

		found := fix.Line == 0

		for _, l := range lines {
			if fix.Line != 0 && int(l.N) != fix.Line {
				continue
			}

			found = true

			text, n := ReplaceWhole(l.Text, from, to)
			if n == 0 {
				continue
			}

			if utf8.RuneCountInString(text) > maxLineText {
				return fmt.Errorf("%w: line %d would be longer than %d characters", storeerr.ErrInvalid, l.N,
					maxLineText)
			}

			if _, err := q.SetMeetingLineText(ctx, store.SetMeetingLineTextParams{
				Text: text, MeetingID: id, N: l.N,
			}); err != nil {
				return fmt.Errorf("fixing line %d of meeting %s: %w", l.N, id, err)
			}

			out.Replaced += n
		}

		if !found {
			return fmt.Errorf("%w: line %d of meeting %s", storeerr.ErrNotFound, fix.Line, id)
		}

		if out.Replaced == 0 {
			where := "the meeting"
			if fix.Line != 0 {
				where = "line " + strconv.Itoa(fix.Line)
			}

			return fmt.Errorf("%w: %s does not say %q", storeerr.ErrInvalid, where, from)
		}

		out.Learned, err = learn(ctx, q, to, now)

		return err
	})

	return out, err
}

// MaxLineWord bounds what a fix replaces and with what.
const MaxLineWord = 200

// learn adds a word to the owner's list, unless it is there already, too long for it or the list is full.
func learn(ctx context.Context, q *store.Queries, word string, now int64) (bool, error) {
	if utf8.RuneCountInString(word) > MaxWord {
		return false, nil
	}

	had, err := q.ListMeetingWords(ctx)
	if err != nil {
		return false, err
	}

	for _, w := range had {
		if Fold(w) == Fold(word) {
			return true, nil
		}
	}

	if len(had) >= MaxWords {
		return false, nil
	}

	if err := q.InsertMeetingWord(ctx, store.InsertMeetingWordParams{Word: word, Now: now}); err != nil {
		return false, fmt.Errorf("keeping the word %q: %w", word, err)
	}

	return true, nil
}

// ReplaceWhole replaces every whole-word occurrence of from in text, in any case, with to, and says how many there
// were. An occurrence is whole when no letter or digit touches it on a side where from begins or ends with one.
func ReplaceWhole(text, from, to string) (string, int) {
	text = norm.NFC.String(text)
	lowerText, lowerFrom := []rune(strings.ToLower(text)), []rune(strings.ToLower(from))
	runes := []rune(text)

	// strings.ToLower keeps the count of runes for every letter a transcript holds, so the indexes agree; when it
	// does not, nothing is replaced rather than the wrong letters.
	if len(lowerText) != len(runes) || len(lowerFrom) != utf8.RuneCountInString(from) || len(lowerFrom) == 0 {
		return text, 0
	}

	var (
		b strings.Builder
		n int
	)

	for i := 0; i < len(runes); {
		if i+len(lowerFrom) <= len(runes) && equalRunes(lowerText[i:i+len(lowerFrom)], lowerFrom) &&
			whole(runes, i, i+len(lowerFrom)) {
			b.WriteString(to)

			i += len(lowerFrom)
			n++

			continue
		}

		b.WriteRune(runes[i])
		i++
	}

	if n == 0 {
		return text, 0
	}

	return b.String(), n
}

func equalRunes(a, b []rune) bool {
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}

func wordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.Mn, r)
}

// whole says whether runes[start:end] is not part of a longer word.
func whole(runes []rune, start, end int) bool {
	if start > 0 && wordRune(runes[start]) && wordRune(runes[start-1]) {
		return false
	}

	if end < len(runes) && wordRune(runes[end-1]) && wordRune(runes[end]) {
		return false
	}

	return true
}
