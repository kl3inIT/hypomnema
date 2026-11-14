package modelgateway

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway/prices"
)

// A model's attributes (issue #367) are what a client needs to know of it:
// its context window, largest output, inputs, whether it reasons and at which
// effort levels. A value set in the model's YAML wins; else the value
// models.dev gives the first target's model (the price book's snapshot), as
// 9router-go, omniroute and omp seed theirs and let a model override them;
// else it is unknown, and a client keeps its own default.

// Inputs a model may take.
const (
	InputText  = "text"
	InputImage = "image"
)

// efforts are the reasoning effort levels, in order, as models.dev and a
// chat request's reasoning_effort name them.
var efforts = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

// Attributes are the attributes a model's YAML sets; nil or empty is not set.
type Attributes struct {
	ContextWindow *int64   `yaml:"context_window,omitempty"`
	MaxOutput     *int64   `yaml:"max_output,omitempty"`
	Input         []string `yaml:"input,omitempty,flow"`
	Reasoning     *bool    `yaml:"reasoning,omitempty"`
	Efforts       []string `yaml:"efforts,omitempty,flow"`
}

// attributeKeys are the keys of a model's YAML that hold its attributes.
var attributeKeys = []string{"context_window", "max_output", "input", "reasoning", "efforts"}

// Source is where a resolved value came from.
type Source string

// The sources of a value.
const (
	SourceSet       Source = "set"
	SourceModelsDev Source = "models_dev"
	// SourceCodex is the window of a Codex subscription's model, from Codex's own list.
	SourceCodex   Source = "codex"
	SourceUnknown Source = "unknown"
)

// codexWindows are the context windows Codex itself gives its models, which a
// Codex subscription serves, and codexWindow the one it gives a model it does
// not list. models.dev gives the API's larger window (1,050,000 for
// gpt-6.1-sol), past which the subscription's backend refuses a request.
// From Codex (Apache License 2.0, https://github.com/openai/codex, f5ac7a7):
// codex-rs/models-manager/models.json context_window, and
// codex-rs/models-manager/src/model_info.rs:129.
var codexWindows = map[string]int64{"gpt-6.1-terra": 500000, "gpt-daybreak-red-latest": 372000}

const codexWindow int64 = 272000

// Value is one resolved attribute: the value used (nil when unknown), where
// it came from, and what models.dev gives (nil when nothing), which a model
// that sets its own shows beside it.
type Value[T any] struct {
	Value     *T
	Source    Source
	ModelsDev *T
}

// Resolved are a model's attributes as clients are given them, with the
// price its tokens are counted at.
type Resolved struct {
	ContextWindow Value[int64]
	MaxOutput     Value[int64]
	Input         Value[[]string]
	Reasoning     Value[bool]
	Efforts       Value[[]string]
	// Price is nil when the price book has none.
	Price *prices.Price
}

func resolve[T any](set, seed *T) Value[T] {
	switch {
	case set != nil:
		return Value[T]{Value: set, Source: SourceSet, ModelsDev: seed}
	case seed != nil:
		return Value[T]{Value: seed, Source: SourceModelsDev, ModelsDev: seed}
	default:
		return Value[T]{Source: SourceUnknown}
	}
}

// listOf is a list as a value: nil when empty.
func listOf(l []string) *[]string {
	if len(l) == 0 {
		return nil
	}

	return &l
}

// Resolve gives a model's attributes: each set one, else models.dev's for
// the model of its first target, under the vendor of that target's provider.
// A provider that is gone resolves as one of unknown kind.
func (s *Service) Resolve(ctx context.Context, m Model) (Resolved, error) {
	var kind, upstream string

	if len(m.Targets) > 0 {
		upstream = m.Targets[0].UpstreamModel

		p, err := s.providers(ctx, m.Targets[0].Provider)

		switch {
		case err == nil:
			kind = p.Kind
		case !errors.Is(err, ErrNotFound):
			return Resolved{}, fmt.Errorf("reading provider %q: %w", m.Targets[0].Provider, err)
		}
	}

	return resolveAttributes(m.Name, m.Attributes, kind, upstream), nil
}

func resolveAttributes(name string, set Attributes, kind, upstream string) Resolved {
	seed, _ := prices.Find(kind, upstream)

	// An effort level the backend does not take as its own is not offered.
	accepted, limited := effortsOf(kind, upstream)

	seedEfforts := seed.Efforts
	if limited {
		seedEfforts = slices.DeleteFunc(slices.Clone(seedEfforts), func(e string) bool {
			return !slices.Contains(accepted, e)
		})
	}

	out := Resolved{
		ContextWindow: contextWindowOf(set.ContextWindow, seed.Context, kind, upstream),
		MaxOutput:     resolve(set.MaxOutput, seed.MaxOutput),
		Input:         resolve(listOf(set.Input), listOf(seed.Inputs)),
		Reasoning:     resolve(set.Reasoning, seed.Reasoning),
		Efforts:       resolve(listOf(set.Efforts), listOf(seedEfforts)),
	}

	// The price is the one the gateway counts the model's usage at
	// (usage_cost.go): the public name first, then the target's model.
	if p, ok := prices.Lookup(kind, name, upstream); ok {
		out.Price = &p
	}

	return out
}

