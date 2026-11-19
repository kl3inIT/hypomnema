package meetings

import "time"

// SetPublished makes a Service call f in a track's opening, right after the track takes its stream: a test holds
// the opening there to end the meeting meanwhile.
func SetPublished(s *Service, f func()) { s.published = f }

// SetLastWords makes a Service wait d for the tracks' last words in place of endWait.
func SetLastWords(s *Service, d time.Duration) { s.lastWords = d }
