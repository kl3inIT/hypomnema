package speech

import (
	"strings"
	"time"
)

// A transcript is cut into segments as MemoryOS cuts a meeting's
// (voice/SonioxAsync.java:112-151, group): one speaker's words, broken where
// the speaker changes, where the recording pauses over 800 ms, or where a
// sentence has run past 400 characters; markers and sounds that are not
// speech are left out.
const (
	segmentGap      = 800 * time.Millisecond
	maxSegmentChars = 400
)

// token is a piece of what was said, as a provider gives it: Soniox's tokens
// carry their own leading space; ElevenLabs's words are given one
// (elevenlabs.go).
type token struct {
	text       string
	start, end time.Duration
	speaker    string
	// event is a sound that is not speech (Soniox's is_audio_event, ElevenLabs's audio_event).
	event bool
}

// segments groups the tokens.
func segments(tokens []token) []Segment {
	var (
		out     []Segment
		current *Segment
		text    strings.Builder
	)

	flush := func() {
		if current == nil {
			return
		}

		if said := strings.TrimSpace(text.String()); said != "" {
			current.Text = said
			current.End = max(current.End, current.Start)
			out = append(out, *current)
		}

		current = nil

		text.Reset()
	}

	for _, t := range tokens {
		if t.text == "" || t.text == "<fin>" || t.text == "<end>" || t.event {
			continue
		}

		if current != nil && (current.Speaker != t.speaker || t.start-current.End > segmentGap ||
			len([]rune(text.String())) > maxSegmentChars) {
			flush()
		}

		if current == nil {
			current = &Segment{Speaker: t.speaker, Start: t.start}
		}

		text.WriteString(t.text)
		current.End = max(current.End, t.end)
	}

	flush()

	return out
}

// lastEnd is when the last token ends: the recording's length when the provider does not say it.
func lastEnd(tokens []token) time.Duration {
	var end time.Duration

	for _, t := range tokens {
		end = max(end, t.end)
	}

	return end
}
