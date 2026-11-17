package prices

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sync"
)

// Prices by unit (issue #723, owner's decision 2 of 2026-11-18): what is not
// counted in tokens, generated from LiteLLM's price map by ./unitsgen and
// embedded beside the token prices, as LiteLLM prices a search by its query
// (cost_calculator.py:787-795), a transcription by its seconds and speech by
// its characters (:694-786), and a model's own web searches beside its
// tokens (llms/anthropic/cost_calculation.py:71-127). For a provider asked
// without a key, or a subscription's model, it is what the same use would
// cost at the API, as the token prices are.

//go:embed units.json
var unitBook []byte

// The units a price is given for.
const (
	UnitQuery     = "query"
	UnitSecond    = "second"
	UnitCharacter = "character"
	UnitWebSearch = "web_search"
)

// UnitPrice is the price of one unit, in nano-dollars, of what Name names:
// provider/model for a provider's search or speech, vendor/model for a
// model's own web search.
type UnitPrice struct {
	Name  string `json:"name"`
	Unit  string `json:"unit"`
	Nanos int64  `json:"nanos"`
}

var (
	unitsOnce sync.Once
	byUnit    map[string]UnitPrice // unit + " " + name → price
	errUnits  error
)

func loadUnits() {
	var all []UnitPrice
	if errUnits = json.Unmarshal(unitBook, &all); errUnits != nil {
		errUnits = fmt.Errorf("reading the prices by unit: %w", errUnits)

		return
	}

	byUnit = make(map[string]UnitPrice, len(all))
	for _, p := range all {
		byUnit[p.Unit+" "+p.Name] = p
	}
}

// LookupUnit finds the price of a unit of what is named.
func LookupUnit(unit, name string) (UnitPrice, bool) {
	unitsOnce.Do(loadUnits)

	if errUnits != nil {
		return UnitPrice{}, false
	}

	p, ok := byUnit[unit+" "+name]

	return p, ok
}

// LookupWebSearch finds the price of one of a model's own web searches, the
// first of models found under the provider's vendors, as Lookup finds a
// token price.
func LookupWebSearch(kind string, models ...string) (UnitPrice, bool) {
	for _, model := range models {
		if model == "" {
			continue
		}

		for _, vendor := range vendorsOf(kind, model) {
			if p, ok := LookupUnit(UnitWebSearch, vendor+"/"+model); ok {
				return p, true
			}
		}
	}

	return UnitPrice{}, false
}

// UnitCost is what an amount costs at a price, in micro-dollars, the amount
// given in thousandths of the unit (a millisecond for a second), rounded to
// the nearest micro-dollar.
func UnitCost(p UnitPrice, thousandths int64) int64 {
	return (thousandths*p.Nanos + 500_000) / 1_000_000
}
