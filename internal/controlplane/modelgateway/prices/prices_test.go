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

// Scenario: the Claude subscription is served Claude Code's short names
// (issue #399). Each is priced and described as the newest model of its
// family in the book, under Anthropic; the same name under a provider of
// another maker is not Claude's.
func TestAClaudeShortNameIsTheNewestOfItsFamily(t *testing.T) {
	t.Parallel()

	for family, want := range map[string]string{
		"opus": "claude-opus-5-5", "sonnet": "claude-sonnet-5-5", "haiku": "claude-haiku-5-5", "fable": "claude-fable-5-2",
	} {
		got, ok := prices.ClaudeModel(family)
		require.True(t, ok, family)
		assert.Equal(t, want, got, family)

		p, ok := prices.Lookup("claude_subscription", family, family)
		require.True(t, ok, family)
		assert.Equal(t, want, p.Model, family)

		m, ok := prices.Find("anthropic", family)
		require.True(t, ok, family)
		assert.Equal(t, want, m.Model, family)
	}

	got, ok := prices.ClaudeModel("claude-opus-4-5")
	assert.False(t, ok)
	assert.Equal(t, "claude-opus-4-5", got, "a full id is itself")

	_, ok = prices.Lookup("openai_compatible", "opus")
	assert.False(t, ok, "opus of another provider is not Claude's")
}