// contextWindowOf resolves a model's window as resolve does, but a Codex
// subscription's model takes Codex's own window before models.dev's.
func contextWindowOf(set, seed *int64, kind, upstream string) Value[int64] {
	if set != nil || kind != "codex_subscription" {
		return resolve(set, seed)
	}

	w, ok := codexWindows[upstream]
	if !ok {
		w = codexWindow
	}

	return Value[int64]{Value: &w, Source: SourceCodex, ModelsDev: seed}
}

// effortsOf are the effort levels a provider's adapter passes on as levels of
// their own, and whether it is limited to them. An OpenAI-compatible provider
// gets reasoning_effort as sent, so it is not.
func effortsOf(kind, upstream string) ([]string, bool) {
	switch kind {
	case "codex_subscription":
		// Passed on, but max is sent as xhigh (codex/request.go reasoningOf).
		return []string{"none", "minimal", "low", "medium", "high", "xhigh"}, true
	case "claude_subscription":
		// A thinking budget each (claude/thinking.go); none turns it off,
		// and minimal's budget is raised to low's, the smallest Anthropic takes.
		return []string{"none", "low", "medium", "high", "xhigh", "max"}, true
	case "antigravity_subscription":
		// A Gemini model reasons at low, medium or high; another model's
		// effort is in its name (antigravity/request.go thinkingLevel).
		if strings.HasPrefix(upstream, "gemini-") {
			return []string{"low", "medium", "high"}, true
		}

		return nil, true
	default:
		return nil, false
	}
}

// checkAttributes refuses attributes that are not values a model can have,
// or effort levels the first target's provider does not take.
func checkAttributes(a Attributes, kind, upstream string) error {
	switch {
	case a.ContextWindow != nil && *a.ContextWindow <= 0:
		return fmt.Errorf("%w: context_window is a number of tokens above 0", ErrInvalid)
	case a.MaxOutput != nil && *a.MaxOutput <= 0:
		return fmt.Errorf("%w: max_output is a number of tokens above 0", ErrInvalid)
	}

	for _, in := range a.Input {
		if in != InputText && in != InputImage {
			return fmt.Errorf("%w: input is %s or %s, not %q", ErrInvalid, InputText, InputImage, in)
		}
	}

	accepted, limited := effortsOf(kind, upstream)

	for _, e := range a.Efforts {
		switch {
		case !slices.Contains(efforts, e):
			return fmt.Errorf("%w: an effort level is one of %s, not %q", ErrInvalid, strings.Join(efforts, ", "), e)
		case limited && !slices.Contains(accepted, e):
			return fmt.Errorf("%w: the gateway does not give %s effort %q (it gives %s)",
				ErrInvalid, kind, e, strings.Join(accepted, ", "))
		}
	}

	return nil
}

// SetAttributes changes the attributes a model's YAML sets and applies it
// again: a nil or empty one is taken out, to come from models.dev. The rest
// of the document stays as it was.
func (s *Service) SetAttributes(ctx context.Context, name string, a Attributes) (Model, error) {
	m, err := s.GetModel(ctx, name)
	if err != nil {
		return Model{}, err
	}

	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(m.Source), &doc); err != nil || len(doc.Content) != 1 ||
		doc.Content[0].Kind != yaml.MappingNode {
		return Model{}, fmt.Errorf("reading the YAML of model %q: %w", name, errors.Join(ErrInvalid, err))
	}

	root := doc.Content[0]

	kept := make([]*yaml.Node, 0, len(root.Content))
	for i := 0; i+1 < len(root.Content); i += 2 {
		if !slices.Contains(attributeKeys, root.Content[i].Value) {
			kept = append(kept, root.Content[i], root.Content[i+1])
		}
	}

	var set yaml.Node
	if err := set.Encode(a); err != nil {
		return Model{}, fmt.Errorf("encoding the attributes of model %q: %w", name, err)
	}

	root.Content = append(kept, set.Content...)

	var source strings.Builder

	enc := yaml.NewEncoder(&source)
	enc.SetIndent(2)

	if err := errors.Join(enc.Encode(&doc), enc.Close()); err != nil {
		return Model{}, fmt.Errorf("encoding model %q: %w", name, err)
	}

	return s.ApplyModel(ctx, source.String())
}
