package agentapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/agentapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/phone"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
)

// sharedPhone stands for the phone: it records who called what and answers
// with err when it is set.
type sharedPhone struct {
	calls []string
	works []string
	err   error
}

func (p *sharedPhone) Tools(context.Context) ([]devicetool.Tool, error) {
	return devicetool.Tools(), p.err
}

func (p *sharedPhone) Call(_ context.Context, c phone.Caller, tool string, arguments json.RawMessage,
) (json.RawMessage, error) {
	if p.err != nil {
		return nil, p.err
	}

	p.calls = append(p.calls, fmt.Sprintf("%s %s %s", c.Key, tool, arguments))
	p.works = append(p.works, c.Work)

	return json.RawMessage(`{"content":[{"type":"image","data":"cG5n","mimeType":"image/png"}],"isError":false}`), nil
}

// withPhone serves the world's agent API with a phone.
func withPhone(w *world, p agentapi.Device) {
	mux := http.NewServeMux()
	agentapi.New(w.svc, nil, slog.New(slog.NewTextHandler(w.logs, nil))).WithDevice(p).Register(mux)

	w.srv = httptest.NewServer(mux)
	w.t.Cleanup(w.srv.Close)
}

func (w *world) useDevice(key, tool, arguments string) (int, string) {
	w.t.Helper()

	status, body := w.call(http.MethodPost, agenttool.PathDeviceCall, key,
		agenttool.BrowserCall{Tool: tool, Arguments: json.RawMessage(arguments)})

	return status, string(body)
}

// Scenario: every worker has the phone, as it has the browser (issue #417,
// owner 2026-11-15), even one whose Run was given no account. A call reaches
// the phone under the worker's own name, so its screens stay its own, and the
// answer is passed on as it is. Which agent used which tool, and for how long,
// is logged, never what it typed; without a credential nothing is listed or run.
func TestEveryWorkerUsesThePhoneAsItself(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	p := &sharedPhone{}
	withPhone(w, p)

	key, err := w.svc.AddAgentSession(t.Context(), "dsp_1", nil)
	require.NoError(t, err)

	status, body := w.call(http.MethodGet, agenttool.PathDeviceTools, key, nil)
	require.Equal(t, http.StatusOK, status)

	var tools agenttool.BrowserToolList
	require.NoError(t, json.Unmarshal(body, &tools))
	require.Len(t, tools.Data, len(devicetool.Tools()))
	assert.Equal(t, devicetool.Screen, tools.Data[0].Name)

	status, answer := w.useDevice(key, devicetool.Type, `{"text":"mật khẩu bí mật"}`)
	require.Equal(t, http.StatusOK, status)
	assert.JSONEq(t, `{"content":[{"type":"image","data":"cG5n","mimeType":"image/png"}],"isError":false}`, answer)
	assert.Equal(t, []string{`d-dsp_1 device_type {"text":"mật khẩu bí mật"}`}, p.calls)

	logs := w.logs.String()
	assert.Contains(t, logs, "dsp_1")
	assert.Contains(t, logs, "device_type")
	assert.Contains(t, logs, "duration_ms=", "how long the call took")
	assert.NotContains(t, logs, "bí mật")

	status, _ = w.call(http.MethodGet, agenttool.PathDeviceTools, "", nil)
	assert.Equal(t, http.StatusUnauthorized, status)

	status, _ = w.useDevice("", devicetool.Screen, `{}`)
	assert.Equal(t, http.StatusUnauthorized, status)
	assert.Len(t, p.calls, 1, "a call without a credential does not reach the phone")
}

