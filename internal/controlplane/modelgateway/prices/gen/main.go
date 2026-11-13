// Command gen writes the gateway's price book from a models.dev snapshot
// (issue #304), as Coder generates its own (scripts/aibridgepricesgen): the
// anthropic, openai and google models, each with its input, output, cache
// read and cache write prices in micro-dollars per million tokens, sorted so a
// new snapshot changes only what changed. A price models.dev does not give is
// left out (null). Only the base tier is kept; a long context's tier is not.
//
// Each model also keeps what a client needs to know of it (issue #367, as
// 9router-go and omp seed theirs from models.dev): its context window and
// largest output (limit.context, limit.output), the inputs it takes of those a
// gateway client sends (text, image), whether it reasons and its effort levels
// (reasoning, reasoning_options of type effort). A limit of 0 is not given.
//
//	curl -sSo api.json https://models.dev/api.json
//	go run ./internal/controlplane/modelgateway/prices/gen api.json > internal/controlplane/modelgateway/prices/prices.json
package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"slices"
	"sort"
)

// vendors are the models.dev providers kept: the makers whose models the
// gateway serves from subscriptions.
var vendors = []string{"anthropic", "google", "openai"}

// inputs are the inputs kept: those a client of the gateway's chat sends.
var inputs = []string{"text", "image"}

type snapshot map[string]struct {
	Models map[string]struct {
		Cost *struct {
			Input      *float64 `json:"input"`
			Output     *float64 `json:"output"`
			CacheRead  *float64 `json:"cache_read"`
			CacheWrite *float64 `json:"cache_write"`
		} `json:"cost"`
		Limit struct {
			Context int64 `json:"context"`
			Output  int64 `json:"output"`
		} `json:"limit"`
		Modalities struct {
			Input []string `json:"input"`
		} `json:"modalities"`
		Reasoning        *bool `json:"reasoning"`
		ReasoningOptions []struct {
			Type   string   `json:"type"`
			Values []string `json:"values"`
		} `json:"reasoning_options"`
	} `json:"models"`
}

type price struct {
	Vendor     string   `json:"vendor"`
	Model      string   `json:"model"`
	Input      *int64   `json:"input"`
	Output     *int64   `json:"output"`
	CacheRead  *int64   `json:"cache_read"`
	CacheWrite *int64   `json:"cache_write"`
	Context    *int64   `json:"context,omitempty"`
	MaxOutput  *int64   `json:"max_output,omitempty"`
	Inputs     []string `json:"inputs,omitempty"`
	Reasoning  *bool    `json:"reasoning,omitempty"`
	Efforts    []string `json:"efforts,omitempty"`
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
			p := price{
				Vendor: vendor, Model: model, Context: positive(m.Limit.Context), MaxOutput: positive(m.Limit.Output),
				Reasoning: m.Reasoning,
			}

			if m.Cost != nil && m.Cost.Input != nil && m.Cost.Output != nil {
				p.Input, p.Output = micros(m.Cost.Input), micros(m.Cost.Output)
				p.CacheRead, p.CacheWrite = micros(m.Cost.CacheRead), micros(m.Cost.CacheWrite)
			}

			for _, in := range inputs {
				if slices.Contains(m.Modalities.Input, in) {
					p.Inputs = append(p.Inputs, in)
				}
			}

			for _, o := range m.ReasoningOptions {
				if o.Type == "effort" {
					p.Efforts = append(p.Efforts, o.Values...)
				}
			}

			if p.Input == nil && p.Context == nil {
				continue
			}

			out = append(out, p)
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

// positive is a limit models.dev gives; 0 is one it does not.
func positive(n int64) *int64 {
	if n <= 0 {
		return nil
	}

	return &n
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
