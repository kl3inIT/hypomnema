package controlplane

import (
	"context"
	"io"
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

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/speech"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/compose"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/ffmpeg"
	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

type usageTake string

func (id usageTake) Get(string) (devicetool.Take, error) {
	return devicetool.Take{ID: string(id), Title: "Demo", State: devicetool.TakeAvailable, DurationMs: 6000,
		Segments: []devicetool.TakeSegment{{File: "seg-001.mp4", LengthMs: 6000}},
		Steps:    []devicetool.TakeStep{{AtMs: 500, Text: "Mở ứng dụng"}, {AtMs: 3000, Text: "Chạm nút"}},
	}, nil
}

func (usageTake) File(_, name string) (string, error) { return name, nil }

type usageMovieMaker struct{}

func (usageMovieMaker) Compose(_ context.Context, _ ffmpeg.Tools, in compose.Input) (compose.Result, error) {
	return compose.Result{LengthMs: 4000, Scenes: []compose.SceneResult{
		{OutLengthMs: 2000}, {OutStartMs: 2000, OutLengthMs: 2000},
	}}, os.WriteFile(in.Out, []byte("movie"), 0o600)
}

func (usageMovieMaker) Burn(_ context.Context, _ ffmpeg.Tools, video, _, _ string) (string, error) {
	return video, nil
}

func (usageMovieMaker) Thumbnail(_ context.Context, _ ffmpeg.Tools, _, out string, _ int64) error {
	return os.WriteFile(out, []byte("thumbnail"), 0o600)
}

func TestVideoRevisionsRecordOnlyNewAudioCallsOnTheRequestersKey(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db, err := sqlite.Open(ctx, sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	// The fake maker draws no caption preset: the default look keeps the captions made before the presets (#937).
	_, err = db.ExecContext(ctx, `UPDATE video_settings SET look = '{"captions":{"preset":"legacy"},`+
		`"frame":{"chrome":"full"}}'`)
	require.NoError(t, err)

	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/text-to-speech/"):
			_, _ = io.WriteString(w, "clip")
		case r.URL.Path == "/v1/speech-to-text":
			_, _ = io.WriteString(w, `{"text":"Chạm","words":[{"text":"Chạm","type":"word","start":0,"end":0.3}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(provider.Close)

	const takeID = "tk_00000000000000aa"
	gateway := modelgateway.New(db, platform.SystemClock{}, platform.NewID, nil, nil)
	voice := speech.New(speech.Config{ElevenLabsURL: provider.URL, Keys: func(context.Context) ([]speech.Key, error) {
		return []speech.Key{{Provider: speech.ProviderElevenLabs, Name: "test", Value: "test"}}, nil
	}})
	svc, err := videos.New(videos.Config{
		DB: db, Dir: filepath.Join(t.TempDir(), "videos"), Takes: usageTake(takeID), Maker: usageMovieMaker{},
		Speech: videoAudio{speech: voice, gateway: gateway, logger: slog.Default()},
		Settings: func(context.Context) (speech.Settings, error) {
			return speech.Settings{Off: []string{speech.ProviderSoniox}}, nil
		},
		Tools:     func(context.Context) (ffmpeg.Tools, error) { return ffmpeg.Tools{}, nil },
		FreeBytes: func(string) (uint64, error) { return 1 << 40, nil },
	})
	require.NoError(t, err)
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); svc.Run(runCtx) }()
	t.Cleanup(func() { stop(); <-done })

	v, _, err := svc.Make(ctx, videos.MakeRequest{Take: takeID, By: "d-dsp_test"})
	require.NoError(t, err)
	awaitReview := func(rev int) {
		t.Helper()
		require.EventuallyWithT(t, func(collect *assert.CollectT) {
			got, err := svc.Get(ctx, v.ID)
			if assert.NoError(collect, err) && assert.Len(collect, got.Revisions, rev) {
				assert.Equal(collect, videos.Review, got.Revisions[rev-1].State, "%+v", got.Revisions[rev-1])
			}
		}, 5*time.Second, 10*time.Millisecond)
	}
	awaitReview(1)

	workerKey, err := gateway.DispatchUsageKey(ctx, "dsp_test")
	require.NoError(t, err)
	require.NotNil(t, workerKey.RevokedAt, "a usage-only key must not be usable")
	checkUsage := func(rev int, key string, count int) {
		t.Helper()
		rows, err := gateway.ListUsage(ctx, 20)
		require.NoError(t, err)
		speechCalls, transcriptionCalls := 0, 0
		for _, row := range rows {
			assert.Equal(t, v.ID, row.VideoID)
			if row.VideoRev != rev {
				continue
			}
			assert.Equal(t, key, row.KeyID)
			assert.Equal(t, modelgateway.OutcomeOK, row.Outcome)
			switch row.UpstreamModel {
			case speech.ElevenLabsTTSModel:
				speechCalls++
				assert.Positive(t, row.Characters)
			case speech.ElevenLabsSTTModel:
				transcriptionCalls++
				assert.Equal(t, 300*time.Millisecond, row.Audio)
			default:
				t.Errorf("unexpected audio model %s", row.UpstreamModel)
			}
		}
		assert.Equal(t, count, speechCalls)
		assert.Equal(t, count, transcriptionCalls)
	}
	checkUsage(1, workerKey.ID, 2)

	_, err = svc.RequestChanges(ctx, v.ID, 1, videos.ChangeRequest{Lines: []string{"Mở ứng dụng", "Bấm đăng nhập"}})
	require.NoError(t, err)
	awaitReview(2)
	ownerKey, err := gateway.VideosKey(ctx)
	require.NoError(t, err)
	checkUsage(2, ownerKey, 1)
	rows, err := gateway.ListUsage(ctx, 20)
	require.NoError(t, err)
	require.Len(t, rows, 6)
}
