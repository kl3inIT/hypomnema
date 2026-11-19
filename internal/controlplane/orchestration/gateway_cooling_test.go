package orchestration

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

func (w *world) gatewayCooling(route, model string, reset time.Time) {
	w.t.Helper()
	_, err := w.db.ExecContext(w.t.Context(), `INSERT INTO gateway_model_cooling (route, model, observed_at, resets_at)
		VALUES (?, ?, ?, ?)`, route, model, w.clock.Now().UnixMilli(), reset.UnixMilli())
	require.NoError(w.t, err)
}

type roleGatewayAccounts struct {
	modelgateway.Subscriptions
	clock platform.Clock
	reset time.Time
}

func (a *roleGatewayAccounts) Token(
	_ context.Context, runtime, _ string, _ ...string,
) (modelgateway.AccountToken, error) {
	if runtime == "codex" && a.reset.After(a.clock.Now()) {
		return modelgateway.AccountToken{}, &modelgateway.CoolingError{Until: a.reset}
	}
	return modelgateway.AccountToken{AccountID: "account", AccessToken: "test-token"}, nil
}

func (*roleGatewayAccounts) NoteAnswer(string, string, http.Header) {}

func (a *roleGatewayAccounts) CoolDown(_ context.Context, runtime, id string, until time.Time, _ string) error {
	if runtime != "codex" || id != "account" {
		return fmt.Errorf("unexpected account %s/%s", runtime, id)
	}
	a.reset = until
	return nil
}

func (a *roleGatewayAccounts) FirstFree(
	_ context.Context, runtime string, _ func(string) time.Time,
) (time.Time, error) {
	if runtime == "codex" && a.reset.After(a.clock.Now()) {
		return a.reset, nil
	}
	return time.Time{}, nil
}

func (w *world) roleGateway(reset time.Time, backend *httptest.Server) (*modelgateway.Service, *http.ServeMux) {
	w.t.Helper()
	service := modelgateway.New(w.db, w.clock, platform.NewID,
		func(ctx context.Context, name string) (modelgateway.ProviderInfo, error) {
			provider, err := w.s.GetProvider(ctx, name)
			return modelgateway.ProviderInfo{Name: provider.Name, Kind: provider.Kind, Models: provider.Models,
				BaseURL: provider.BaseURL, APIKeyRef: provider.APIKeyRef}, err
		}, func(context.Context, string) (string, error) { return "test-api-key", nil })
	config := modelgateway.Config{Subscriptions: &roleGatewayAccounts{clock: w.clock, reset: reset}}
	if backend != nil {
		config.CodexBaseURL, config.ClaudeBaseURL = backend.URL, backend.URL
	}
	mux := http.NewServeMux()
	modelgateway.NewServer(service, platform.NewLogger(w.t.Output(), platform.LogConfig{}, "test"), config).Register(mux)
	return service, mux
}

func (w *world) askGateway(
	service *modelgateway.Service, mux *http.ServeMux, dispatch Dispatch, route, body string,
) *httptest.ResponseRecorder {
	w.t.Helper()
	key, err := service.AddDispatchKey(w.t.Context(), dispatch.ID, dispatch.Model)
	require.NoError(w.t, err)
	req := httptest.NewRequestWithContext(w.t.Context(), http.MethodPost, route,
		strings.NewReader(fmt.Sprintf(body, dispatch.Model)))
	req.Header.Set("Authorization", "Bearer "+key)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, req)
	return response
}

