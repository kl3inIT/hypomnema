package meetings

// SetPublished makes a Service call f in a track's opening, right after the track takes its stream: a test holds
// the opening there to end the meeting meanwhile.
func SetPublished(s *Service, f func()) { s.published = f }
