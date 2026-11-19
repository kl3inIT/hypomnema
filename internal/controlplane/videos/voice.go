// Ported from aidemo (https://github.com/tandryukha/aidemo) at commit b67235a, src/voice.ts: voiceHash (line 234)
// and the reuse of a scene's clip when its hash is unchanged (lines 320-372). Issue #818, row 5.
//
// What differs, and why: a clip is kept by the hash of its text, provider, model, voice and language in one cache
// beside the videos, so that any later revision, of this video or another, reuses it, where aidemo compares each
// scene with the project's previous voice.json; the provider is the first in the owner's order that answers, which
// then speaks every scene of the revision (row 5), where aidemo's provider is set before the run.
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

package videos

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/speech"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/captions"
)

// clipFormat is the format the clips are spoken in.
const clipFormat = "mp3"

// spoken is the voice a revision was made with: the clips, and who spoke them.
type spoken struct {
	clips    []string
	provider string
	model    string
	voice    string
}

// voiceHash is a clip's identity: its text, provider, model, voice and language.
func voiceHash(text, provider, model, voice, lang string) string {
	raw, _ := json.Marshal(map[string]string{ //nolint:errchkjson // Strings.
		"text": text, "provider": provider, "model": model, "voice": voice, "lang": lang,
	})
	sum := sha256.Sum256(raw)

	return hex.EncodeToString(sum[:])
}

// speak voices each line: the first provider of the owner's order that
// answers speaks the first, and every other line with it; a clip made before
// with the same text, provider, model and voice is reused. done is told how
// many lines are voiced, after each.
func (s *Service) speak(ctx context.Context, source AudioSource, lines []string, asked string, done func(n int),
) (spoken, error) {
	set, err := s.settings(ctx)
	if err != nil {
		return spoken{}, err
	}

	voiceOf := func(p string) string {
		if asked != "" {
			return asked
		}

		return set.Voices[p]
	}

	var (
		out      spoken
		failures []string
	)

	for _, p := range set.Order(speech.KindTTS) {
		if slices.Contains(set.Off, p) {
			continue
		}

		clip, err := s.clip(ctx, source, lines[0], p, voiceOf(p))
		if err != nil {
			if ctx.Err() != nil {
				return spoken{}, ctx.Err()
			}

			failures = append(failures, p+": "+err.Error())

			continue
		}

		out = spoken{clips: []string{clip}, provider: p, model: speech.Model(p, speech.KindTTS), voice: voiceOf(p)}

		break
	}

	if out.provider == "" {
		if len(failures) == 0 {
			return spoken{}, errors.New("no audio provider is on")
		}

		return spoken{}, fmt.Errorf("scene 1: %s", strings.Join(failures, "; "))
	}

	done(1)

	for k, line := range lines[1:] {
		clip, err := s.clip(ctx, source, line, out.provider, out.voice)
		if err != nil {
			return spoken{}, fmt.Errorf("scene %d, %s: %w", k+2, out.provider, err)
		}

		out.clips = append(out.clips, clip)
		done(len(out.clips))
	}

	return out, nil
}

func (s *Service) settings(ctx context.Context) (speech.Settings, error) {
	if s.cfg.Settings == nil {
		return speech.Settings{}, nil
	}

	return s.cfg.Settings(ctx)
}

// clip is a line spoken by a provider: from the cache, or asked and kept there.
func (s *Service) clip(ctx context.Context, source AudioSource, text, provider, voice string) (string, error) {
	hash := voiceHash(text, provider, speech.Model(provider, speech.KindTTS), voice, Language)
	path := filepath.Join(s.cfg.Dir, voiceDir, hash+"."+clipFormat)

	if info, err := os.Stat(path); err == nil && info.Size() > 0 {
		return path, nil
	}

	out, err := s.cfg.Speech.Speak(ctx, source, speech.SpeakRequest{
		Input: text, Voice: voice, Format: clipFormat, Language: Language, Provider: provider,
	})
	if err != nil {
		return "", err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}

	if err := writeCache(path, out.Audio); err != nil {
		return "", err
	}

	return path, nil
}

func (s *Service) clipWords(ctx context.Context, source AudioSource, clip string) ([]captions.Word, error) {
	info, err := os.Stat(clip)
	if err != nil {
		return nil, err
	}

	path := clip + ".words.json"
	var words []captions.Word

	if raw, err := os.ReadFile(path); err == nil { //nolint:gosec // The voice cache's own file.
		if json.Unmarshal(raw, &words) == nil && len(words) > 0 {
			return words, nil
		}
	}

	t, err := s.cfg.Speech.Transcribe(ctx, source, speech.TranscribeRequest{
		Audio: speech.Audio{
			Name: filepath.Base(clip), MIME: "audio/mpeg", Size: info.Size(),
			Open: func() (io.ReadCloser, error) { return os.Open(clip) }, //nolint:gosec // The voice cache's own file.
		},
		Language: Language,
	})
	if err != nil {
		return nil, err
	}

	pieces := make([]captions.Piece, 0, len(t.Words))
	for _, w := range t.Words {
		pieces = append(pieces, captions.Piece{Text: w.Word, Start: w.Start.Seconds(), End: w.End.Seconds()})
	}

	words = captions.Words(pieces, t.Provider == speech.ProviderSoniox)
	if len(words) > 0 {
		raw, err := json.Marshal(words)
		if err != nil {
			return nil, err
		}

		if err := writeCache(path, raw); err != nil {
			return nil, err
		}
	}

	return words, nil
}

func writeCache(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}

	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)

		return err
	}

	return nil
}