func TestRoleGatewayAliasHandsOffInTheSameWorktree(t *testing.T) {
	t.Parallel()
	w := roleWorld(t)
	ctx := t.Context()
	reset, wrongReset := t0.Add(time.Hour), t0.Add(24*time.Hour)
	service, mux := w.roleGateway(reset, nil)
	_, err := service.ApplyModel(ctx, "name: gpt-mixed\ntargets:\n"+
		"  - {provider: codex-subscription, model: gpt-6.2-sol}\n"+
		"  - {provider: claude-subscription, model: claude-opus-5-5}\n")
	require.NoError(t, err)
	_, err = w.s.ApplyProvider(ctx, "", "name: model-gateway\nkind: model_gateway\nmodels: [gpt-mixed]\n")
	require.NoError(t, err)
	require.NoError(t, w.s.ApplyRoleRouting(ctx, strings.ReplaceAll(defaultRoleRouting,
		"provider: codex-subscription, model: gpt-6.2-sol", "provider: model-gateway, model: gpt-mixed")))
	w.tasks(NewTask{Ref: "role", Kind: "narrow", Spec: "work"})
	created := w.schedule().Created
	require.Len(t, created, 1)
	first := w.dispatchOf(created[0])
	require.Equal(t, "gpt-mixed", first.Model)
	call := w.begin(first.ID)
	response := w.askGateway(service, mux, first, "/v1/responses", `{"model":%q,"input":"work"}`)
	require.Equal(t, http.StatusTooManyRequests, response.Code, response.Body.String())
	assert.Equal(t, "3600", response.Header().Get("Retry-After"))
	requests, _, err := service.UsageRequests(ctx, "24h", false, 5, 0)
	require.NoError(t, err)
	require.Len(t, requests, 1)
	assert.Equal(t, 1, requests[0].Attempts, "the Responses route cannot use the healthy Claude target")
	assert.Equal(t, "codex-subscription", requests[0].Provider)
	require.NoError(t, w.finish(call, Outcome{Class: FailureRateLimited, ResetsAt: &wrongReset}))
	created = w.schedule().Created
	require.Len(t, created, 1)
	second := w.dispatchOf(created[0])
	require.NotNil(t, second.ChainEntry)
	assert.Equal(t, 2, *second.ChainEntry)
	assert.Equal(t, "claude-subscription/claude-opus-5-5", second.Model)
	assert.True(t, second.Handoff)
	assert.Equal(t, first.NodeID, second.NodeID)
	assert.Equal(t, w.conversation(first.ConversationID).Worktree, w.conversation(second.ConversationID).Worktree)
	assert.Empty(t, w.schedule().Created)
	row, err := w.s.read().GetDispatch(ctx, first.ID)
	require.NoError(t, err)
	assert.Equal(t, reset.UnixMilli(), row.ResetsAt.Int64)
}

func TestRoleWaitsForTheFirstResetWhenNoBackupIsUsable(t *testing.T) {
	t.Parallel()
	w := roleWorld(t)
	reset := t0.Add(90 * time.Minute)
	service, mux := w.roleGateway(reset, nil)
	w.gatewayCooling("/v1/messages", "claude-subscription/claude-opus-5-5", t0.Add(2*time.Hour))
	task := w.tasks(NewTask{Ref: "role", Kind: "narrow", Spec: "work"})[0]
	created := w.schedule().Created
	require.Len(t, created, 1)
	first := w.dispatchOf(created[0])
	call := w.begin(first.ID)
	response := w.askGateway(service, mux, first, "/v1/responses", `{"model":%q,"input":"work"}`)
	require.Equal(t, http.StatusTooManyRequests, response.Code)
	require.NoError(t, w.finish(call, Outcome{Class: FailureRateLimited}))

	wantReason := "no account free for " + first.Model + " until " + reset.Format(time.RFC3339)
	for range 90 {
		w.alive(w.node)
		require.Empty(t, w.schedule().Created)
		assert.Equal(t, wantReason, w.task(task.ID).WaitingReason)
		w.advance(time.Minute)
	}
	var dispatches int
	row := w.db.QueryRowContext(t.Context(), "SELECT count(*) FROM dispatches WHERE task_id = ?", task.ID)
	require.NoError(t, row.Scan(&dispatches))
	assert.Equal(t, 1, dispatches)
	w.alive(w.node)
	created = w.schedule().Created
	require.Len(t, created, 1)
	assert.Equal(t, first.ConversationID, w.dispatchOf(created[0]).ConversationID)
	assert.Empty(t, w.task(task.ID).WaitingReason)
}

