// Command unitsgen writes the gateway's prices by unit from LiteLLM's price
// map (issue #723, owner's decision 2 of 2026-11-18), beside the token prices
// ./gen writes from models.dev: what a web search costs a query, what
// speech-to-text costs a second heard, what text-to-speech costs a character
// spoken, and what a model's own web search costs a search. LiteLLM keeps
// them as input_cost_per_query, input_cost_per_second (and
// output_cost_per_second, Soniox's), input_cost_per_character and
// search_context_cost_per_query (model_prices_and_context_window.json; MIT,
// third_party/litellm/NOTICE). Only the providers the gateway asks are kept,
// under the names it records them by. Exa's auto search price comes from
// OmniRoute's searchRegistry.ts when LiteLLM does not give one (issue #972).
// Other missing prices are left out. Each is in nano-dollars a unit, sorted
// so that a new snapshot changes only what changed.
//
//	curl -sSo litellm.json https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json
//	go run ./internal/controlplane/modelgateway/prices/unitsgen litellm.json > internal/controlplane/modelgateway/prices/units.json
package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"slices"
	"sort"
	"strings"
)

// providers are LiteLLM's names of the providers the gateway asks, with the
// gateway's names of them (websearch.Providers, speech.Providers).
var providers = map[string]string{
	"exa_ai": "exa", "firecrawl": "firecrawl", "parallel_ai": "parallel", "tavily": "tavily", "tinyfish": "tinyfish",
	"elevenlabs": "elevenlabs", "soniox": "soniox",
}

// vendors are the makers whose models' own web search is priced.
var vendors = []string{"anthropic", "openai"}

// The units.
const (
	unitQuery     = "query"
	unitSecond    = "second"
	unitCharacter = "character"
	unitWebSearch = "web_search"
)

type entry struct {
	Provider            string   `json:"litellm_provider"`
	Mode                string   `json:"mode"`
	PerQuery            *float64 `json:"input_cost_per_query"`
	InPerSecond         *float64 `json:"input_cost_per_second"`
	OutPerSecond        *float64 `json:"output_cost_per_second"`
	PerCharacter        *float64 `json:"input_cost_per_character"`
	SearchContextPerQry *struct {
		Medium *float64 `json:"search_context_size_medium"`
	} `json:"search_context_cost_per_query"`
}

type unitPrice struct {
	Name  string `json:"name"`
	Unit  string `json:"unit"`
	Nanos int64  `json:"nanos"`
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: unitsgen <LiteLLM's model_prices_and_context_window.json>")
		os.Exit(2)
	}

	raw, err := os.ReadFile(os.Args[1]) //nolint:gosec // The map is the path the person running it names.
	if err != nil {
		fail(err)
	}

	var all map[string]json.RawMessage
	if err := json.Unmarshal(raw, &all); err != nil {
		fail(fmt.Errorf("reading the map: %w", err))
	}

	var out []unitPrice

	for name, rawEntry := range all {
		var e entry
		if json.Unmarshal(rawEntry, &e) != nil {
			continue // the map's sample_spec and any other entry not of this shape
		}

		if p, ok := unitOf(name, e); ok {
			out = append(out, p)
		}
	}

	if !slices.ContainsFunc(out, func(p unitPrice) bool { return p.Name == "exa/search" && p.Unit == unitQuery }) {
		out = append(out, unitPrice{Name: "exa/search", Unit: unitQuery, Nanos: nanos(0.007)})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}

		return out[i].Unit < out[j].Unit
	})

	encoded, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		fail(err)
	}

	if _, err := os.Stdout.Write(append(encoded, '\n')); err != nil {
		fail(err)
	}
}

// unitOf is an entry's price by unit under the gateway's name, when it is
// one the gateway records.
func unitOf(name string, e entry) (unitPrice, bool) {
	for _, vendor := range vendors {
		if e.Provider == vendor && !strings.Contains(name, "/") && e.SearchContextPerQry != nil &&
			e.SearchContextPerQry.Medium != nil {
			return unitPrice{Name: vendor + "/" + name, Unit: unitWebSearch, Nanos: nanos(*e.SearchContextPerQry.Medium)},
				true
		}
	}

	ours, ok := providers[e.Provider]
	if !ok {
		return unitPrice{}, false
	}

	model, ok := strings.CutPrefix(name, e.Provider+"/")
	if !ok {
		return unitPrice{}, false
	}

	name = ours + "/" + model

	switch {
	case e.Mode == "search" && model == "search" && e.PerQuery != nil:
		return unitPrice{Name: name, Unit: unitQuery, Nanos: nanos(*e.PerQuery)}, true
	case e.Mode == "audio_transcription" && (e.InPerSecond != nil || e.OutPerSecond != nil):
		// Soniox's is all output_cost_per_second, LiteLLM's way of pricing the seconds it heard.
		return unitPrice{Name: name, Unit: unitSecond, Nanos: nanos(value(e.InPerSecond) + value(e.OutPerSecond))}, true
	case e.Mode == "audio_speech" && e.PerCharacter != nil:
		return unitPrice{Name: name, Unit: unitCharacter, Nanos: nanos(*e.PerCharacter)}, true
	}

	return unitPrice{}, false
}

func value(v *float64) float64 {
	if v == nil {
		return 0
	}

	return *v
}

// nanos is dollars as nano-dollars.
func nanos(dollars float64) int64 { return int64(math.Round(dollars * 1e9)) }

func fail(err error) {
	fmt.Fprintln(os.Stderr, "unitsgen:", err)
	os.Exit(1)
}
