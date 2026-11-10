package coordinator

import (
	"context"
	"fmt"
)

// Live frames go from pi to whoever watches its chat, from memory only; a
// watcher that falls behind loses frames, and the kept message that follows
// puts it right (Omnara's delta previews, replaced by the final message).

// Frame events.
const (
	// FrameReset starts a new reply: drop the text shown so far.
	FrameReset = "reset"
	// FrameDelta adds text or thinking: {kind, index, delta}.
	FrameDelta = "delta"
	// FrameTool says a tool started {phase: start, id, name, args} or ended {phase: end, id}.
	FrameTool = "tool"
	// FrameState says whether pi works: {working}.
	FrameState = "state"
)

// watchBuffer is how many frames a watcher may lag before frames are dropped.
const watchBuffer = 512

// Frame is one live update of a chat.
type Frame struct {
	Event string
	Data  any
}

type watcher chan Frame

// Watch returns the chat's live frames until stop is called.
func (s *Supervisor) Watch(chatID string) (frames <-chan Frame, stop func()) {
	w := make(watcher, watchBuffer)

	s.mu.Lock()
	if s.watchers[chatID] == nil {
		s.watchers[chatID] = map[watcher]struct{}{}
	}

	s.watchers[chatID][w] = struct{}{}
	s.mu.Unlock()

	return w, func() {
		s.mu.Lock()
		delete(s.watchers[chatID], w)

		if len(s.watchers[chatID]) == 0 {
			delete(s.watchers, chatID)
		}
		s.mu.Unlock()
	}
}

func (s *Supervisor) publish(chatID string, f Frame) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for w := range s.watchers[chatID] {
		select {
		case w <- f:
		default:
		}
	}
}

// Abort asks pi to stop the chat's current turn; nothing happens when pi is
// not working on it.
func (s *Supervisor) Abort(ctx context.Context, chatID string) error {
	s.mu.Lock()
	p := s.procs[chatID]
	s.mu.Unlock()

	if p == nil || p.exited() || !p.isWorking() {
		return nil
	}

	if _, err := p.call(ctx, map[string]any{"type": "abort"}); err != nil {
		return fmt.Errorf("stopping pi in chat %s: %w", chatID, err)
	}

	return nil
}