func TestReviewerSkipsASecondCoolingEntry(t *testing.T) {
	t.Parallel()
	w := roleWorld(t)
	author := w.tasks(NewTask{Ref: "author", Runtime: "omp", Model: "glm-5.3-flash", Spec: "work"})[0]
	created := w.schedule().Created
	require.Len(t, created, 1)
	require.NoError(t, w.finish(w.begin(created[0]), success("done")))
	w.tasks(NewTask{Ref: "review", Profile: "reviewer", ReviewOf: author.ID, Spec: "review"})
	created = w.schedule().Created
	require.Len(t, created, 1)
	first := w.dispatchOf(created[0])
	require.Equal(t, "codex", first.Runtime)
	call := w.begin(first.ID)
	reset := t0.Add(time.Hour)
	w.gatewayCooling("/v1/responses", first.Model, reset)
	w.gatewayCooling("/v1/messages", "claude-subscription/claude-opus-5-5", reset)
	require.NoError(t, w.finish(call, Outcome{Class: FailureRateLimited}))
	created = w.schedule().Created
	require.Len(t, created, 1)
	backup := w.dispatchOf(created[0])
	assert.Equal(t, "opencode-go/deepseek-v4.1-flash", backup.Model)
	require.NotNil(t, backup.ChainEntry)
	assert.Equal(t, 3, *backup.ChainEntry)
	assert.True(t, backup.Handoff)
	assert.Equal(t, w.conversation(first.ConversationID).Worktree, w.conversation(backup.ConversationID).Worktree)
}

func TestRoleDoesNotAddAWaitWhenTheGatewayResetAlreadyPassed(t *testing.T) {
	t.Parallel()
	w := roleWorld(t)
	service, mux := w.roleGateway(t0.Add(2*time.Second), nil)
	w.tasks(NewTask{Ref: "role", Kind: "narrow", Runtime: "codex", Spec: "work"})
	created := w.schedule().Created
	require.Len(t, created, 1)
	first := w.dispatchOf(created[0])
	call := w.begin(first.ID)
	response := w.askGateway(service, mux, first, "/v1/responses", `{"model":%q,"input":"work"}`)
	require.Equal(t, http.StatusTooManyRequests, response.Code)
	w.advance(3 * time.Second)
	require.NoError(t, w.finish(call, Outcome{Class: FailureRateLimited}))
	created = w.schedule().Created
	require.Len(t, created, 1)
	assert.Equal(t, first.ConversationID, w.dispatchOf(created[0]).ConversationID)
}

