package clientapi

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/compose"
)

// lookVideos answers look samples with the error its test sets, and records what it was sent.
type lookVideos struct {
	Videos

	err  error
	sent []videos.PreviewRequest
}

func (v *lookVideos) Preview(_ context.Context, req videos.PreviewRequest) ([]byte, error) {
	v.sent = append(v.sent, req)
	if v.err != nil {
		return nil, v.err
	}

	return []byte{0xFF, 0xD8, 0xFF, 0xD9}, nil
}

func (v *lookVideos) Looks(context.Context) (videos.Catalog, error) {
	return videos.Catalog{Presets: []videos.PresetInfo{{ID: "legacy", Name: "Classic"}}, Default: videos.DefaultLook()},
		nil
}

// Scenario: the look sample (issue #908, row 14) is the owner's: a JPEG never cached; a look refused by the
// contract or the service is 400, an unknown take 404, a busy sampler 429 with Retry-After, a slow one 504, no
// ffmpeg 503; a Bot's and a coordinator's tokens are refused.
func TestTheLookSampleAnswersItsLimits(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newWorld(t)
	fake := &lookVideos{}
	w.srv.WithVideos(fake)

	owner, _ := w.token("owner", bearertoken.Owner)
	coordinator, _ := w.token("pi", bearertoken.Coordinator)
	chat, err := w.svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	bot := w.piToken("pi", chat.ID, orchestration.DefaultBotID)

	chrome := openapi.ChromeAndroid
	body := openapi.VideoPreview{Take: "tk_0123456789abcdef", Look: &openapi.VideoLookInput{
		Frame: &openapi.VideoFrameInput{Chrome: &chrome},
	}}

	ok, err := owner.PreviewVideoLookWithResponse(ctx, body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, ok.StatusCode(), string(ok.Body))
	assert.Equal(t, "image/jpeg", ok.HTTPResponse.Header.Get("Content-Type"))
	assert.Equal(t, "no-store", ok.HTTPResponse.Header.Get("Cache-Control"))
	assert.Equal(t, []byte{0xFF, 0xD8, 0xFF, 0xD9}, ok.Body)
	require.Len(t, fake.sent, 1)
	require.NotNil(t, fake.sent[0].Look.Frame)
	assert.Equal(t, "android", *fake.sent[0].Look.Frame.Chrome)
	assert.Nil(t, fake.sent[0].Look.Captions, "a section not sent stays unsent")

	for name, c := range map[string]*openapi.ClientWithResponses{"bot": bot, "coordinator": coordinator} {
		refused, err := c.PreviewVideoLookWithResponse(ctx, body)
		require.NoError(t, err)
		assert.Equal(t, http.StatusForbidden, refused.StatusCode(), name)

		looks, err := c.ListVideoLooksWithResponse(ctx)
		require.NoError(t, err)
		assert.Equal(t, http.StatusForbidden, looks.StatusCode(), name)
	}

	bad := openapi.ChromeIPhone
	empty, none := []string{}, ""
	for name, b := range map[string]openapi.VideoPreview{
		"no colors": {Take: body.Take, Look: &openapi.VideoLookInput{Background: &openapi.VideoBackground{
			Colors: &empty,
		}}},
		"a take's id": {Take: "take-1", Look: &openapi.VideoLookInput{Frame: &openapi.VideoFrameInput{Chrome: &bad}}},
		"no background": {Take: body.Take, Look: &openapi.VideoLookInput{Background: &openapi.VideoBackground{
			Preset: &none,
		}}},
	} {
		resp, err := owner.PreviewVideoLookWithResponse(ctx, b)
		require.NoError(t, err)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode(), name)
	}

	for _, c := range []struct {
		err    error
		status int
		code   openapi.ErrorCode
	}{
		{fmt.Errorf("%w: no caption preset is named %q", videos.ErrInvalid, "comic"), http.StatusBadRequest,
			openapi.InvalidRequest},
		{fmt.Errorf("%w: take tk_0123456789abcdef", videos.ErrNotFound), http.StatusNotFound, openapi.NotFound},
		{fmt.Errorf("%w: the phone turned", videos.ErrConflict), http.StatusConflict, openapi.Conflict},
		{videos.ErrBusy, http.StatusTooManyRequests, openapi.RateLimited},
		{fmt.Errorf("%w: longer than 20s", videos.ErrTimedOut), http.StatusGatewayTimeout, openapi.TimedOut},
		{fmt.Errorf("%w: ffmpeg is not installed", videos.ErrUnavailable), http.StatusServiceUnavailable,
			openapi.Unavailable},
	} {
		fake.err = c.err

		resp, err := owner.PreviewVideoLookWithResponse(ctx, body)
		require.NoError(t, err)
		require.Equal(t, c.status, resp.StatusCode(), c.err.Error())
		require.NotNil(t, resp.JSONDefault, c.err.Error())
		assert.Equal(t, c.code, resp.JSONDefault.Code)

		if c.status == http.StatusTooManyRequests {
			assert.Equal(t, "1", resp.HTTPResponse.Header.Get("Retry-After"))
		}
	}

	looks, err := owner.ListVideoLooksWithResponse(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, looks.StatusCode(), string(looks.Body))
	assert.Equal(t, openapi.ChromeAndroid, looks.JSON200.Default.Frame.Chrome)
	assert.Equal(t, []openapi.VideoPreset{{Id: "legacy", Name: "Classic"}}, looks.JSON200.Presets)
}

