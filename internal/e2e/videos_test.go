package e2e

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/agentapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/speech"
	"github.com/kl3inIT/hypomnema/internal/controlplane/takes"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/compose"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/ffmpeg"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

// videoSpeech speaks a line as its text and hears a clip as its words.
type videoSpeech struct{}

func (videoSpeech) Speak(_ context.Context, _ videos.AudioSource, req speech.SpeakRequest) (speech.Speech, error) {
	return speech.Speech{Audio: []byte(req.Input), Provider: req.Provider}, nil
}

func (videoSpeech) Transcribe(_ context.Context, _ videos.AudioSource, req speech.TranscribeRequest,
) (speech.Transcript, error) {
	r, err := req.Audio.Open()
	if err != nil {
		return speech.Transcript{}, err
	}

	defer r.Close() //nolint:errcheck // a test's reader

	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)

	t := speech.Transcript{Provider: speech.ProviderElevenLabs}
	for i, w := range strings.Fields(buf.String()) {
		at := time.Duration(i) * 300 * time.Millisecond
		t.Words = append(t.Words, speech.Word{Word: w, Start: at, End: at + 250*time.Millisecond})
	}

	return t, nil
}

// videoMaker writes files that say what they are, in place of ffmpeg.
type videoMaker struct{}

func (videoMaker) Compose(_ context.Context, _ ffmpeg.Tools, in compose.Input) (compose.Result, error) {
	res := compose.Result{Size: compose.Size{Width: 720, Height: 1280}, LengthMs: 2600}
	for k := range in.Voice {
		res.Scenes = append(res.Scenes, compose.SceneResult{OutStartMs: int64(k) * 1300, OutLengthMs: 1300})
	}

	return res, os.WriteFile(in.Out, []byte("an MP4 of the take"), 0o600)
}

func (videoMaker) Burn(_ context.Context, _ ffmpeg.Tools, video, _, work string) (string, error) {
	raw, err := os.ReadFile(video) //nolint:gosec // the test's own file
	if err != nil {
		return "", err
	}

	out := filepath.Join(work, "burned.mp4")
	if err := os.MkdirAll(work, 0o700); err != nil {
		return "", err
	}

	return out, os.WriteFile(out, append(raw, " with captions"...), 0o600) //nolint:gosec // The test's work folder.
}

func (videoMaker) Thumbnail(_ context.Context, _ ffmpeg.Tools, _, out string, _ int64) error {
	return os.WriteFile(out, []byte{0xFF, 0xD8, 0xFF, 0xE0}, 0o600)
}

// videoRig is a control plane that makes videos: a take a worker filmed, and one a Bot's pi filmed.
type videoRig struct {
	t      *testing.T
	srv    *httptest.Server
	worker string
	pi     string
	// plain is a pi whose Bot has the phone off: it has no video tool of the phone's, but video_make and video_get,
	// which the browser's family gives too (issue #1189).
	plain    string
	agentKey string
	owner    string
	coord    string
	take     string
	piTake   string
}

// availableTake records a take its node uploaded whole.
func availableTake(t *testing.T, store *takes.Store, id, agent string) {
	t.Helper()

	require.NoError(t, store.Create(devicetool.Take{ID: id, Title: "Đăng nhập", Agent: agent, Node: "nod_1",
		StartedAt: time.Now(), State: devicetool.TakeRecording}))

	seg := []byte("frames")
	segSum := sha256.Sum256(seg)
	require.NoError(t, store.Receive("nod_1", id, "seg-001.mp4", int64(len(seg)), hex.EncodeToString(segSum[:]),
		bytes.NewReader(seg)))

	raw, err := json.Marshal(devicetool.Take{ID: id, State: devicetool.TakeUploading, DurationMs: 4000,
		Screen: &devicetool.TakeScreen{Width: 1080, Height: 2400},
		Segments: []devicetool.TakeSegment{{File: "seg-001.mp4", LengthMs: 4000, Bytes: 6,
			SHA256: hex.EncodeToString(segSum[:])}},
		Steps: []devicetool.TakeStep{{AtMs: 200, Text: "Mở ứng dụng"},
			{AtMs: 2000, Text: "Chạm vào Đăng nhập", Tap: &devicetool.TakeTap{X: 500, Y: 900}}}})
	require.NoError(t, err)

	sum := sha256.Sum256(raw)
	require.NoError(t, store.Receive("nod_1", id, "take.json", int64(len(raw)), hex.EncodeToString(sum[:]),
		bytes.NewReader(raw)))
}

