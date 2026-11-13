// Command gen writes the gateway's price book from a models.dev snapshot
// (issue #304), as Coder generates its own (scripts/aibridgepricesgen): the
// anthropic, openai and google models, each with its input, output, cache
// read and cache write prices in micro-dollars per million tokens, sorted so a
// new snapshot changes only what changed. A price models.dev does not give is
// left out (null). Only the base tier is kept; a long context's tier is not.
//
//	curl -sSo api.json https://models.dev/api.json
//	go run ./internal/controlplane/modelgateway/prices/gen api.json > internal/controlplane/modelgateway/prices/prices.json
package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
)

// vendors are the models.dev providers kept: the makers whose models the
// gateway serves from subscriptions.
var vendors = []string{"anthropic", "google", "openai"}

type snapshot map[string]struct {
	Models map[string]struct {
		Cost *struct {
			Input      *float64 `json:"input"`
			Output     *float64 `json:"output"`
			CacheRead  *float64 `json:"cache_read"`
			CacheWrite *float64 `json:"cache_write"`
		} `json:"cost"`
	} `json:"models"`
}

type price struct {
	Vendor     string `json:"vendor"`
	Model      string `json:"model"`
	Input      *int64 `json:"input"`
	Output     *int64 `json:"output"`
	CacheRead  *int64 `json:"cache_read"`
	CacheWrite *int64 `json:"cache_write"`
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: gen <models.dev api.json>")
		os.Exit(2)
	}

	raw, err := os.ReadFile(os.Args[1]) //nolint:gosec // The snapshot is the path the person running it names.
	if err != nil {
		fail(err)
	}

	var snap snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		fail(fmt.Errorf("reading the snapshot: %w", err))
	}

	var out []price

	for _, vendor := range vendors {
		for model, m := range snap[vendor].Models {
			if m.Cost == nil || m.Cost.Input == nil || m.Cost.Output == nil {
				continue
			}

			out = append(out, price{
				Vendor: vendor, Model: model, Input: micros(m.Cost.Input), Output: micros(m.Cost.Output),
				CacheRead: micros(m.Cost.CacheRead), CacheWrite: micros(m.Cost.CacheWrite),
			})
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Vendor != out[j].Vendor {
			return out[i].Vendor < out[j].Vendor
		}

		return out[i].Model < out[j].Model
	})

	encoded, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		fail(err)
	}

	if _, err := os.Stdout.Write(append(encoded, '\n')); err != nil {
		fail(err)
	}
}

// micros is dollars per million tokens as micro-dollars per million tokens.
func micros(dollars *float64) *int64 {
	if dollars == nil {
		return nil
	}

	v := int64(math.Round(*dollars * 1_000_000))

	return &v
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "gen:", err)
	os.Exit(1)
}
