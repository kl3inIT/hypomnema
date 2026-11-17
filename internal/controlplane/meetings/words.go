package meetings

import (
	"context"
	"unicode/utf8"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/meetingstore"
)

// The words to hear right (issue #747): the owner's list, kept once on Settings → Meetings and given to every
// transcription (Nojoin's glossary setting, OpenNoteTaker's DEEPGRAM_KEYTERMS), with the names found where they
// are kept: the names the owner gave voices in earlier meetings (Nojoin's people library, models/speaker.py
// GlobalSpeaker), and the Bots' and the registered repositories' names (no reference does this; the owner's design
// of issue #747).

// Where a word comes from.
const (
	FromOwner      = "owner"
	FromSpeaker    = "speaker"
	FromBot        = "bot"
	FromRepository = "repository"
)

// MaxSentWords bounds the words a transcription is given: MemoryOS gives Soniox at most 100 terms
// (MeetingService.MAX_TERMS) of at most 100 characters, which keeps them inside the 10,000 characters Soniox takes
// as a context. LiteLLM (llms/soniox/audio_transcription/transformation.py) sets no bound of its own. The owner's
// words come first, so the found ones are what is left out past it.
const MaxSentWords = 100

// Word is a word to hear right.
type Word struct {
	Text string
	From string
	// Sent: the word is among those a transcription is given.
	Sent bool
}

// Words lists the words to hear right: the owner's, then the names found, each once.
func (s *Service) Words(ctx context.Context) ([]Word, error) {
	own, err := s.cfg.Store.Words(ctx)
	if err != nil {
		return nil, err
	}

	found, err := s.cfg.Store.FoundWords(ctx)
	if err != nil {
		return nil, err
	}

	var out []Word

	seen := map[string]bool{}
	add := func(text, from string) {
		key := meetingstore.Fold(text)
		if key == "" || seen[key] || utf8.RuneCountInString(text) > meetingstore.MaxWord {
			return
		}

		seen[key] = true
		out = append(out, Word{Text: text, From: from, Sent: len(out) < MaxSentWords})
	}

	for _, w := range own {
		add(w, FromOwner)
	}

	for _, w := range found.Speakers {
		add(w, FromSpeaker)
	}

	for _, w := range found.Bots {
		add(w, FromBot)
	}

	for _, w := range found.Repositories {
		add(w, FromRepository)
	}

	return out, nil
}

// sentWords are the words a transcription is given.
func (s *Service) sentWords(ctx context.Context) ([]string, error) {
	all, err := s.Words(ctx)
	if err != nil {
		return nil, err
	}

	var out []string

	for _, w := range all {
		if w.Sent {
			out = append(out, w.Text)
		}
	}

	return out, nil
}

// SetWords makes the owner's words these.
func (s *Service) SetWords(ctx context.Context, words []string) error {
	return s.cfg.Store.SetWords(ctx, words)
}

// ReplaceWord fixes a word heard wrong in a line or the whole meeting; the right word joins the owner's words. The
// notes are not written again: the owner asks for that with Write again.
func (s *Service) ReplaceWord(ctx context.Context, id string, fix meetingstore.Fix) (meetingstore.Fixed, error) {
	return s.cfg.Store.ReplaceWord(ctx, id, fix)
}
