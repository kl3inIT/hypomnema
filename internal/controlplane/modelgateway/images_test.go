package modelgateway_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

// imageAccount is an account of the fake subscriptions: its runtime, its
// plan and its project, as the subscription service gives them.
type imageAccount struct {
	id, runtime, plan, project string
}

// imageAccounts stands in for the accounts logged in on the control plane,
// taken in their order for each runtime; it records the chat cooldowns, which
// an image's refusal must never cause.
type imageAccounts struct {
	mu       sync.Mutex
	all      []imageAccount
	cooldown []string
}

func (a *imageAccounts) Token(_ context.Context, runtime, _ string, skip ...string) (modelgateway.AccountToken, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	for _, acc := range a.all {
		if acc.runtime == runtime && !slices.Contains(skip, acc.id) {
			return modelgateway.AccountToken{
				AccountID: acc.id, ExternalID: "ext-" + acc.id, AccessToken: "fake-token-" + acc.id,
				Project: acc.project, Plan: acc.plan, Version: 1,
			}, nil
		}
	}

	return modelgateway.AccountToken{}, modelgateway.ErrNoAccount
}

func (a *imageAccounts) TokenAfterRefusal(
	_ context.Context, refused modelgateway.AccountToken,
) (modelgateway.AccountToken, error) {
	refused.AccessToken += "-refreshed"
	refused.Version++

	return refused, nil
}

func (a *imageAccounts) CoolDown(_ context.Context, _, id string, _ time.Time, _ string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.cooldown = append(a.cooldown, id)

	return nil
}

func (a *imageAccounts) Quota(context.Context, string) (map[string]float64, error) {
	return map[string]float64{}, nil
}
func (a *imageAccounts) NoteQuota(string, float64)              {}
func (a *imageAccounts) NoteAnswer(string, string, http.Header) {}

func (a *imageAccounts) cooled() []string {
	a.mu.Lock()
	defer a.mu.Unlock()

	return slices.Clone(a.cooldown)
}

// imageGateway is a gateway whose Codex and Antigravity accounts are served
// by the fake backends, with a key any image request may use.
type imageGateway struct {
	chatGateway

	server *modelgateway.Server
}

func newImageGateway(t *testing.T, subs modelgateway.Subscriptions, codexURL, antigravityURL string) imageGateway {
	t.Helper()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	svc := modelgateway.New(db, platform.SystemClock{}, platform.NewID,
		func(context.Context, string) (modelgateway.ProviderInfo, error) {
			return modelgateway.ProviderInfo{}, modelgateway.ErrNotFound
		},
		func(context.Context, string) (string, error) { return "", nil })

	// A key that may use one chat model only: an image is any key's, as a web search.
	_, key, err := svc.AddKey(t.Context(), "tool", []string{"gpt"})
	require.NoError(t, err)

	cfg := modelgateway.Config{Subscriptions: subs, CodexBaseURL: codexURL}
	if antigravityURL != "" {
		cfg.AntigravityBaseURL, cfg.AntigravityManifestURL = antigravityURL, antigravityURL+"/manifest"
	}

	server := modelgateway.NewServer(svc, platform.NewLogger(t.Output(), platform.LogConfig{}, "test"), cfg)
	mux := http.NewServeMux()
	server.Register(mux)

	return imageGateway{chatGateway: chatGateway{svc: svc, mux: mux, key: key}, server: server}
}

// pngBytes are a PNG's first bytes, enough for its type to be read.
var pngBytes = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x0dIHDR fake image")

// codexImageAnswer is an Images route's answer, as codex-rs's test records it.
const codexImageAnswer = `{"created":1778832973,"background":"opaque","data":[{"b64_json":"aW1hZ2U="}],` +
	`"output_format":"png","quality":"medium","size":"1024x1536","usage":{"input_tokens":1474,` +
	`"input_tokens_details":{"image_tokens":1457,"text_tokens":17},"output_tokens":1372,` +
	`"output_tokens_details":{"image_tokens":1372,"text_tokens":0},"total_tokens":2846}}`

