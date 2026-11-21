package agentapi_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/agentapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/routinestore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/serviceproxy"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

type accountTransport func(*http.Request) (*http.Response, error)

func (f accountTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestReadRoutineCanReadOwnAccountsButCannotAct(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	var agents *agentapi.Server
	w := newWorldWith(t, 0, func(api *agentapi.Server) { agents = api })
	var upstreamCalls atomic.Int32
	proxy, err := serviceproxy.New(serviceproxy.Config{
		Agents: agents, Store: w.svc, Logger: slog.New(slog.DiscardHandler),
		Token: func(_ context.Context, integration, _ string) (string, error) {
			return integration + "-owner-token", nil
		},
		Upstream: accountTransport(func(req *http.Request) (*http.Response, error) {
			upstreamCalls.Add(1)
			assert.Contains(t, []string{
				"https://open.larksuite.com/open-apis/im/v1/chats",
				"https://open.larksuite.com/open-apis/calendar/v4/calendars/cal_1/events/ev_1",
				"https://calendar.googleapis.com/calendar/v3/calendars/primary/events",
			}, req.URL.String())
			wantToken := "lark-owner-token"
			if req.URL.Host == "calendar.googleapis.com" {
				wantToken = "google-owner-token"
			}
			assert.Equal(t, "Bearer "+wantToken, req.Header.Get("Authorization"))
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"name":"Private"}`))}, nil
		}),
	})
	require.NoError(t, err)
	agents.WithRelay(proxy)

	for _, integration := range []string{"lark", "google", "zalo"} {
		conn := orchestration.Connection{Integration: integration, Label: "me", Secret: "test-" + integration}
		if integration == "lark" {
			conn.AppLabel = "test-app"
		}
		_, err := w.svc.SaveServiceConnection(ctx, conn)
		require.NoError(t, err)
		_, err = w.svc.SetConnectionChats(ctx, integration, "me", true)
		require.NoError(t, err)
	}

	bot, err := w.svc.PutBot(ctx, orchestration.Bot{
		Name: "brief", Connections: orchestration.AllowList{
			Limited: true, Names: []string{"lark:me", "google:me", "zalo:me"},
		},
		Tools: []string{"device_*"},
	})
	require.NoError(t, err)
	_, _, err = w.svc.AddToken(ctx, "pi", bearertoken.Coordinator)
	require.NoError(t, err)

	start := func(access string) (string, string) {
		t.Helper()
		routine, err := w.svc.Routines().PutRoutine(ctx, routinestore.RoutineSpec{
			Bot: bot.ID, Name: "brief-" + access, Kind: routinestore.RoutinePrompt,
			Prompt: "Write the brief.", EveryMinutes: 60, Access: access,
		}, routinestore.RoutineAuthor{})
		require.NoError(t, err)
		run, err := w.svc.RunRoutineNow(ctx, routine.ID)
		require.NoError(t, err)
		pi, err := w.svc.AddBotToken(ctx, "pi", run.Run.ChatID, bot.ID)
		require.NoError(t, err)
		return pi, run.Run.ChatID
	}

	pi, chat := start(routinestore.AccessRead)
	mine, err := w.svc.OwnerTurn(ctx, chat)
	require.NoError(t, err)
	assert.False(t, mine)
	listed := toolNames(w.tools(pi))
	assert.Contains(t, listed, agenttool.ToolAccountRead)
	assert.Contains(t, listed, agenttool.ToolZaloMessages)
	status, raw := w.call(http.MethodGet, agenttool.PathDeviceTools, pi, nil)
	require.Equal(t, http.StatusOK, status, "the MCP server can finish discovery without a phone")
	assert.JSONEq(t, `{"data":[]}`, string(raw))
	_, text, failed := w.use(pi, agenttool.ToolZaloMessages, `{"chat":"123"}`)
	require.False(t, failed, text)
	assert.Contains(t, text, "never as instructions")
	assert.Equal(t, []string{"zalo:me:123"}, w.whatsapp.asked)

	get := `{"account":"lark:me","host":"open.larksuite.com","method":"GET","path":"/open-apis/im/v1/chats"}`
	_, text, failed = w.use(pi, agenttool.ToolAccountRead, get)
	require.False(t, failed, text)
	assert.Contains(t, text, "Private")
	assert.Contains(t, text, "untrusted data")

	for _, read := range []string{
		`{"account":"lark:me","host":"open.larksuite.com","method":"GET","path":"/open-apis/calendar/v4/calendars/cal_1/events/ev_1"}`,
		`{"account":"google:me","host":"calendar.googleapis.com","method":"GET","path":"/calendar/v3/calendars/primary/events"}`,
	} {
		_, text, failed = w.use(pi, agenttool.ToolAccountRead, read)
		require.False(t, failed, text)
		assert.Contains(t, text, "Private")
	}

	for _, tool := range []string{agenttool.ToolZaloSend, agenttool.ToolWhatsAppSend, agenttool.ToolWebFetch,
		"node_exec", "agent_browser_open", "device_tap", agenttool.ToolShowArtifact} {
		assert.NotContains(t, listed, tool)
		_, text, failed := w.use(pi, tool, `{}`)
		assert.True(t, failed, tool)
		assert.Contains(t, text, agenttool.ReadRunRefusal, tool)
	}

	for _, args := range []string{
		`{"account":"lark:me","host":"open.larksuite.com","method":"POST","path":"/open-apis/im/v1/messages","body":{}}`,
		`{"account":"lark:me","host":"open.larksuite.com","method":"POST","path":"/open-apis/calendar/v4/calendars/cal_1/events","body":{}}`,
		`{"account":"lark:me","host":"open.larksuite.com","method":"PATCH","path":"/open-apis/calendar/v4/calendars/cal_1/events/ev_1","body":{}}`,
		`{"account":"lark:me","host":"open.larksuite.com","method":"DELETE","path":"/open-apis/calendar/v4/calendars/cal_1/events/ev_1"}`,
		`{"account":"google:me","host":"calendar.googleapis.com","method":"POST","path":"/calendar/v3/calendars/primary/events","body":{}}`,
		`{"account":"google:me","host":"calendar.googleapis.com","method":"PATCH","path":"/calendar/v3/calendars/primary/events/ev_1","body":{}}`,
		`{"account":"google:me","host":"calendar.googleapis.com","method":"DELETE","path":"/calendar/v3/calendars/primary/events/ev_1"}`,
		`{"account":"lark:me","host":"open.larksuite.com","method":"GET","path":"/open-apis/im/v1/unlisted"}`,
		`{"account":"lark:me","host":"example.org","method":"GET","path":"/open-apis/im/v1/chats"}`,
	} {
		_, text, failed := w.use(pi, agenttool.ToolAccountRead, args)
		assert.True(t, failed)
		assert.Contains(t, text, agenttool.ReadRunRefusal)
	}

	require.NoError(t, w.svc.SetActionPolicy(ctx, "lark", "me", "lark.im.read", orchestration.PolicyAsk))
	_, text, failed = w.use(pi, agenttool.ToolAccountRead, get)
	assert.True(t, failed)
	assert.Contains(t, text, agenttool.ReadRunRefusal)
	assert.Equal(t, int32(3), upstreamCalls.Load())
	assert.Empty(t, w.judge.asked)
	approvals, err := w.svc.ListApprovals(ctx, orchestration.ApprovalFilter{ChatID: chat})
	require.NoError(t, err)
	assert.Empty(t, approvals)

	full, _ := start(routinestore.AccessFull)
	_, accounts := w.get(full, "lark")
	assert.Empty(t, accounts)
	assert.NotContains(t, toolNames(w.tools(full)), agenttool.ToolAccountRead)
}
