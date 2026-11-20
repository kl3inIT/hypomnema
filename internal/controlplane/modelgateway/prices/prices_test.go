package prices_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway/prices"
)

func ptr(v int64) *int64 { return &v }

// A request's cost: the input not cached at the input price, what was read
// from and written to the cache at theirs, the output at the output price.
func TestCostChargesEachKindOfTokenAtItsPrice(t *testing.T) {
	t.Parallel()

	p := prices.Price{Input: ptr(2_000_000), Output: ptr(10_000_000), CacheRead: ptr(100_000), CacheWrite: ptr(2_500_000)}

	// 1,000 uncached, 8,000 read, 1,000 written, 500 out:
	// 1000×2 + 8000×0.1 + 1000×2.5 + 500×10 = 10,300 micro-dollars, about one cent.
	got := prices.Cost(p, prices.Tokens{Input: 10_000, CacheRead: 8_000, CacheWrite: 1_000, Output: 500})
	assert.Equal(t, int64(10_300), got)

	// A vendor that gives no cache prices charges the cache at the input price.
	plain := prices.Price{Input: ptr(1_000_000), Output: ptr(1_000_000)}
	assert.Equal(t, int64(2_000), prices.Cost(plain, prices.Tokens{Input: 1_000, CacheRead: 500, Output: 1_000}))
}

// Scenario: a Claude request wrote 1,000 tokens to the cache, 600 of them for
// an hour (issue #448). The hour's writes cost twice the input, as Anthropic
// prices them and LiteLLM's catalog has it; the rest the book's cache_write.
// Another vendor's writes all cost cache_write.
func TestCostPricesAnHoursCacheWriteAtTwiceTheInput(t *testing.T) {
	t.Parallel()

	sonnet := prices.Price{
		Vendor: "anthropic", Input: ptr(3_000_000), Output: ptr(15_000_000),
		CacheRead: ptr(300_000), CacheWrite: ptr(3_750_000),
	}
	tokens := prices.Tokens{Input: 1_000, CacheWrite: 1_000, CacheWrite1h: 600}

	// 400×3.75 + 600×6 = 5,100 micro-dollars.
	assert.Equal(t, int64(5_100), prices.Cost(sonnet, tokens))

	other := sonnet
	other.Vendor = "openai"
	assert.Equal(t, int64(3_750), prices.Cost(other, tokens), "1000×3.75")
}

// The model asked for is priced before the provider's own, under the
// provider's vendor; Antigravity's Claude is priced as Anthropic's by its name;
// a model the book does not know has no price.
func TestLookupFollowsTheAskedModelThenTheProvidersModel(t *testing.T) {
	t.Parallel()

	p, ok := prices.Lookup("claude_subscription", "claude-haiku-5-5", "claude-opus-5-5")
	require.True(t, ok)
	assert.Equal(t, "claude-haiku-5-5", p.Model)

	p, ok = prices.Lookup("claude_subscription", "small", "claude-sonnet-5-5")
	require.True(t, ok, "a name the book does not know falls back to the provider's model")
	assert.Equal(t, "claude-sonnet-5-5", p.Model)

	p, ok = prices.Lookup("codex_subscription", "gpt-6.1-sol")
	require.True(t, ok)
	assert.Equal(t, "openai", p.Vendor)

	p, ok = prices.Lookup("antigravity_subscription", "claude-sonnet", "claude-sonnet-4-6")
	require.True(t, ok)
	assert.Equal(t, "anthropic", p.Vendor)

	_, ok = prices.Lookup("openai_compatible", "deepseek-v4")
	assert.False(t, ok)
}