// imageBackend starts a backend that records what reaches path and answers
// with handle.
func imageBackend(t *testing.T, path string, handle http.HandlerFunc) (*httptest.Server, *seen) {
	t.Helper()

	got := &seen{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			http.NotFound(w, r)

			return
		}

		raw, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(raw))
		got.add(r)
		r.Body = io.NopCloser(bytes.NewReader(raw))
		handle(w, r)
	}))
	t.Cleanup(srv.Close)

	return srv, got
}

func answerWith(status int, body string, headers ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		for i := 0; i+1 < len(headers); i += 2 {
			w.Header().Set(headers[i], headers[i+1])
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

// Scenario: a client asks for two images and names no model. The Codex
// backend's Images route is called once an image, as Codex and OmniRoute do,
// with the Codex CLI's headers under the account and codex-rs's body with
// gpt-image-2; the answer is OpenAI's, the usage summed, and the request is
// one usage row on the key as image/gpt-image-2 with the tokens Codex
// reported.
func TestCodexMakesImagesThroughItsImagesRoute(t *testing.T) {
	t.Parallel()

	backend, got := imageBackend(t, "/images/generations", answerWith(http.StatusOK, codexImageAnswer))
	subs := &imageAccounts{all: []imageAccount{{id: "acc_1", runtime: "codex", plan: "plus"}}}
	g := newImageGateway(t, subs, backend.URL, "")

	status, _, body := g.post(t, "/v1/images/generations", g.key,
		`{"prompt":"a red fox","n":2,"size":"1024x1536","quality":"hd"}`)
	require.Equal(t, http.StatusOK, status, body)
	assert.JSONEq(t, `{"created":1778832973,"data":[{"b64_json":"aW1hZ2U="},{"b64_json":"aW1hZ2U="}],
		"usage":{"input_tokens":2948,"output_tokens":2744,"total_tokens":5692}}`, body)

	require.Equal(t, 2, got.calls(), "one call an image")

	header, sent := got.last(t)
	assert.Equal(t, "Bearer fake-token-acc_1", header.Get("Authorization"))
	assert.Equal(t, "ext-acc_1", header.Get("Chatgpt-Account-Id"))
	assert.Equal(t, "codex_cli_rs", header.Get("Originator"))
	assert.Equal(t, "application/json", header.Get("Accept"))
	assert.NotEmpty(t, header.Get("X-Codex-Image-Turn-Id"))
	assert.JSONEq(t, `{"prompt":"a red fox","model":"gpt-image-2","n":1,"size":"1024x1536","quality":"high"}`, sent)

	u := g.lastUsage(t)
	assert.Equal(t, "image/gpt-image-2", u.Model)
	assert.Equal(t, "codex", u.Provider)
	assert.Equal(t, "acc_1", u.AccountID)
	assert.Equal(t, modelgateway.OutcomeOK, u.Outcome)
	assert.Equal(t, int64(2948), u.Tokens.Prompt)
	assert.Equal(t, int64(2744), u.Tokens.Completion)
	assert.False(t, u.Tokens.Estimated, "no tokens are invented")
}

// Scenario: the first Codex account is on ChatGPT's free plan, which has no
// image model: it is skipped without a call (9router-go, OmniRoute). The
// next answers the backend's not-entitled 400, which moves on as OmniRoute's
// issue #8307 has it; the third makes the image. None is cooled for chat.
func TestCodexSkipsFreePlansAndMovesOnWhenNotEntitled(t *testing.T) {
	t.Parallel()

	backend, got := imageBackend(t, "/images/generations", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer fake-token-team_1" {
			answerWith(http.StatusBadRequest,
				`{"detail":"The 'gpt-image-2' model is not supported when using Codex with a ChatGPT account."}`)(w, r)

			return
		}

		answerWith(http.StatusOK, codexImageAnswer)(w, r)
	})
	subs := &imageAccounts{all: []imageAccount{
		{id: "free_1", runtime: "codex", plan: "free"},
		{id: "team_1", runtime: "codex", plan: "team"},
		{id: "team_2", runtime: "codex", plan: "team"},
	}}
	g := newImageGateway(t, subs, backend.URL, "")

	status, _, body := g.post(t, "/v1/images/generations", g.key, `{"prompt":"a fox"}`)
	require.Equal(t, http.StatusOK, status, body)

	got.mu.Lock()
	tokens := make([]string, 0, len(got.headers))
	for _, h := range got.headers {
		tokens = append(tokens, h.Get("Authorization"))
	}
	got.mu.Unlock()

	assert.Equal(t, []string{"Bearer fake-token-team_1", "Bearer fake-token-team_2"}, tokens,
		"the free account is never called")
	assert.Equal(t, "team_2", g.lastUsage(t).AccountID)
	assert.Empty(t, subs.cooled(), "an image's refusal cools no account for chat")
}

