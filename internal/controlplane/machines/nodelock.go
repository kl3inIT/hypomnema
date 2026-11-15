package machines

import (
	"context"
	"sync"
)

// nodeLocks lets one step for a node run at a time, as client-go's workqueue
// hands a key to one worker at a time and processes it again after when it is
// added meanwhile (util/workqueue/queue.go, its processing and dirty sets).
type nodeLocks struct {
	mu   sync.Mutex
	busy map[string]chan struct{}
}

// lock waits until no step for the node runs and returns the function that
// ends this one.
func (l *nodeLocks) lock(ctx context.Context, nodeID string) (func(), error) {
	for {
		unlock, held := l.tryLock(nodeID)
		if unlock != nil {
			return unlock, nil
		}

		select {
		case <-held:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// tryLock takes the node's lock when no step for it runs. Otherwise it
// returns no function and a channel closed when the running step ends.
func (l *nodeLocks) tryLock(nodeID string) (func(), <-chan struct{}) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if held, ok := l.busy[nodeID]; ok {
		return nil, held
	}

	if l.busy == nil {
		l.busy = map[string]chan struct{}{}
	}

	done := make(chan struct{})
	l.busy[nodeID] = done

	return func() {
		l.mu.Lock()
		delete(l.busy, nodeID)
		l.mu.Unlock()
		close(done)
	}, nil
}
