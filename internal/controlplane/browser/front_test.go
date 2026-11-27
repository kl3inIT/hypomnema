package browser

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
)

// Review round 2 of PR #1233: moves that replace each other share one wait. A call that outlasts the wait does not
// start a second one for the newer move, and the move replaced is not made: at TakeoverWait from the first move, only
// the newest is. On synctest's fake clock.
func TestReviewReplacingMovesShareOneDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var f focus

		if err := f.enter(t.Context()); err != nil {
			t.Fatal(err)
		}
		defer f.leave()

		start := time.Now()

		var (
			mu      sync.Mutex
			brought []string
		)

		bring := func(name string) func(context.Context) {
			return func(context.Context) {
				mu.Lock()
				defer mu.Unlock()

				brought = append(brought, name+" at "+time.Since(start).String())
			}
		}

		var wg sync.WaitGroup

		wg.Go(func() { f.move(t.Context(), bring("older")) })
		time.Sleep(5 * time.Second)
		wg.Go(func() { f.move(t.Context(), bring("newer")) })

		time.Sleep(time.Minute)
		synctest.Wait()

		mu.Lock()
		defer mu.Unlock()

		assert.Equal(t, []string{"newer at " + TakeoverWait.String()}, brought)
	})
}