// Scenario: Codex's only account answers 429 with the time its limit resets.
// The client gets 429 with Retry-After; the account's image model then rests
// until that reset, so the next request does not reach the backend at all,
// and its chat is not cooled.
func TestACodexLimitRestsTheImageModelUntilItsReset(t *testing.T) {
	t.Parallel()

	backend, got := imageBackend(t, "/images/generations", answerWith(http.StatusTooManyRequests,
		`{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","resets_in_seconds":600}}`))
	subs := &imageAccounts{all: []imageAccount{{id: "acc_1", runtime: "codex", plan: "plus"}}}
	g := newImageGateway(t, subs, backend.URL, "")
	g.offAntigravity(t)

	status, header, body := g.post(t, "/v1/images/generations", g.key, `{"prompt":"a fox"}`)
	require.Equal(t, http.StatusTooManyRequests, status, body)
	assert.Contains(t, body, "image_rate_limited")
	assert.Contains(t, body, "codex gpt-image-2: The usage limit has been reached")
	assert.InDelta(t, 600, atoi(t, header.Get("Retry-After")), 2)

	status, _, _ = g.post(t, "/v1/images/generations", g.key, `{"prompt":"a fox"}`)
	assert.Equal(t, http.StatusTooManyRequests, status)
	assert.Equal(t, 1, got.calls(), "a resting account is not asked again before its reset")

	rests, err := g.server.ImageRests(t.Context())
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now().Add(10*time.Minute), rests["codex"]["acc_1"], 5*time.Second)
	assert.Empty(t, subs.cooled())

	u := g.lastUsage(t)
	assert.Equal(t, modelgateway.OutcomeUpstreamError, u.Outcome)
	assert.Equal(t, http.StatusTooManyRequests, u.Status)
}

func atoi(t *testing.T, s string) float64 {
	t.Helper()

	var n float64
	require.NoError(t, json.Unmarshal([]byte(s), &n))

	return n
}

func (g imageGateway) offAntigravity(t *testing.T) {
	t.Helper()
	require.NoError(t, g.svc.SetImageProvider(t.Context(), "antigravity", false))
}

// antigravityImageAnswer is generateContent's answer with an image.
const antigravityImageAnswer = `{"response":{"candidates":[{"content":{"parts":[{"text":"A fox in snow."},` +
	`{"inlineData":{"mimeType":"image/jpeg","data":"Zm94"}}]}}],` +
	`"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":1290}}}`