// Scenario: a Bot's pi has the phone too (issue #536, step 4), on the same
// routes as a worker, and its calls are its chat's: the phone keeps the last
// screen of each chat's pi apart from a worker's, and the log names the Bot
// and the chat.
func TestABotsPiUsesThePhoneAsItsChat(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	p := &sharedPhone{}
	withPhone(w, p)

	ctx := t.Context()

	_, _, err := w.svc.AddToken(ctx, "pi", bearertoken.Coordinator)
	require.NoError(t, err)

	chat, err := w.svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	pi, err := w.svc.AddBotToken(ctx, "pi", chat.ID, orchestration.DefaultBotID)
	require.NoError(t, err)

	status, body := w.call(http.MethodGet, agenttool.PathDeviceTools, pi, nil)
	require.Equal(t, http.StatusOK, status)

	var tools agenttool.BrowserToolList
	require.NoError(t, json.Unmarshal(body, &tools))
	assert.Len(t, tools.Data, len(devicetool.Tools()))

	status, _ = w.useDevice(pi, devicetool.Tap, `{"target":"#3"}`)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, []string{`b-` + chat.ID + ` device_tap {"target":"#3"}`}, p.calls)
	assert.Contains(t, w.logs.String(), "bot "+orchestration.DefaultBotID+" in chat "+chat.ID)
}

// An agent is told what it did wrong and why the phone cannot be used now;
// a control plane with no phone says so.
func TestAPhoneCallThatFailsSaysWhy(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	p := &sharedPhone{}
	withPhone(w, p)

	key, err := w.svc.AddAgentSession(t.Context(), "dsp_1", nil)
	require.NoError(t, err)

	tests := []struct {
		err    error
		status int
		says   string
	}{
		{fmt.Errorf("%w: device_reboot", devicetool.ErrNoSuchTool), http.StatusNotFound, "device_reboot"},
		{fmt.Errorf("%w: key is one of back", devicetool.ErrBadArguments), http.StatusBadRequest, "key is one of"},
		{fmt.Errorf("%w: the phone is waking", phone.ErrUnavailable), http.StatusServiceUnavailable, "waking"},
	}

	for _, tt := range tests {
		p.err = tt.err

		status, body := w.useDevice(key, devicetool.Key, `{"key":"power"}`)
		assert.Equal(t, tt.status, status, tt.err)
		assert.Contains(t, body, tt.says, tt.err)
	}

	assert.NotContains(t, w.logs.String(), "an agent used the phone", "a call that failed is not logged as made")

	none := newWorld(t)
	noneKey, err := none.svc.AddAgentSession(t.Context(), "dsp_2", nil)
	require.NoError(t, err)

	status, body := none.call(http.MethodGet, agenttool.PathDeviceTools, noneKey, nil)
	assert.Equal(t, http.StatusServiceUnavailable, status)
	assert.Contains(t, string(body), "no phone")
}

// Scenario: the phone is told what each caller works on, which a take it
// films ends with (issue #817): a worker's Dispatch, and a Bot's turn, which
// is another turn once the chat's turn settled and the next began.
func TestThePhoneKnowsTheWorkOfEachCall(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	p := &sharedPhone{}
	withPhone(w, p)

	ctx := t.Context()

	key, err := w.svc.AddAgentSession(ctx, "dsp_1", nil)
	require.NoError(t, err)

	status, _ := w.useDevice(key, devicetool.VideoStart, `{"title":"Sign in"}`)
	require.Equal(t, http.StatusOK, status)

	_, _, err = w.svc.AddToken(ctx, "pi", bearertoken.Coordinator)
	require.NoError(t, err)

	chat, err := w.svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	pi, err := w.svc.AddBotToken(ctx, "pi", chat.ID, orchestration.DefaultBotID)
	require.NoError(t, err)

	require.NoError(t, w.svc.MarkChatTurn(ctx, chat.ID, "pi-1"))

	status, _ = w.useDevice(pi, devicetool.Screen, `{}`)
	require.Equal(t, http.StatusOK, status)

	require.NoError(t, w.svc.SettleChatTurn(ctx, chat.ID, "pi-1"))
	time.Sleep(2 * time.Millisecond)
	require.NoError(t, w.svc.MarkChatTurn(ctx, chat.ID, "pi-1"))

	status, _ = w.useDevice(pi, devicetool.Screen, `{}`)
	require.Equal(t, http.StatusOK, status)

	require.Len(t, p.works, 3)
	assert.Equal(t, "dispatch dsp_1", p.works[0])
	assert.True(t, strings.HasPrefix(p.works[1], "turn "+chat.ID+" pi-1@"), p.works[1])
	assert.NotEqual(t, p.works[1], p.works[2], "the chat's next turn is other work")
}