func TestOpenCodeGoListPricesAreProviderSpecific(t *testing.T) {
	t.Parallel()

	for _, want := range []struct {
		model               string
		input, output, read int64
	}{
		{"deepseek-v4.1-flash", 150_000, 600_000, 3_000},
		{"glm-5.3", 1_400_000, 4_400_000, 260_000},
		{"glm-5.3-flash", 150_000, 500_000, 30_000},
	} {
		got, ok := prices.Lookup("opencode_go", "opencode-go/"+want.model, want.model)
		require.True(t, ok, want.model)
		assert.Equal(t, prices.Price{
			Vendor: "opencode-go", Model: want.model, Input: ptr(want.input), Output: ptr(want.output),
			CacheRead: ptr(want.read),
		}, got)

		_, ok = prices.Lookup("openai_compatible", want.model)
		assert.False(t, ok, want.model)
	}

	_, ok := prices.Find("opencode_go", "glm-5.3")
	assert.False(t, ok, "Go's price rows do not seed the model catalog")
}

func TestAntigravityFlashEffortPricesFallBackToTheBasePrice(t *testing.T) {
	t.Parallel()

	base, ok := prices.Lookup("antigravity_subscription", "gemini-3.8-flash")
	require.True(t, ok)

	for _, model := range []string{"gemini-3.8-flash-high", "gemini-3.8-flash-medium", "gemini-3.8-flash-low"} {
		got, ok := prices.Lookup("antigravity_subscription", "flash", model)
		require.True(t, ok, model)
		assert.Equal(t, base, got)

		_, ok = prices.Lookup("openai_compatible", model)
		assert.False(t, ok, model)
	}

	_, ok = prices.Lookup("antigravity_subscription", "gemini-3.8-flash-unknown")
	assert.False(t, ok)
}

// What models.dev says of a model is found as its price is: a Codex model
// takes images and has its window, its largest output and its effort levels.
func TestFindGivesWhatModelsDevSaysOfAModel(t *testing.T) {
	t.Parallel()

	m, ok := prices.Find("codex_subscription", "gpt-6.1-sol")
	require.True(t, ok)
	require.NotNil(t, m.Context)
	assert.Positive(t, *m.Context)
	require.NotNil(t, m.MaxOutput)
	assert.Equal(t, []string{"text", "image"}, m.Inputs)
	require.NotNil(t, m.Reasoning)
	assert.True(t, *m.Reasoning)
	assert.NotEmpty(t, m.Efforts)

	_, ok = prices.Find("openai_compatible", "deepseek-v4")
	assert.False(t, ok)
}

// Scenario (issues #723 and #972): the prices by unit are under the names
// the gateway records: a provider's search by the query, a transcription by
// the second, speech by the character, and a model's own web search by the
// search, found under the provider's vendor as a token price is. Exa falls
// back to its base API rate. A cost is rounded to the micro-dollar.
func TestThePricesByUnitMatchTheProvidersRates(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		unit, name string
		nanos      int64
	}{
		{prices.UnitQuery, "exa/search", 7_000_000},
		{prices.UnitQuery, "tavily/search", 8_000_000},
		{prices.UnitQuery, "parallel/search", 5_000_000},
		{prices.UnitSecond, "elevenlabs/scribe_v2", 61_100},
		{prices.UnitSecond, "soniox/stt-async-v5", 27_778},
		{prices.UnitCharacter, "elevenlabs/eleven_multilingual_v2", 80_000},
	} {
		p, ok := prices.LookupUnit(tt.unit, tt.name)
		require.True(t, ok, tt.name)
		assert.Equal(t, tt.nanos, p.Nanos, tt.name)
	}

	_, ok := prices.LookupUnit(prices.UnitCharacter, "soniox/tts-rt-v2")
	assert.False(t, ok)

	search, ok := prices.LookupWebSearch("codex_subscription", "gpt-6.1-sol")
	require.True(t, ok)
	assert.Equal(t, "openai/gpt-6.1-sol", search.Name)
	assert.Equal(t, int64(30_000), prices.UnitCost(search, 3000), "three searches at $0.01")
	assert.Equal(t, int64(2_500), prices.UnitCost(prices.UnitPrice{Nanos: 27_778}, 90_000), "90 s, to the micro-dollar")
}
