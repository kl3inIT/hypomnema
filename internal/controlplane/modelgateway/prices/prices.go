// Package prices is the gateway's price book (issue #304): what a model's
// tokens cost at its vendor's API, generated from models.dev by ./gen and
// embedded, as Coder embeds its own (coderd/aibridge/prices). The cost of a
// request is reckoned from it as 9router-go and Coder reckon theirs
// (internal/pricing/cost.go, aibridgedserver/cost.go). For a subscription it
// is what the same tokens would cost at the API, not what the plan costs.
package prices

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

//go:embed prices.json
var book []byte

// Price is a model's price in micro-dollars per million tokens. A cache price
// the vendor does not give is nil.
type Price struct {
	Vendor     string `json:"vendor"`
	Model      string `json:"model"`
	Input      *int64 `json:"input"`
	Output     *int64 `json:"output"`
	CacheRead  *int64 `json:"cache_read"`
	CacheWrite *int64 `json:"cache_write"`
}

// Tokens are a request's counts. Input includes what was read from and
// written to the cache, as the gateway records it.
type Tokens struct {
	Input, CacheRead, CacheWrite, Output int64
}

var (
	once    sync.Once
	byModel map[string]map[string]Price // vendor → model → price
	errLoad error
)

func load() {
	var all []Price
	if errLoad = json.Unmarshal(book, &all); errLoad != nil {
		errLoad = fmt.Errorf("reading the price book: %w", errLoad)

		return
	}

	byModel = map[string]map[string]Price{}
	for _, p := range all {
		if byModel[p.Vendor] == nil {
			byModel[p.Vendor] = map[string]Price{}
		}

		byModel[p.Vendor][p.Model] = p
	}
}

// vendorsOf are the vendors to look a model up under, in order: the
// provider's own, then the model's maker read from its name, for a provider
// that serves several makers' models (Antigravity, an API provider).
func vendorsOf(kind, model string) []string {
	var out []string

	switch kind {
	case "claude_subscription":
		out = append(out, "anthropic")
	case "codex_subscription":
		out = append(out, "openai")
	}

	lower := strings.ToLower(model)

	switch {
	case strings.Contains(lower, "claude"):
		out = append(out, "anthropic")
	case strings.Contains(lower, "gemini"):
		out = append(out, "google")
	case strings.HasPrefix(lower, "gpt") || strings.HasPrefix(lower, "o1") ||
		strings.HasPrefix(lower, "o3") || strings.HasPrefix(lower, "o4"):
		out = append(out, "openai")
	}

	return out
}

// Lookup finds the price of the first of models found under the provider's
// vendors: the model asked for first, then the provider's own model, as
// Coder looks a price up (GetAIModelPriceForProvider).
func Lookup(kind string, models ...string) (Price, bool) {
	once.Do(load)

	if errLoad != nil {
		return Price{}, false
	}

	for _, model := range models {
		if model == "" {
			continue
		}

		for _, vendor := range vendorsOf(kind, model) {
			if p, ok := byModel[vendor][model]; ok {
				return p, true
			}
		}
	}

	return Price{}, false
}

// Cost is what tokens cost at a price, in micro-dollars: the input not read
// from or written to the cache at the input price, the cache's at its own (the
// input price when the vendor gives none), and the output, reasoning
// included, at the output price.
func Cost(p Price, t Tokens) int64 {
	input, output := val(p.Input), val(p.Output)
	read, write := input, input

	if p.CacheRead != nil {
		read = *p.CacheRead
	}

	if p.CacheWrite != nil {
		write = *p.CacheWrite
	}

	uncached := max(t.Input-t.CacheRead-t.CacheWrite, 0)
	total := uncached*input + t.CacheRead*read + t.CacheWrite*write + t.Output*output

	// Prices are a million tokens'; round to the nearest micro-dollar.
	return (total + 500_000) / 1_000_000
}

func val(v *int64) int64 {
	if v == nil {
		return 0
	}

	return *v
}
