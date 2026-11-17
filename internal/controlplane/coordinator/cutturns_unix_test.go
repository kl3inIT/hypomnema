//go:build unix

package coordinator

// The update boundary is read from selfupdate's marker, which exists on unix only (update_other.go stubs it):
// this test runs where the control plane updates itself (issue #784).

import (
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/selfupdate"
)

// Scenario (review round 3 of #725, finding 2): a pi start has looked for an update for the last time, and finds
// none; right then the update begins. It begins through BeginUpdate, which waits for the start: the pi that started
// started before the update began, and once it has begun no pi starts.
func TestTheUpdateBeginsOnlyAfterAStartThatLookedBeforeIt(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	keys := &blockingKeys{asked: make(chan struct{}, 1), release: make(chan struct{})}
	log := &lockedBuffer{}

	var (
		mu       sync.Mutex
		armed    bool
		boundary = make(chan struct{})
		letGo    = make(chan struct{})
	)

	svc, _ := newStore(t)
	ctx := t.Context()

	sup, _ := runSupervisor(t, svc, Config{
		Keys: keys, Logger: slog.New(slog.NewTextHandler(log, nil)),
		Updating: func() bool {
			updating := selfupdate.Updating(dir)

			mu.Lock()
			hold := armed
			armed = false
			mu.Unlock()

			// The boundary after the last look before pi starts: the update begins here.
			if hold {
				close(boundary)
				<-letGo
			}

			return updating
		},
	})

	chat, err := svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	_, err = svc.AddChatMessage(ctx, chat.ID, orchestration.ChatOwner, "hello")
	require.NoError(t, err)

	// The key is made after the first look; the next look is the last before pi starts.
	<-keys.asked
	mu.Lock()
	armed = true
	mu.Unlock()
	close(keys.release)
	<-boundary

	began := make(chan bool, 1)

	go func() {
		_ = sup.BeginUpdate(func() error {
			began <- strings.Contains(log.String(), "event=coordinator.started")

			return selfupdate.WriteMarker(dir, selfupdate.Begin{PID: os.Getpid()})
		})
	}()

	early := false

	select {
	case <-began:
		early = true
	case <-time.After(300 * time.Millisecond):
	}

	close(letGo)

	require.False(t, early, "the update began while a pi start that looked before it was under way")
	assert.True(t, <-began, "the pi started before the update began")
	waitFor(t, svc, chat.ID, orchestration.ChatCoordinator, "echo: hello")

	// Once it began, no pi starts.
	other, err := svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	_, err = svc.AddChatMessage(ctx, other.ID, orchestration.ChatOwner, "again")
	require.NoError(t, err)

	<-time.After(2*tick + time.Second)

	assert.Equal(t, 1, strings.Count(log.String(), "event=coordinator.started"), "no other pi starts")
}
