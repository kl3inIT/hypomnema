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

		open := func(name string) {
			order, deadline := f.request(false)
			v := &View{life: t.Context(), order: order}
			f.move(t.Context(), v, deadline, true, bring(name))
		}

		open("older")
		time.Sleep(5 * time.Second)
		open("newer")

		time.Sleep(time.Minute)
		synctest.Wait()

		mu.Lock()
		defer mu.Unlock()

		assert.Equal(t, []string{"newer at " + TakeoverWait.String()}, brought)
	})
}

// A call outlasting the wait must not cause a second wait after tab creation.
func TestReviewMakingAndFrontingShareOneDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var f focus

		if err := f.enter(t.Context()); err != nil {
			t.Fatal(err)
		}
		defer f.leave()

		start := time.Now()
		_, ownerDeadline := f.request(true)
		made := make(chan time.Duration, 1)

		go func() {
			err := f.alone(t.Context(), ownerDeadline, func() error {
				made <- time.Since(start)

				return nil
			})
			f.prepared()
			if err != nil {
				t.Error(err)
			}
		}()

		time.Sleep(5 * time.Second)
		order, deadline := f.request(false)
		bot := &View{life: t.Context(), order: order}
		var fronted time.Duration

		f.move(t.Context(), bot, deadline, true, func(context.Context) {
			fronted = time.Since(start)
		})

		assert.Equal(t, TakeoverWait, <-made, "the owner's tab waits for running calls")
		synctest.Wait()
		assert.Equal(t, TakeoverWait, fronted, "the later pane uses the same deadline")
	})
}