func newVideoRig(t *testing.T) *videoRig {
	t.Helper()

	ctx := t.Context()

	db, err := sqlite.Open(ctx, sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	// The fake maker draws no caption preset: the default look keeps the captions made before the presets.
	_, err = db.ExecContext(ctx, `UPDATE video_settings SET look = '{"captions":{"preset":"legacy"},`+
		`"frame":{"chrome":"full"}}'`)
	require.NoError(t, err)

	logger := slog.New(slog.DiscardHandler)
	svc := orchestration.New(db, platform.SystemClock{}, platform.NewID)

	store, err := takes.Open(filepath.Join(t.TempDir(), "takes"))
	require.NoError(t, err)

	vs, err := videos.New(videos.Config{
		DB: db, Dir: filepath.Join(t.TempDir(), "videos"), Takes: store, Speech: videoSpeech{}, Maker: videoMaker{},
		Tools: func(context.Context) (ffmpeg.Tools, error) { return ffmpeg.Tools{}, nil },
		Settings: func(context.Context) (speech.Settings, error) {
			return speech.Settings{Voices: map[string]string{speech.ProviderSoniox: "Linh"}}, nil
		},
		Logger: logger,
	})
	require.NoError(t, err)

	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	done := make(chan struct{})

	go func() {
		defer close(done)

		vs.Run(runCtx)
	}()

	t.Cleanup(func() {
		cancel()
		<-done
	})

	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })

	mux := http.NewServeMux()
	agentapi.New(svc, nil, logger).WithVideos(vs, store).Register(mux)
	require.NoError(t, clientapi.New(svc, logger, stop).WithTakes(ownerTakes{store}).WithVideos(vs).Register(mux))

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// The video tools are switched with the phone's family, which a worker's profile and a Bot turn on.
	worker, dispatch := phoneDispatch(t, svc)

	_, owner, err := svc.AddToken(ctx, "owner", bearertoken.Owner)
	require.NoError(t, err)

	_, coord, err := svc.AddToken(ctx, "pi", bearertoken.Coordinator)
	require.NoError(t, err)

	bot, err := svc.PutBot(ctx, orchestration.Bot{
		Name:      "filmer",
		AgentSpec: orchestration.AgentSpec{Tools: []string{"phone"}},
	})
	require.NoError(t, err)

	chat, err := svc.CreateChat(ctx, bot.ID, "")
	require.NoError(t, err)

	pi, err := svc.AddBotToken(ctx, "pi", chat.ID, bot.ID)
	require.NoError(t, err)

	other, err := svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	plain, err := svc.AddBotToken(ctx, "pi", other.ID, orchestration.DefaultBotID)
	require.NoError(t, err)

	r := &videoRig{t: t, srv: srv, worker: worker, pi: pi, plain: plain, owner: owner, coord: coord,
		take: "tk_00000000000000aa", piTake: "tk_00000000000000bb", agentKey: "d-" + dispatch}
	availableTake(t, store, r.take, r.agentKey)
	availableTake(t, store, r.piTake, "b-"+chat.ID)

	return r
}

// tool is an agent's call of one of its tools: the text answered, and whether it is an error.
func (r *videoRig) tool(token, name, args string) (string, bool) {
	r.t.Helper()

	body, err := json.Marshal(agenttool.BrowserCall{Tool: name, Arguments: json.RawMessage(args)})
	require.NoError(r.t, err)

	req, err := http.NewRequestWithContext(r.t.Context(), http.MethodPost, r.srv.URL+agenttool.PathToolsCall,
		bytes.NewReader(body))
	require.NoError(r.t, err)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(r.t, err)

	defer resp.Body.Close() //nolint:errcheck // a test's response body

	require.Equal(r.t, http.StatusOK, resp.StatusCode)

	var answer struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	require.NoError(r.t, json.NewDecoder(resp.Body).Decode(&answer))
	require.Len(r.t, answer.Content, 1)

	return answer.Content[0].Text, answer.IsError
}