// Scenario: an edit reaches Antigravity, the owner having put it first. The
// request is OmniRoute's: the IDE's envelope with requestType image_gen and
// the account's project, the reference as inlineData before a prompt that
// asks to use it, the size as an aspect ratio, the image size, the count as
// candidates. The image comes back with the model's text as its revised
// prompt.
func TestAntigravityMakesAnImageFromAReference(t *testing.T) {
	t.Parallel()

	backend, got := imageBackend(t, "/v1internal:generateContent", answerWith(http.StatusOK, antigravityImageAnswer))
	subs := &imageAccounts{all: []imageAccount{{id: "g_1", runtime: "antigravity", plan: "g1-pro-tier",
		project: "bright-wave-1234"}}}
	g := newImageGateway(t, subs, "", backend.URL)
	require.NoError(t, g.svc.SetImageOrder(t.Context(), []string{"antigravity", "codex"}))

	ref := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes)
	status, _, body := g.post(t, "/v1/images/edits", g.key,
		`{"prompt":"make it snow","size":"1024x1536","image_size":"2k","input_references":[{"type":"image_url",`+
			`"image_url":{"url":"`+ref+`"}}]}`)
	require.Equal(t, http.StatusOK, status, body)

	var answer struct {
		Data []struct {
			B64JSON       string `json:"b64_json"`
			RevisedPrompt string `json:"revised_prompt"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &answer))
	require.Len(t, answer.Data, 1)
	assert.Equal(t, "Zm94", answer.Data[0].B64JSON)
	assert.Equal(t, "A fox in snow.", answer.Data[0].RevisedPrompt)

	header, sent := got.last(t)
	assert.Equal(t, "Bearer fake-token-g_1", header.Get("Authorization"))

	var req map[string]any
	require.NoError(t, json.Unmarshal([]byte(sent), &req))
	assert.Equal(t, "image_gen", req["requestType"])
	assert.Equal(t, "antigravity", req["userAgent"])
	assert.Equal(t, "gemini-3.1-flash-image", req["model"])
	assert.Equal(t, "bright-wave-1234", req["project"])
	requestID, _ := req["requestId"].(string)
	assert.True(t, strings.HasPrefix(requestID, "image_gen/"), requestID)

	inner, err := json.Marshal(req["request"])
	require.NoError(t, err)
	assert.JSONEq(t, `{"contents":[{"role":"user","parts":[
		{"inlineData":{"mimeType":"image/png","data":"`+base64.StdEncoding.EncodeToString(pngBytes)+`"}},
		{"text":"Generate an image as the final response. Use all 1 attached reference image in the result. `+
		`Do not answer with text only.\n\nmake it snow"}]}],
		"generationConfig":{"candidateCount":1,"imageConfig":{"aspectRatio":"2:3","imageSize":"2K"}}}`, string(inner))

	u := g.lastUsage(t)
	assert.Equal(t, "image/gemini-3.1-flash-image", u.Model)
	assert.Equal(t, int64(1290), u.Tokens.Completion)
}

// Scenario: Antigravity answers with text only: a refusal, which says the
// model's words; and a 429 that names the quota's reset time rests the
// account's image model until then (9router-go's geminiQuotaResetSeconds).
func TestAntigravityTextOnlyIsARefusalAndItsQuotaResetIsKept(t *testing.T) {
	t.Parallel()

	reset := time.Now().Add(47 * time.Minute).UTC().Truncate(time.Second)
	answer := answerWith(http.StatusOK,
		`{"response":{"candidates":[{"content":{"parts":[{"text":"I can't make images of real people."}]}}]}}`)

	var mu sync.Mutex

	backend, _ := imageBackend(t, "/v1internal:generateContent", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		answer(w, r)
	})
	subs := &imageAccounts{all: []imageAccount{{id: "g_1", runtime: "antigravity", project: "p"}}}
	g := newImageGateway(t, subs, "", backend.URL)

	status, _, body := g.post(t, "/v1/images/generations", g.key, `{"prompt":"a person","provider":"antigravity"}`)
	require.Equal(t, http.StatusBadGateway, status, body)
	assert.Contains(t, body, "Antigravity returned text instead of an image: I can't make images of real people.")

	mu.Lock()
	answer = answerWith(http.StatusTooManyRequests, `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED",`+
		`"message":"You have exhausted your capacity on this model.","details":[{"@type":`+
		`"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED","metadata":`+
		`{"quotaResetTimeStamp":"`+reset.Format(time.RFC3339)+`"}}]}}`)
	mu.Unlock()

	status, header, body := g.post(t, "/v1/images/generations", g.key, `{"prompt":"a fox","model":"antigravity/auto"}`)
	require.Equal(t, http.StatusTooManyRequests, status, body)
	assert.NotEmpty(t, header.Get("Retry-After"))

	rests, err := g.server.ImageRests(t.Context())
	require.NoError(t, err)
	assert.WithinDuration(t, reset, rests["antigravity"]["g_1"], 2*time.Second)
	assert.Empty(t, subs.cooled())
}

// Scenario: the request's model chooses the provider and is passed on as it
// is, so a newer model can be tried; the owner's model for a provider is used
// when a request names none; a model the provider does not know comes back
// with its name.
func TestTheModelIsChosenByTheRequestOrTheOwner(t *testing.T) {
	t.Parallel()

	backend, got := imageBackend(t, "/images/generations", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)

		if strings.Contains(string(raw), "gpt-image-9") {
			answerWith(http.StatusNotFound, `{"detail":"Model not found"}`)(w, r)

			return
		}

		answerWith(http.StatusOK, codexImageAnswer)(w, r)
	})
	subs := &imageAccounts{all: []imageAccount{{id: "acc_1", runtime: "codex", plan: "team"}}}
	g := newImageGateway(t, subs, backend.URL, "")
	g.offAntigravity(t)

	status, _, body := g.post(t, "/v1/images/generations", g.key, `{"prompt":"a fox","model":"gpt-image-2.5-flare"}`)
	require.Equal(t, http.StatusOK, status, body)

	_, sent := got.last(t)
	assert.Contains(t, sent, `"model":"gpt-image-2.5-flare"`)
	assert.Equal(t, "image/gpt-image-2.5-flare", g.lastUsage(t).Model)

	require.NoError(t, g.svc.SetImageModel(t.Context(), "codex", "gpt-image-2.6"))

	status, _, body = g.post(t, "/v1/images/generations", g.key, `{"prompt":"a fox","model":"auto"}`)
	require.Equal(t, http.StatusOK, status, body)

	_, sent = got.last(t)
	assert.Contains(t, sent, `"model":"gpt-image-2.6"`)

	status, _, body = g.post(t, "/v1/images/generations", g.key, `{"prompt":"a fox","model":"codex/gpt-image-9"}`)
	assert.Equal(t, http.StatusNotFound, status)
	assert.Contains(t, body, "codex gpt-image-9: Model not found")

	status, _, body = g.post(t, "/v1/images/generations", g.key, `{"prompt":"a fox","model":"dall-e-3"}`)
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Contains(t, body, "names no provider")
}

// Scenario: Codex has no account; Antigravity, next in the order, makes the
// image. A provider switched off is not asked, and naming it is refused.
func TestTheProvidersAreAskedInTheOwnersOrder(t *testing.T) {
	t.Parallel()

	backend, got := imageBackend(t, "/v1internal:generateContent", answerWith(http.StatusOK, antigravityImageAnswer))
	subs := &imageAccounts{all: []imageAccount{{id: "g_1", runtime: "antigravity", project: "p"}}}
	g := newImageGateway(t, subs, "", backend.URL)

	status, _, body := g.post(t, "/v1/images/generations", g.key, `{"prompt":"a fox"}`)
	require.Equal(t, http.StatusOK, status, body)

	u := g.lastUsage(t)
	assert.Equal(t, "antigravity", u.Provider)
	assert.Equal(t, 2, u.Attempts, "Codex was tried first")

	require.NoError(t, g.svc.SetImageProvider(t.Context(), "antigravity", false))

	status, _, body = g.post(t, "/v1/images/generations", g.key, `{"prompt":"a fox"}`)
	assert.Equal(t, http.StatusServiceUnavailable, status, body)
	assert.Contains(t, body, "no Codex account is logged in")
	assert.Equal(t, 1, got.calls(), "a provider that is off is not asked")

	status, _, body = g.post(t, "/v1/images/generations", g.key, `{"prompt":"a fox","provider":"antigravity"}`)
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Contains(t, body, "image provider antigravity is off")
}

// Scenario: an edit comes as a multipart form, its images as image[] files
// as omp and opencode send them; the files reach Codex's edits route as
// data addresses. A reference on the web is refused, not fetched.
func TestAnEditComesAsAForm(t *testing.T) {
	t.Parallel()

	backend, got := imageBackend(t, "/images/edits", answerWith(http.StatusOK, codexImageAnswer))
	subs := &imageAccounts{all: []imageAccount{{id: "acc_1", runtime: "codex", plan: "plus"}}}
	g := newImageGateway(t, subs, backend.URL, "")

	var form bytes.Buffer

	w := multipart.NewWriter(&form)
	require.NoError(t, w.WriteField("prompt", "add a red hat"))
	require.NoError(t, w.WriteField("model", "gpt-image-2"))
	require.NoError(t, w.WriteField("n", "1"))

	file, err := w.CreateFormFile("image[]", "fox.png")
	require.NoError(t, err)
	_, _ = file.Write(pngBytes)
	require.NoError(t, w.Close())

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/images/edits", &form)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+g.key)

	rec := httptest.NewRecorder()
	g.mux.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	_, sent := got.last(t)
	assert.JSONEq(t, `{"prompt":"add a red hat","model":"gpt-image-2","n":1,"images":[{"image_url":`+
		`"data:image/png;base64,`+base64.StdEncoding.EncodeToString(pngBytes)+`"}]}`, sent)

	status, _, body := g.post(t, "/v1/images/edits", g.key,
		`{"prompt":"x","images":[{"image_url":"https://example.com/fox.png"}]}`)
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Contains(t, body, "not fetched")
	assert.Equal(t, 1, got.calls())

	status, _, _ = g.post(t, "/v1/images/edits", "", `{"prompt":"x"}`)
	assert.Equal(t, http.StatusUnauthorized, status)
}

// Scenario: what the owner sets is kept in the database and read back in the
// order set; a model equal to the default is the default, and "" resets it.
func TestTheImageSettingsAreKept(t *testing.T) {
	t.Parallel()

	g := newImageGateway(t, &imageAccounts{}, "", "")
	ctx := t.Context()

	settings, err := g.svc.ImageSettings(ctx)
	require.NoError(t, err)
	assert.Equal(t, []modelgateway.ImageProviderSetting{
		{Provider: "codex", On: true, Model: "gpt-image-2"},
		{Provider: "antigravity", On: true, Model: "gemini-3.1-flash-image"},
	}, settings)

	require.NoError(t, g.svc.SetImageOrder(ctx, []string{"antigravity", "codex"}))
	require.NoError(t, g.svc.SetImageProvider(ctx, "codex", false))
	require.NoError(t, g.svc.SetImageModel(ctx, "antigravity", "gemini-3-pro-image"))

	settings, err = g.svc.ImageSettings(ctx)
	require.NoError(t, err)
	assert.Equal(t, []modelgateway.ImageProviderSetting{
		{Provider: "antigravity", On: true, Model: "gemini-3-pro-image", Chosen: true},
		{Provider: "codex", On: false, Model: "gpt-image-2"},
	}, settings)

	require.NoError(t, g.svc.SetImageModel(ctx, "antigravity", ""))

	settings, err = g.svc.ImageSettings(ctx)
	require.NoError(t, err)
	assert.Equal(t, "gemini-3.1-flash-image", settings[0].Model)
	assert.False(t, settings[0].Chosen)

	require.ErrorIs(t, g.svc.SetImageOrder(ctx, []string{"codex"}), modelgateway.ErrInvalid)
	require.ErrorIs(t, g.svc.SetImageModel(ctx, "codex", "bad model"), modelgateway.ErrInvalid)
	require.ErrorIs(t, g.svc.SetImageProvider(ctx, "claude", true), modelgateway.ErrNotFound)
}
