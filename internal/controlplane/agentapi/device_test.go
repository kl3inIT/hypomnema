package agentapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/agentapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/phone"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
)

// sharedPhone stands for the phone: it records who called what and answers
// with err when it is set.
type sharedPhone struct {
	calls []string
	err   error
}

func (p *sharedPhone) Tools(context.Context) ([]devicetool.Tool, error) {
	return devicetool.Tools(), p.err
}

func (p *sharedPhone) Call(_ context.Context, agent, tool string, arguments json.RawMessage) (json.RawMessage, error) {
	if p.err != nil {
		return nil, p.err
	}

	p.calls = append(p.calls, fmt.Sprintf("%s %s %s", agent, tool, arguments))

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