// tools are the names of an agent's tools.
func (r *videoRig) tools(token string) []string {
	r.t.Helper()

	req, err := http.NewRequestWithContext(r.t.Context(), http.MethodGet, r.srv.URL+agenttool.PathTools, nil)
	require.NoError(r.t, err)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(r.t, err)

	defer resp.Body.Close() //nolint:errcheck // a test's response body

	var list agenttool.BrowserToolList
	require.NoError(r.t, json.NewDecoder(resp.Body).Decode(&list))

	var names []string
	for _, tl := range list.Data {
		names = append(names, tl.Name)
	}

	return names
}

func (r *videoRig) api(token, method, path, body string, header http.Header) (int, http.Header, []byte) {
	r.t.Helper()

	req, err := http.NewRequestWithContext(r.t.Context(), method, r.srv.URL+clientapi.BasePath+path,
		strings.NewReader(body))
	require.NoError(r.t, err)

	req.Header = header.Clone()
	if req.Header == nil {
		req.Header = http.Header{}
	}

	req.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := http.DefaultClient.Do(req)
	require.NoError(r.t, err)

	defer resp.Body.Close() //nolint:errcheck // a test's response body

	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)

	return resp.StatusCode, resp.Header, buf.Bytes()
}

// videoID reads the video's id from video_make's answer.
func videoID(t *testing.T, text string) string {
	t.Helper()

	id, _, ok := strings.Cut(strings.TrimPrefix(text, "Video "), " ")
	require.True(t, ok, text)
	require.True(t, strings.HasPrefix(id, "vid_"), text)

	return id
}