func TestRoleRateLimitWithoutGatewayFactDoesNotLoop(t *testing.T) {
	t.Parallel()
	w := roleWorld(t)
	backend := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, _ *http.Request) {
		out.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(out, `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`)
	}))
	defer backend.Close()
	service, mux := w.roleGateway(time.Time{}, backend)
	_, err := w.s.ApplyProvider(t.Context(), "", "name: remote\nkind: openai_compatible\nbase_url: "+
		backend.URL+"/v1\napi_key_ref: vault:remote\nmodels: [remote-model]\n")
	require.NoError(t, err)
	_, err = service.ApplyModel(t.Context(), "name: claude-remote\ntargets: [{provider: remote, model: remote-model}]\n")
	require.NoError(t, err)
	_, err = w.s.ApplyProvider(t.Context(), "", "name: model-gateway\nkind: model_gateway\nmodels: [claude-remote]\n")
	require.NoError(t, err)
	require.NoError(t, w.s.ApplyRoleRouting(t.Context(), strings.ReplaceAll(defaultRoleRouting,
		"provider: claude-subscription, model: claude-opus-5-5", "provider: model-gateway, model: claude-remote")))
	w.gatewayCooling("/v1/responses", "codex-subscription/gpt-6.2-sol", t0.Add(time.Hour))
	w.tasks(NewTask{Ref: "role", Kind: "broad", Spec: "work"})
	created := w.schedule().Created
	require.Len(t, created, 1)
	first := w.dispatchOf(created[0])
	call := w.begin(first.ID)
	response := w.askGateway(service, mux, first, "/v1/messages",
		`{"model":%q,"max_tokens":100,"messages":[{"role":"user","content":"work"}]}`)
	require.Equal(t, http.StatusTooManyRequests, response.Code, response.Body.String())
	assert.Empty(t, response.Header().Get("Retry-After"))
	_, err = w.s.read().GetGatewayModelCooling(t.Context(), store.GetGatewayModelCoolingParams{
		Route: "/v1/messages", Model: first.Model,
	})
	require.ErrorIs(t, err, sql.ErrNoRows)
	require.NoError(t, w.finish(call, Outcome{Class: FailureRateLimited}))
	for range 4 {
		w.alive(w.node)
		require.Empty(t, w.schedule().Created)
		w.advance(RateLimitCooldown / 4)
	}
	w.alive(w.node)
	created = w.schedule().Created
	require.Len(t, created, 1)
	assert.Equal(t, first.ConversationID, w.dispatchOf(created[0]).ConversationID)
}

func TestCodexStreamUsageLimitDoesNotLoop(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct {
		name, fields string
		wait         time.Duration
	}{
		{"provider-reset", `,"resets_in_seconds":5400`, 90 * time.Minute},
		{"no-reset", "", time.Hour},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			w := roleWorld(t)
			body := `data: {"type":"response.failed","response":{"error":{"code":"usage_limit_reached"` +
				scenario.fields + "}}}\n\n"
			backend := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, _ *http.Request) {
				out.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(out, body)
			}))
			defer backend.Close()
			service, mux := w.roleGateway(time.Time{}, backend)
			task := w.tasks(NewTask{Ref: "role", Kind: "narrow", Runtime: "codex", Spec: "work"})[0]
			firstConversation := ""
			const route = "/v1/responses"
			const request = `{"model":%q,"input":"work"}`
			for minute := time.Duration(0); minute < 3*time.Hour; minute += time.Minute {
				w.alive(w.node)
				created := w.schedule().Created
				if minute%scenario.wait == 0 {
					require.Len(t, created, 1, "at %s", minute)
					dispatch := w.dispatchOf(created[0])
					firstConversation = or(firstConversation, dispatch.ConversationID)
					assert.Equal(t, firstConversation, dispatch.ConversationID)
					assert.Empty(t, w.task(task.ID).WaitingReason)
					call := w.begin(dispatch.ID)
					reset := w.clock.Now().Add(scenario.wait)
					response := w.askGateway(service, mux, dispatch, route, request)
					require.Equal(t, http.StatusOK, response.Code)
					assert.Equal(t, body, response.Body.String())
					fact, err := w.s.read().GetGatewayModelCooling(t.Context(), store.GetGatewayModelCoolingParams{
						Route: route, Model: dispatch.Model,
					})
					require.NoError(t, err)
					assert.Equal(t, reset.UnixMilli(), fact.ResetsAt)
					refused := w.askGateway(service, mux, dispatch, route, request)
					require.Equal(t, http.StatusTooManyRequests, refused.Code)
					assert.Equal(t, fmt.Sprint(int(scenario.wait.Seconds())), refused.Header().Get("Retry-After"))
					require.NoError(t, w.finish(call, Outcome{Class: FailureRateLimited}))
					wantReason := "no account free for " + dispatch.Model + " until " + reset.Format(time.RFC3339)
					assert.Equal(t, wantReason, w.task(task.ID).WaitingReason)
				} else {
					require.Empty(t, created, "at %s", minute)
				}
				w.advance(time.Minute)
			}
			var dispatches int
			row := w.db.QueryRowContext(t.Context(), "SELECT count(*) FROM dispatches WHERE task_id = ?", task.ID)
			require.NoError(t, row.Scan(&dispatches))
			assert.Equal(t, int(3*time.Hour/scenario.wait), dispatches)
			failed, _, err := service.UsageRequests(t.Context(), "24h", true, 10, 0)
			require.NoError(t, err)
			require.Len(t, failed, dispatches)
			for _, usage := range failed {
				assert.Equal(t, modelgateway.OutcomeTruncated, usage.Outcome)
				assert.Equal(t, "usage_limit_reached", usage.Error)
			}
		})
	}
}

