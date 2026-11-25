package agentapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/agentapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// commands plays the coordinator's background commands: it records what it was asked, and fails with err when set.
type commands struct {
	mu    sync.Mutex
	asked []string
	err   error
}

func (c *commands) StartBackground(_ context.Context, chatID, bot, pi string, in agenttool.BackgroundRequest,
) (agenttool.BackgroundAnswer, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.asked = append(c.asked, fmt.Sprintf("start %s %s %s %q %s %d", chatID, bot, pi, in.Command, in.Cwd,
		in.TimeoutSeconds))

	return agenttool.BackgroundAnswer{ID: "bg_0123456789ab", OutputFile: "/run/bg_0123456789ab.log",
		TimeoutSeconds: 1800}, c.err
}

func (c *commands) StopBackground(_ context.Context, chatID, id string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.asked = append(c.asked, "stop "+chatID+" "+id)

	return true, c.err
}

// Scenario (issues #1058, #1084): a Bot's pi starts a background command for its chat as its Bot, with its own token,
// and the next pi of the chat stops it; no other agent runs one.
func TestABotsPiStartsAndStopsBackgroundCommandsOfItsChat(t *testing.T) {
	t.Parallel()

	bg := &commands{}
	w := newWorldWith(t, 0, func(s *agentapi.Server) { s.WithBackground(bg) })
	ctx := t.Context()

	pi, chatID := w.botPi(orchestration.Bot{Name: "maps"})
	chat, err := w.svc.GetChat(ctx, chatID)
	require.NoError(t, err)
	next, err := w.svc.AddBotToken(ctx, "pi", chatID, chat.Bot)
	require.NoError(t, err)
	_, owner, err := w.svc.AddToken(ctx, "laptop", bearertoken.Owner)
	require.NoError(t, err)

	start := agenttool.BackgroundRequest{Kind: agenttool.BackgroundStart, Command: "make all", Cwd: "/bots/maps"}
	status, body := w.call(http.MethodPost, agenttool.PathBackground, owner, start)
	assert.Equal(t, http.StatusForbidden, status, string(body))

	status, body = w.call(http.MethodPost, agenttool.PathBackground, pi, start)
	require.Equal(t, http.StatusOK, status, string(body))

	var got agenttool.BackgroundAnswer
	require.NoError(t, json.Unmarshal(body, &got))
	assert.Equal(t, agenttool.BackgroundAnswer{ID: "bg_0123456789ab", OutputFile: "/run/bg_0123456789ab.log",
		TimeoutSeconds: 1800}, got)

	status, body = w.call(http.MethodPost, agenttool.PathBackground, next,
		agenttool.BackgroundRequest{Kind: agenttool.BackgroundStop, ID: "bg_0123456789ab"})
	require.Equal(t, http.StatusOK, status, string(body))

	var stopped agenttool.BackgroundAnswer
	require.NoError(t, json.Unmarshal(body, &stopped))
	assert.Equal(t, agenttool.BackgroundAnswer{ID: "bg_0123456789ab", Stopped: true}, stopped)

	assert.Equal(t, []string{
		fmt.Sprintf("start %s %s %s %q /bots/maps 0", chatID, chat.Bot, bearertoken.Hash(pi), "make all"),
		"stop " + chatID + " bg_0123456789ab",
	}, bg.asked)

	status, body = w.call(http.MethodPost, agenttool.PathBackground, pi, agenttool.BackgroundRequest{Kind: "paused"})
	assert.Equal(t, http.StatusBadRequest, status, string(body))
}

// Scenario (issue #1084): a request the coordinator refuses as invalid answers 422; a launcher that failed, or none,
// answers 503 with why, so the Bot can run the command in the foreground.
func TestABackgroundCommandThatCannotRunSaysWhy(t *testing.T) {
	t.Parallel()

	bg := &commands{err: fmt.Errorf("%w: a background command's timeout is 1 to 2147483 seconds",
		orchestration.ErrInvalid)}
	w := newWorldWith(t, 0, func(s *agentapi.Server) { s.WithBackground(bg) })
	pi, _ := w.botPi(orchestration.Bot{Name: "maps"})
	start := agenttool.BackgroundRequest{Kind: agenttool.BackgroundStart, Command: "make", Cwd: "/w"}

	status, body := w.call(http.MethodPost, agenttool.PathBackground, pi, start)
	assert.Equal(t, http.StatusUnprocessableEntity, status, string(body))
	assert.Contains(t, string(body), "timeout is 1 to 2147483 seconds")

	bg.err = fmt.Errorf("%w: the background launcher's start failed", orchestration.ErrUnavailable)
	status, body = w.call(http.MethodPost, agenttool.PathBackground, pi, start)
	assert.Equal(t, http.StatusServiceUnavailable, status, string(body))
	assert.Contains(t, string(body), "the background launcher's start failed")

	none := newWorld(t)
	pi, _ = none.botPi(orchestration.Bot{Name: "maps"})
	status, body = none.call(http.MethodPost, agenttool.PathBackground, pi, start)
	assert.Equal(t, http.StatusServiceUnavailable, status, string(body))
	assert.Contains(t, string(body), "runs no background commands")
}
