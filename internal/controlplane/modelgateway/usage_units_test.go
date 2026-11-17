package modelgateway_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/speech"
	"github.com/kl3inIT/hypomnema/internal/controlplane/websearch"
)

// Scenario (issue #723, decisions 3.2 to 3.4; the owner's decision 2 of
// 2026-11-18): what is not counted in tokens is counted in its own unit and
// priced at LiteLLM's price of that unit. A Tavily search is one search at
// $0.008; an Exa search one search LiteLLM has no price for, so unpriced; a
// page read and a search that failed count nothing and cost nothing, the
// failure keeping its error. Ninety seconds heard by Soniox cost its price a
// second, 1,200 characters spoken by ElevenLabs its price a character, and
// an image request counts the images it made. The report's totals add the
// units up. No row holds what was searched, read, heard or said.
func TestEachKindIsCountedInItsOwnUnitAndPriced(t *testing.T) {
	t.Parallel()

	g, _ := webGateway(t, exaFound)
	ctx := t.Context()

	k, err := g.svc.Authenticate(ctx, g.key)
	require.NoError(t, err)

	failed := errors.New("tavily (rate limited)")

	for _, c := range []modelgateway.WebCall{
		{Model: modelgateway.ModelWebSearch, Provider: websearch.ProviderTavily, Attempts: 1},
		{Model: modelgateway.ModelWebSearch, Provider: websearch.ProviderExa, Attempts: 2},
		{Model: modelgateway.ModelWebSearch, Provider: websearch.ProviderTavily, Attempts: 1, Err: failed},
		{Model: modelgateway.ModelWebFetch, Attempts: 1},
	} {
		c.KeyID = k.ID
		require.NoError(t, g.svc.RecordWebCall(ctx, c))
	}

	require.NoError(t, g.svc.RecordAudioCall(ctx, modelgateway.AudioCall{
		KeyID: k.ID, Kind: speech.KindSTT, Provider: speech.ProviderSoniox, Model: speech.SonioxSTTModel,
		Audio: 90 * time.Second,
	}))
	require.NoError(t, g.svc.RecordAudioCall(ctx, modelgateway.AudioCall{
		KeyID: k.ID, Kind: speech.KindTTS, Provider: speech.ProviderElevenLabs, Model: speech.ElevenLabsTTSModel,
		Characters: 1200,
	}))
	require.NoError(t, g.svc.RecordImageCall(ctx, modelgateway.ImageCall{
		KeyID: k.ID, Result: modelgateway.ImageResult{
			Provider: "codex", Model: "gpt-image-2",
			Images: []modelgateway.GeneratedImage{{B64JSON: "AA=="}, {B64JSON: "AQ=="}},
		},
	}))

	rows, _, err := g.svc.UsageRequests(ctx, "24h", false, 50, 0)
	require.NoError(t, err)
	require.Len(t, rows, 7)

	row := func(model, provider, errText string) modelgateway.Usage {
		t.Helper()

		i := slices.IndexFunc(rows, func(u modelgateway.Usage) bool {
			return u.Model == model && u.Provider == provider && u.Error == errText
		})
		require.GreaterOrEqual(t, i, 0, "%s %s %q", model, provider, errText)

		return rows[i]
	}

	cost := func(u modelgateway.Usage) any {
		if u.CostMicros == nil {
			return nil
		}

		return *u.CostMicros
	}

	tavily := row(modelgateway.ModelWebSearch, "tavily", "")
	assert.Equal(t, int64(1), tavily.WebSearches)
	assert.Equal(t, int64(8000), cost(tavily), "$0.008 a query")
	assert.Equal(t, "tavily/search", tavily.PricedModel)

	exa := row(modelgateway.ModelWebSearch, "exa", "")
	assert.Equal(t, int64(1), exa.WebSearches)
	assert.Nil(t, cost(exa), "LiteLLM has no price for Exa's search")

	refused := row(modelgateway.ModelWebSearch, "tavily", "tavily (rate limited)")
	assert.Zero(t, refused.WebSearches)
	assert.Nil(t, cost(refused))

	fetched := row(modelgateway.ModelWebFetch, "", "")
	assert.Zero(t, fetched.WebSearches)
	assert.Nil(t, cost(fetched))

	heard := row("audio/stt-async-v5", "soniox", "")
	assert.Equal(t, 90*time.Second, heard.Audio)
	assert.Equal(t, int64(2500), cost(heard), "90 s at $0.0000277778 a second")

	spoken := row("audio/eleven_multilingual_v2", "elevenlabs", "")
	assert.Equal(t, int64(1200), spoken.Characters)
	assert.Equal(t, int64(96000), cost(spoken), "1,200 characters at $0.00008 each")

	images := row("image/gpt-image-2", "codex", "")
	assert.Equal(t, int64(2), images.Images)

	report, err := g.svc.Usage(ctx, "24h", 0)
	require.NoError(t, err)
	assert.Equal(t, int64(2), report.Totals.WebSearches)
	assert.Equal(t, 90*time.Second, report.Totals.Audio)
	assert.Equal(t, int64(1200), report.Totals.Characters)
	assert.Equal(t, int64(2), report.Totals.Images)
	assert.Equal(t, int64(8000+2500+96000), report.Totals.CostMicros)
	assert.Equal(t, int64(1), report.Totals.Unpriced, "Exa's search")
	assert.Equal(t, int64(2), report.Buckets[23].Images, "the images are in the last hour's bar")

	i := slices.IndexFunc(report.ByModel, func(m modelgateway.UsageGroup) bool {
		return m.Name == modelgateway.ModelWebSearch
	})
	require.GreaterOrEqual(t, i, 0)
	assert.Equal(t, int64(2), report.ByModel[i].WebSearches)
}

// Scenario (incoming voice): a chat's newest key is found whether it works
// or not, so a voice message heard before the chat's pi starts is recorded on
// the key it had last; a chat that never had one has none.
func TestAChatsNewestKeyIsFoundWorkingOrNot(t *testing.T) {
	t.Parallel()

	g, _ := webGateway(t, exaFound)
	ctx := t.Context()

	_, err := g.svc.ChatKey(ctx, "cht_none")
	require.ErrorIs(t, err, modelgateway.ErrNotFound)

	first, _, err := g.svc.AddChatKey(ctx, "cht_1", "bot_1", "")
	require.NoError(t, err)

	second, _, err := g.svc.AddChatKey(ctx, "cht_1", "bot_1", "")
	require.NoError(t, err)
	require.NoError(t, g.svc.RevokeKey(ctx, second.ID))

	k, err := g.svc.ChatKey(ctx, "cht_1")
	require.NoError(t, err)
	assert.Equal(t, second.ID, k.ID, "the newest, revoked when its pi ended")
	assert.NotEqual(t, first.ID, k.ID)
}