// A look change and a retry are refused without an Idempotency-Key (#908, row 4; review of #1001): the contract
// requires it, before the videos are asked.
func TestALookChangeAndARetryNeedAnIdempotencyKey(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newWorld(t)
	w.srv.WithVideos(&lookVideos{})
	owner, _ := w.token("owner", bearertoken.Owner)

	look, err := owner.ChangeVideoLookWithResponse(ctx, "vid_abc", 1, &openapi.ChangeVideoLookParams{},
		openapi.VideoLookChange{})
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, look.StatusCode(), string(look.Body))

	retry, err := owner.RetryVideoRevisionWithResponse(ctx, "vid_abc", 1, &openapi.RetryVideoRevisionParams{})
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, retry.StatusCode(), string(retry.Body))
}

// Scenario: the cards (issue #1230, row 1) go to the service as sent, a sample of one card with them; the contract
// refuses a card too short and a sample of no card it knows; a stored look shows its cards.
func TestTheCardsThroughTheAPI(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newWorld(t)
	fake := &lookVideos{}
	w.srv.WithVideos(fake)

	owner, _ := w.token("owner", bearertoken.Owner)
	end := openapi.VideoPreviewCardEnd
	url, accent := "secondbite.vn", "#ff3366"
	body := openapi.VideoPreview{Take: "tk_0123456789abcdef", Card: &end, Look: &openapi.VideoLookInput{
		Cards: &openapi.VideoCardsInput{Accent: &accent, End: &openapi.VideoCardInput{Title: "Tải", CtaUrl: &url}},
	}}

	ok, err := owner.PreviewVideoLookWithResponse(ctx, body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, ok.StatusCode(), string(ok.Body))
	require.Len(t, fake.sent, 1)
	assert.Equal(t, videos.CardEnd, fake.sent[0].Card)
	assert.Equal(t, &videos.CardsInput{Accent: &accent, End: &videos.CardInput{Title: "Tải", CTAURL: &url}},
		fake.sent[0].Look.Cards)

	short, middle := int64(400), openapi.VideoPreviewCard("middle")
	for name, b := range map[string]openapi.VideoPreview{
		"too short": {Take: body.Take, Look: &openapi.VideoLookInput{Cards: &openapi.VideoCardsInput{
			Title: &openapi.VideoCardInput{Title: "A", DurationMs: &short},
		}}},
		"no title":       {Take: body.Take, Look: &openapi.VideoLookInput{Cards: &openapi.VideoCardsInput{Title: &openapi.VideoCardInput{}}}},
		"a card unknown": {Take: body.Take, Card: &middle},
	} {
		resp, err := owner.PreviewVideoLookWithResponse(ctx, b)
		require.NoError(t, err)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode(), name)
	}

	shown := lookOf(videos.Look{Cards: &compose.Cards{Accent: "#6c8cff",
		Title: &compose.Card{Title: "SecondBite", DurationMs: 2600}}})
	assert.Equal(t, &openapi.VideoCards{Accent: "#6c8cff",
		Title: &openapi.VideoCard{Title: "SecondBite", DurationMs: 2600}}, shown.Cards)
	assert.Nil(t, lookOf(videos.Look{}).Cards, "no cards, none shown")
}
