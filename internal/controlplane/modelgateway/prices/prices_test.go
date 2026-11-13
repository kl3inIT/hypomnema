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

// The model asked for is priced before the provider's own, under the
// provider's vendor; Antigravity's Claude is priced as Anthropic's by its name;
// a model the book does not know has no price.
func TestLookupFollowsTheAskedModelThenTheProvidersModel(t *testing.T) {
	t.Parallel()

	p, ok := prices.Lookup("claude_subscription", "claude-haiku-5-5", "claude-opus-5-5")
	require.True(t, ok)
	assert.Equal(t, "claude-haiku-5-5", p.Model)

	p, ok = prices.Lookup("claude_subscription", "sonnet", "claude-sonnet-5-5")
	require.True(t, ok, "an alias the book does not know falls back to the provider's model")
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
