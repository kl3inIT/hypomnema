package coordinator

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// usageOf is the context usage kept with the chat: "tokens/window", "-/window" without tokens, or "" before any.
func usageOf(t *testing.T, svc *orchestration.Service, chatID string) string {
	t.Helper()

	chat, err := svc.GetChat(t.Context(), chatID)
	require.NoError(t, err)

	switch u := chat.Usage; {
	case u == nil:
		return ""
	case u.Tokens == nil:
		return fmt.Sprintf("-/%d", u.Window)
	default:
		return fmt.Sprintf("%d/%d", *u.Tokens, u.Window)
	}
}

// nextUsage is the next usage frame a watcher of the chat gets.
func nextUsage(t *testing.T, frames <-chan Frame) UsageFrame {
	t.Helper()

	timeout := time.After(10 * time.Second)

	for {
		select {
		case f := <-frames:
			if u, ok := f.Data.(UsageFrame); ok && f.Event == FrameUsage {
				return u
			}
		case <-timeout:
			require.FailNow(t, "no usage frame")
		}
	}
}

func idle(t *testing.T, sup *Supervisor, chatID string) {
	t.Helper()

	require.Eventually(t, func() bool { return !sup.Working(chatID) }, 10*time.Second, 10*time.Millisecond)
}

// Scenario: the composer's context meter (issue #384). After each of pi's turns the supervisor asks pi how full its
// context is, keeps the report with the chat and tells the chat's watchers. The owner compacts: pi's report then has
// no tokens until its next answer measures them again.
func TestPiReportsItsContextAndCompacts(t *testing.T) {
	t.Parallel()

	svc, sup := newSupervisor(t)
	ctx := t.Context()

	chat, err := svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	frames, stop := sup.Watch(chat.ID)
	defer stop()

	_, err = svc.AddChatMessage(ctx, chat.ID, orchestration.ChatOwner, "no meter yet")
	require.NoError(t, err)
	waitFor(t, svc, chat.ID, orchestration.ChatCoordinator, "echo: no meter yet")
	idle(t, sup, chat.ID)
	assert.Empty(t, usageOf(t, svc, chat.ID), "a pi that reports no window leaves the chat without a meter")

	_, err = svc.AddChatMessage(ctx, chat.ID, orchestration.ChatOwner, "how is the context")
	require.NoError(t, err)

	u := nextUsage(t, frames)
	require.NotNil(t, u.Tokens)
	require.NotNil(t, u.Percent)
	assert.Equal(t, int64(128000), u.ContextWindow)
	assert.Equal(t, int64(2000), *u.Tokens)
	assert.InDelta(t, 1.5625, *u.Percent, 0.0001)
	assert.Equal(t, "2000/128000", usageOf(t, svc, chat.ID))
	idle(t, sup, chat.ID)

	require.NoError(t, sup.Compact(ctx, chat.ID))

	u = nextUsage(t, frames)
	assert.Nil(t, u.Tokens, "pi has not measured its context since it compacted")
	assert.Nil(t, u.Percent)
	assert.Equal(t, "-/128000", usageOf(t, svc, chat.ID))
	idle(t, sup, chat.ID)

	_, err = svc.AddChatMessage(ctx, chat.ID, orchestration.ChatOwner, "after the summary")
	require.NoError(t, err)

	u = nextUsage(t, frames)
	require.NotNil(t, u.Tokens)
	assert.Equal(t, int64(3000), *u.Tokens, "the next answer measures it again")
}

// Scenario: the owner compacts at the wrong time. pi aborts a running turn to compact, so a chat whose pi works is
// refused; a compaction pi fails ends the turn it made in a failed row, and the chat goes on.
func TestACompactionWaitsForItsTurnAndMayFail(t *testing.T) {
	t.Parallel()

	svc, sup := newSupervisor(t)
	ctx := t.Context()

	chat, err := svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	_, err = svc.AddChatMessage(ctx, chat.ID, orchestration.ChatOwner, "hang settled")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return sup.Working(chat.ID) }, 10*time.Second, 10*time.Millisecond)
	require.ErrorIs(t, sup.Compact(ctx, chat.ID), orchestration.ErrConflict)

	require.NoError(t, sup.Abort(ctx, chat.ID))
	waitFor(t, svc, chat.ID, orchestration.ChatSystem, "Stopped")
	idle(t, sup, chat.ID)

	_, err = svc.AddChatMessage(ctx, chat.ID, orchestration.ChatOwner, "too small to compact")
	require.NoError(t, err)
	waitFor(t, svc, chat.ID, orchestration.ChatCoordinator, "echo: too small to compact")
	idle(t, sup, chat.ID)

	require.NoError(t, sup.Compact(ctx, chat.ID))
	waitFor(t, svc, chat.ID, orchestration.ChatSystem, "pi's compaction failed: Nothing to compact (session too small)")
	idle(t, sup, chat.ID)

	_, err = svc.AddChatMessage(ctx, chat.ID, orchestration.ChatOwner, "still here")
	require.NoError(t, err)
	waitFor(t, svc, chat.ID, orchestration.ChatCoordinator, "echo: still here")
}