// Scenario: a worker makes the take it filmed into a video (issue #818). video_make answers at once with the
// video's id, made in the background apart from the phone; video_get follows it to review. A Bot's pi neither
// makes nor reads a video of a take it did not film, and no agent reaches the owner's routes. The owner reads the
// video, plays a range of its MP4, sees its first frame, asks for changes with an edited line, approves the new
// revision and deletes the video.
func TestAWorkerMakesAVideoTheOwnerApproves(t *testing.T) {
	t.Parallel()

	r := newVideoRig(t)

	for _, token := range []string{r.worker, r.pi} {
		names := r.tools(token)
		assert.Contains(t, names, devicetool.VideoMake)
		assert.Contains(t, names, devicetool.VideoGet)
	}

	plain := r.tools(r.plain)
	assert.Contains(t, plain, devicetool.VideoMake, "the browser's family gives it, to make a browser take")

	text, failed := r.tool(r.pi, devicetool.VideoMake, `{"take":"`+r.take+`"}`)
	assert.True(t, failed)
	assert.Equal(t, "You have no take "+r.take+".", text, "a take another agent filmed is not the pi's")

	text, failed = r.tool(r.worker, devicetool.VideoMake, `{"take":"`+r.take+`","request_id":"m1"}`)
	require.False(t, failed, text)
	assert.Contains(t, text, "revision 1 is ", "answered at once, queued or being made")

	id := videoID(t, text)

	again, failed := r.tool(r.worker, devicetool.VideoMake, `{"take":"`+r.take+`","request_id":"m1"}`)
	require.False(t, failed)
	assert.Contains(t, again, "Video "+id+" of take "+r.take+": revision 1", "the same request id, the same revision")

	require.Eventually(t, func() bool {
		text, _ := r.tool(r.worker, devicetool.VideoGet, `{"video":"`+id+`"}`)

		return strings.Contains(text, "revision 1: review")
	}, 10*time.Second, 20*time.Millisecond)

	text, _ = r.tool(r.worker, devicetool.VideoGet, `{"video":"`+id+`"}`)
	assert.Contains(t, text, "2.6s long")
	assert.Contains(t, text, "1. Mở ứng dụng\n2. Chạm vào Đăng nhập")
	assert.NotContains(t, text, ".mp4", "no file and no link")

	text, failed = r.tool(r.pi, devicetool.VideoGet, `{"video":"`+id+`"}`)
	assert.True(t, failed)
	assert.Equal(t, "You have no video "+id+".", text)

	base := "/videos/" + id
	rev1 := base + "/revisions/1"

	// No agent's token reaches the owner's routes, files and thumbnails included.
	for _, token := range []string{r.worker, r.pi, r.coord} {
		for _, c := range []struct{ method, path, body string }{
			{http.MethodGet, "/videos", ""},
			{http.MethodPost, "/videos", `{"take":"` + r.take + `"}`},
			{http.MethodGet, base, ""},
			{http.MethodGet, rev1 + "/video.mp4", ""},
			{http.MethodGet, rev1 + "/thumbnail.jpg", ""},
			{http.MethodPost, rev1 + "/approve", ""},
			{http.MethodPost, rev1 + "/changes", `{"note":"x"}`},
			{http.MethodPost, rev1 + "/look", `{"look":{"frame":{"chrome":"none"}}}`},
			{http.MethodPost, rev1 + "/retry", ""},
			{http.MethodGet, "/videos/looks", ""},
			{http.MethodPost, "/videos/preview", `{"take":"` + r.take + `"}`},
			{http.MethodDelete, base, ""},
			{http.MethodGet, "/studio/items", ""},
			{http.MethodGet, "/takes/" + r.take + "/thumbnail.jpg", ""},
		} {
			// A request as the owner would send it, its Idempotency-Key included: refused for who sends it.
			status, _, _ := r.api(token, c.method, c.path, c.body, http.Header{"Idempotency-Key": {"k1"}})
			assert.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, status, "%s %s", c.method, c.path)
		}
	}

	status, _, body := r.api(r.owner, http.MethodGet, base, "", nil)
	require.Equal(t, http.StatusOK, status, string(body))

	var video struct {
		Take      string `json:"take"`
		MadeBy    string `json:"made_by"`
		Revisions []struct {
			State     string `json:"state"`
			Provider  string `json:"provider"`
			UsedVoice string `json:"used_voice"`
		} `json:"revisions"`
	}
	require.NoError(t, json.Unmarshal(body, &video))
	assert.Equal(t, r.take, video.Take)
	assert.Equal(t, r.agentKey, video.MadeBy)
	require.Len(t, video.Revisions, 1)
	assert.Equal(t, "review", video.Revisions[0].State)
	assert.Equal(t, "soniox", video.Revisions[0].Provider)
	assert.Equal(t, "Linh", video.Revisions[0].UsedVoice)

	status, header, body := r.api(r.owner, http.MethodGet, rev1+"/video.mp4", "", http.Header{"Range": {"bytes=3-5"}})
	require.Equal(t, http.StatusPartialContent, status, string(body))
	assert.Equal(t, "MP4", string(body))
	assert.Equal(t, "video/mp4", header.Get("Content-Type"))
	assert.Equal(t, "bytes", header.Get("Accept-Ranges"))

	status, header, body = r.api(r.owner, http.MethodGet, rev1+"/thumbnail.jpg", "", nil)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, "image/jpeg", header.Get("Content-Type"))
	assert.Equal(t, []byte{0xFF, 0xD8, 0xFF, 0xE0}, body)

	status, _, _ = r.api(r.owner, http.MethodGet, base+"/revisions/2/video.mp4", "", nil)
	assert.Equal(t, http.StatusNotFound, status)

	// The Studio lists the video, in review, with its first frame, and the two takes with theirs (issue #818,
	// row 12); a take's first frame is made for the owner.
	status, _, body = r.api(r.owner, http.MethodGet, "/studio/items?kind=video&state=review", "", nil)
	require.Equal(t, http.StatusOK, status, string(body))

	var studio struct {
		Data []struct {
			ID        string `json:"id"`
			State     string `json:"state"`
			Take      string `json:"take"`
			Thumbnail string `json:"thumbnail"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(body, &studio))
	require.Len(t, studio.Data, 1)
	assert.Equal(t, id, studio.Data[0].ID)
	assert.Equal(t, r.take, studio.Data[0].Take)
	assert.Equal(t, "/api/v1"+rev1+"/thumbnail.jpg", studio.Data[0].Thumbnail)

	status, _, body = r.api(r.owner, http.MethodGet, "/studio/items?kind=take", "", nil)
	require.Equal(t, http.StatusOK, status, string(body))
	require.NoError(t, json.Unmarshal(body, &studio))
	require.Len(t, studio.Data, 2)

	status, header, body = r.api(r.owner, http.MethodGet, strings.TrimPrefix(studio.Data[0].Thumbnail, "/api/v1"),
		"", nil)
	require.Equal(t, http.StatusOK, status, string(body))
	assert.Equal(t, "image/jpeg", header.Get("Content-Type"))
	assert.Equal(t, "first frame", string(body))

	// Changes with an edited line: revision 1 is changes_requested with the note, revision 2 is made.
	status, _, body = r.api(r.owner, http.MethodPost, rev1+"/changes",
		`{"lines":["Mở ứng dụng","Chạm nút Đăng nhập"],"note":"Câu 2 ngắn hơn"}`, http.Header{"Idempotency-Key": {"c1"}})
	require.Equal(t, http.StatusOK, status, string(body))

	require.Eventually(t, func() bool {
		text, _ := r.tool(r.worker, devicetool.VideoGet, `{"video":"`+id+`"}`)

		return strings.Contains(text, "revision 2: review")
	}, 10*time.Second, 20*time.Millisecond)

	text, _ = r.tool(r.worker, devicetool.VideoGet, `{"video":"`+id+`"}`)
	assert.Contains(t, text, "The owner's note on revision 1: Câu 2 ngắn hơn")
	assert.Contains(t, text, "2. Chạm nút Đăng nhập")

	status, _, _ = r.api(r.owner, http.MethodPost, rev1+"/approve", "", nil)
	assert.Equal(t, http.StatusConflict, status, "a revision changes were asked of is not approved")

	status, _, body = r.api(r.owner, http.MethodPost, base+"/revisions/2/approve", "", nil)
	require.Equal(t, http.StatusOK, status, string(body))
	assert.Contains(t, string(body), `"approved":2`)

	// The owner makes the pi's take; a second make with the same key answers the same video.
	status, _, body = r.api(r.owner, http.MethodPost, "/videos", `{"take":"`+r.piTake+`","aspect":"16:9"}`,
		http.Header{"Idempotency-Key": {"k1"}})
	require.Equal(t, http.StatusCreated, status, string(body))

	status, _, again2 := r.api(r.owner, http.MethodPost, "/videos", `{"take":"`+r.piTake+`","aspect":"16:9"}`,
		http.Header{"Idempotency-Key": {"k1"}})
	require.Equal(t, http.StatusCreated, status)

	var first, second struct {
		ID        string            `json:"id"`
		Revisions []json.RawMessage `json:"revisions"`
	}
	require.NoError(t, json.Unmarshal(body, &first))
	require.NoError(t, json.Unmarshal(again2, &second))
	assert.Equal(t, first.ID, second.ID)
	assert.Len(t, second.Revisions, 1, "no second revision")

	status, _, _ = r.api(r.owner, http.MethodPost, "/videos", `{"take":"`+r.piTake+`"}`,
		http.Header{"Idempotency-Key": {"k1"}})
	assert.Equal(t, http.StatusConflict, status, "the same key with another request")

	status, _, _ = r.api(r.owner, http.MethodDelete, base, "", nil)
	require.Equal(t, http.StatusNoContent, status)

	status, _, _ = r.api(r.owner, http.MethodGet, base, "", nil)
	assert.Equal(t, http.StatusNotFound, status)

	text, failed = r.tool(r.worker, devicetool.VideoGet, `{"video":"`+id+`"}`)
	assert.True(t, failed)
	assert.Equal(t, "You have no video "+id+".", text)
}

// Scenario: an agent reads the templates and makes its take into a video with app-ad (issue #1230, rows 3-5):
// video_templates lists the five, with their sections' words; video_make with template app-ad answers what it was
// made with, and the owner reads a 9:16 revision with the template's look, the saved default's sections and an end
// card with its call to action over the take's title. The owner reads the templates through the API; no agent does. A template that
// is not one is refused with the list.
func TestAnAgentMakesAVideoFromATemplate(t *testing.T) {
	t.Parallel()

	r := newVideoRig(t)

	for _, token := range []string{r.worker, r.pi, r.plain} {
		assert.Contains(t, r.tools(token), devicetool.VideoTemplates)
	}

	text, failed := r.tool(r.worker, devicetool.VideoTemplates, `{}`)
	require.False(t, failed, text)

	for _, want := range []string{
		"app-ad: App ad, 15-30 s, 9:16, an end card.",
		"feature-announcement: Feature announcement, 30 s, 9:16 or 16:9, no cards.",
		"product-walkthrough: Product walkthrough, 60-90 s, 16:9, a title card and an end card.",
		"onboarding: Onboarding / how-to, 20-60 s, 16:9, no cards.",
		"app-store-preview: App Store preview, 15-30 s, 886x1920, no captions, no cards.",
		"1. Hook, about 10 words (4 s): ",
		`End card: "Tải ứng dụng ngay" over "{title}".`,
		`Title card: "{title}".`,
	} {
		assert.Contains(t, text, want)
	}

	text, failed = r.tool(r.worker, devicetool.VideoMake, `{"take":"`+r.take+`","template":"billboard"}`)
	assert.True(t, failed)
	assert.Contains(t, text, `no template is named "billboard"; the templates are app-ad, feature-announcement`)

	text, failed = r.tool(r.worker, devicetool.VideoMake,
		`{"take":"`+r.take+`","template":"app-ad","request_id":"t1"}`)
	require.False(t, failed, text)
	assert.Contains(t, text, "It is made with the template app-ad: 9:16, an end card. Its 2 lines have 7 words, "+
		"about 6 s with its cards; the template is 15-30 s.")

	id := videoID(t, text)

	require.Eventually(t, func() bool {
		text, _ := r.tool(r.worker, devicetool.VideoGet, `{"video":"`+id+`"}`)

		return strings.Contains(text, "revision 1: review")
	}, 10*time.Second, 20*time.Millisecond)

	status, _, body := r.api(r.owner, http.MethodGet, "/videos/"+id, "", nil)
	require.Equal(t, http.StatusOK, status, string(body))

	var video struct {
		Revisions []struct {
			Aspect   string `json:"aspect"`
			Captions bool   `json:"captions"`
			Look     struct {
				Captions struct {
					Preset string `json:"preset"`
				} `json:"captions"`
				Frame struct {
					Chrome string `json:"chrome"`
				} `json:"frame"`
				Cards *struct {
					Accent string           `json:"accent"`
					Title  *json.RawMessage `json:"title"`
					End    *struct {
						Title      string `json:"title"`
						Subtitle   string `json:"subtitle"`
						DurationMs int64  `json:"duration_ms"`
					} `json:"end"`
				} `json:"cards"`
			} `json:"look"`
		} `json:"revisions"`
	}
	require.NoError(t, json.Unmarshal(body, &video))
	require.Len(t, video.Revisions, 1)

	rev := video.Revisions[0]
	assert.Equal(t, "9:16", rev.Aspect)
	assert.True(t, rev.Captions)
	assert.Equal(t, "legacy", rev.Look.Captions.Preset, "the saved default's captions")
	assert.Equal(t, "full", rev.Look.Frame.Chrome, "and frame")
	require.NotNil(t, rev.Look.Cards)
	assert.Equal(t, "#6c8cff", rev.Look.Cards.Accent)
	assert.Nil(t, rev.Look.Cards.Title, "app-ad has no title card")
	require.NotNil(t, rev.Look.Cards.End)
	assert.Equal(t, "Tải ứng dụng ngay", rev.Look.Cards.End.Title, "the call to action, as aidemo's outro's title")
	assert.Equal(t, "Đăng nhập", rev.Look.Cards.End.Subtitle, "the video's name under it")
	assert.Equal(t, int64(3000), rev.Look.Cards.End.DurationMs)

	status, _, body = r.api(r.owner, http.MethodGet, "/videos/templates", "", nil)
	require.Equal(t, http.StatusOK, status, string(body))

	var list struct {
		Templates []struct {
			ID      string   `json:"id"`
			Aspects []string `json:"aspects"`
			Words   int      `json:"words"`
			Look    struct {
				Frame struct {
					Chrome string `json:"chrome"`
				} `json:"frame"`
			} `json:"look"`
		} `json:"templates"`
	}
	require.NoError(t, json.Unmarshal(body, &list))
	require.Len(t, list.Templates, 5)
	assert.Equal(t, "app-ad", list.Templates[0].ID)
	assert.Equal(t, []string{"9:16"}, list.Templates[0].Aspects)
	assert.Equal(t, 65, list.Templates[0].Words)
	assert.Equal(t, "full", list.Templates[4].Look.Frame.Chrome)

	for _, token := range []string{r.worker, r.pi, r.coord} {
		status, _, _ := r.api(token, http.MethodGet, "/videos/templates", "", nil)
		assert.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, status)
	}
}