func TestGatewayCoolingMigrationKeepsExistingUsage(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	_, db := migratedTo(t, 132)
	_, err := db.ExecContext(ctx, `
		INSERT INTO gateway_keys (id, name, key_hash, models, created_at) VALUES ('key', 'key', 'hash', '["*"]', 1);
		INSERT INTO gateway_usage (id, key_id, model, provider, upstream_model, stream, attempts, outcome, status,
		    estimated, duration_ms, created_at, cache_write_tokens, cost_micros, cache_write_1h_tokens, ttft_ms, error)
		VALUES ('old', 'key', 'model', 'provider', 'upstream', 1, 1, 'truncated', 200, 1, 300, 1,
		    101, 102, 103, 104, 'no event for 5m');
		INSERT INTO gateway_usage_daily (day, caller, model, provider, upstream_model, account_id, requests,
		    estimated_requests, estimated_prompt_tokens, measured_prompt_tokens, measured_cached_tokens)
		VALUES (0, 'owner', 'model', 'provider', 'upstream', '', 2, 1, 201, 320, 203);
	`)
	require.NoError(t, err)
	assertExistingUsage := func() {
		t.Helper()
		row := db.QueryRowContext(ctx, `SELECT cache_write_tokens, cost_micros, cache_write_1h_tokens, ttft_ms,
			estimated, error FROM gateway_usage WHERE id = 'old'`)
		var writes, cost, hourly, ttft, estimated int
		var reason string
		require.NoError(t, row.Scan(&writes, &cost, &hourly, &ttft, &estimated, &reason))
		assert.Equal(t, []int{101, 102, 103, 104, 1}, []int{writes, cost, hourly, ttft, estimated})
		assert.Equal(t, "no event for 5m", reason)

		row = db.QueryRowContext(ctx, `SELECT requests, estimated_requests, estimated_prompt_tokens,
			measured_prompt_tokens, measured_cached_tokens FROM gateway_usage_daily`)
		var requests, estimatedRequests, estimatedPrompt, measuredPrompt, measuredCached int
		require.NoError(t, row.Scan(&requests, &estimatedRequests, &estimatedPrompt, &measuredPrompt, &measuredCached))
		assert.Equal(t, []int{2, 1, 201, 320, 203},
			[]int{requests, estimatedRequests, estimatedPrompt, measuredPrompt, measuredCached})
	}
	assertExistingUsage()
	require.NoError(t, Migrate(ctx, db))
	assertExistingUsage()
	_, err = db.ExecContext(ctx, `
		INSERT INTO gateway_usage
		(id, key_id, model, provider, upstream_model, stream, attempts, outcome, status, duration_ms, created_at)
		VALUES ('new', 'key', 'model', 'provider', 'upstream', 0, 1, 'no_account', 429, 10, 2);
		UPDATE gateway_usage_daily SET requests = requests + 1, no_account = 1;
	`)
	require.NoError(t, err)
	migrations, err := provider(db)
	require.NoError(t, err)
	rolledBack, err := migrations.Down(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(135), rolledBack.Source.Version)
	assertExistingUsage()
	var requests int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM gateway_usage`).Scan(&requests))
	assert.Equal(t, 1, requests)
}
