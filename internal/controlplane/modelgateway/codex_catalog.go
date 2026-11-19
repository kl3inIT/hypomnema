package modelgateway

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sync"

	"github.com/kl3inIT/hypomnema/internal/shared/agentconfig"
)

// The Codex CLI's model catalog (issue #907). Codex lists in /model the models of its own catalog: the one bundled
// in its binary, or one a custom provider serves at its model_catalog_url (codex-rs/model-provider-info/src/lib.rs
// model_catalog_url; model-provider/src/models_endpoint.rs fetch_models: GET <url>?client_version=, a
// {"models":[ModelInfo…]} answer with unique, non-empty slugs). Without one it offers its bundled models under their
// bare names, with its own effort levels, which the gateway does not serve under those names. The gateway serves
// the models a key may call on /v1/responses, the one route Codex speaks, as such a catalog: each entry is Codex's
// own bundled entry for the model behind it (codexmodels/models.json, from Codex rust-v0.169.1; third_party/codex),
// or Codex's own entry for a model it does not know (models-manager/src/model_info.rs model_info_from_slug), renamed
// to the gateway's name and given the gateway's window, inputs and effort levels where the gateway knows them,
// read from the target /v1/responses serves.
//
// Codex 0.156 and later ignore a catalog of 1 MiB or more and keep their bundled one, as CLIProxyAPI found
// (internal/client/codex/models/models.go:719-722, models_test.go:1710): the instructions are most of an entry
// (about 60 KiB of a bundled one's 65), so, as CLIProxyAPI does, only the first entry of each bundled model keeps
// Codex's own; a model Codex does not bundle, and a second name for the same model, get CLIProxyAPI's short ones,
// and when the whole is still too big the last entries that keep their own give them up until it fits.

//go:embed codexmodels/models.json
var codexBundledModels []byte

// codexCatalogLimit is the size Codex takes a catalog up to (CLIProxyAPI's test: below 1 MiB).
const codexCatalogLimit = 1 << 20

// codexCompactInstructions are the instructions of an entry that does not keep Codex's own, CLIProxyAPI's
// (MIT License, https://github.com/router-for-me/CLIProxyAPI, db7f177: internal/client/codex/models/models.go:583
// codexClientFallbackInstructions).
const codexCompactInstructions = "You are Codex, a coding agent. You and the user share one workspace."

// codexEntry is one ModelInfo of a Codex catalog, every field kept as Codex wrote it but those set here.
type codexEntry map[string]json.RawMessage

// codexBundle is Codex's bundled catalog by slug, and the description Codex gives each effort level (the first
// entry that names it), read once.
var codexBundle = sync.OnceValues(func() (map[string]codexEntry, map[string]string) {
	var file struct {
		Models []codexEntry `json:"models"`
	}

	if err := json.Unmarshal(codexBundledModels, &file); err != nil {
		panic(fmt.Sprintf("modelgateway: Codex's bundled catalog does not decode: %v", err))
	}

	bySlug, levels := map[string]codexEntry{}, map[string]string{}

	for _, m := range file.Models {
		var slug string
		_ = json.Unmarshal(m["slug"], &slug)
		bySlug[slug] = m

		var presets []codexLevel
		_ = json.Unmarshal(m["supported_reasoning_levels"], &presets)

		for _, p := range presets {
			if _, ok := levels[p.Effort]; !ok {
				levels[p.Effort] = p.Description
			}
		}
	}

	return bySlug, levels
})

// codexLevel is a ReasoningEffortPreset (codex-rs/protocol/src/openai_models.rs).
type codexLevel struct {
	Effort      string `json:"effort"`
	Description string `json:"description"`
}

// codexUnknownModel is what Codex gives a model it does not know (models-manager/src/model_info.rs
// model_info_from_slug at rust-v0.169.1), but listed in the picker and with the short instructions.
func codexUnknownModel() codexEntry {
	fields := map[string]any{
		"description": nil, "default_reasoning_level": nil, "supported_reasoning_levels": []any{},
		"shell_type": "unified_exec", "visibility": "list", "supported_in_api": true, "priority": 99,
		"additional_speed_tiers": []any{}, "service_tiers": []any{}, "availability_nux": nil, "upgrade": nil,
		"include_skills_usage_instructions": false, "include_plugin_usage_instructions": false,
		"include_apps_usage_instructions": false, "default_reasoning_summary": "auto",
		"support_verbosity": false, "default_verbosity": nil, "apply_patch_tool_type": nil,
		"web_search_tool_type": "text", "truncation_policy": map[string]any{"mode": "bytes", "limit": 10000},
		"supports_image_detail_original": false, "context_window": 272000, "max_context_window": 272000,
		"effective_context_window_percent": 95, "experimental_supported_tools": []any{},
		"input_modalities": []string{InputText, InputImage}, "supports_search_tool": false,
	}

	out := codexEntry{}
	for k, v := range fields {
		out.set(k, v)
	}

	out.compact()

	return out
}

// compact gives an entry the short instructions in both the fields Codex reads them from, as CLIProxyAPI's
// useCompactCodexClientInstructions does (models.go:587-598): a catalog entry needs one of them.
func (e codexEntry) compact() {
	e.set("base_instructions", codexCompactInstructions)
	e.set("model_messages", map[string]any{
		"instructions_template": codexCompactInstructions, "instructions_variables": nil, "approvals": nil,
		"collaboration_modes": nil, "auto_review": nil, "permissions": nil, "multi_agent": nil,
	})
}

func (e codexEntry) set(key string, v any) {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("modelgateway: a catalog value does not encode: %v", err))
	}

	e[key] = raw
}

func (e codexEntry) int64(key string) int64 {
	var n int64
	_ = json.Unmarshal(e[key], &n)

	return n
}

// servedOnResponses says /v1/responses passes a request on to a target of this kind and model: a Codex
// subscription, or an OpenCode Go model Go serves on Responses (issue #854).
func (s *Service) servedOnResponses(ctx context.Context, kind, model string) bool {
	return kind == "codex_subscription" || (kind == openCodeGoKind && s.goWire(ctx, model) == goWireResponses)
}

// responsesTarget is a model's first target /v1/responses serves, and its provider's kind.
func (s *Service) responsesTarget(ctx context.Context, m Model) (Target, string, bool) {
	for _, t := range m.Targets {
		p, err := s.providers(ctx, t.Provider)
		if err == nil && s.servedOnResponses(ctx, p.Kind, t.UpstreamModel) {
			return t, p.Kind, true
		}
	}

	return Target{}, "", false
}

// CodexModels are the models a key may call that Codex can call: those /v1/responses serves.
func (s *Service) CodexModels(ctx context.Context, k Key) ([]Model, error) {
	models, err := s.ModelsFor(ctx, k)
	if err != nil {
		return nil, err
	}

	return slices.DeleteFunc(models, func(m Model) bool {
		_, _, ok := s.responsesTarget(ctx, m)

		return !ok
	}), nil
}

// CodexCatalog is the catalog of the models a key may call from Codex, in the gateway's order, encoded below
// codexCatalogLimit.
func (s *Service) CodexCatalog(ctx context.Context, k Key) ([]byte, error) {
	models, err := s.CodexModels(ctx, k)
	if err != nil {
		return nil, err
	}

	entries := make([]codexEntry, 0, len(models))
	full := []int{}
	kept := map[string]bool{}

	for i, m := range models {
		target, kind, _ := s.responsesTarget(ctx, m)

		resolved, err := s.resolveTarget(ctx, m, target)
		if err != nil {
			return nil, err
		}

		entry, bundled := codexCatalogEntry(m.Name, i, target.UpstreamModel, kind, resolved)

		switch {
		case !bundled:
		case kept[target.UpstreamModel]:
			entry.compact()
		default:
			kept[target.UpstreamModel] = true
			full = append(full, i)
		}

		entries = append(entries, entry)
	}

	for {
		body, err := encodeCatalog(entries)
		if err != nil || len(body) < codexCatalogLimit || len(full) == 0 {
			return body, err
		}

		entries[full[len(full)-1]].compact()
		full = full[:len(full)-1]
	}
}

// resolveTarget gives a model's attributes as served from one of its targets: the model's own when the target is
// its first, which they are resolved for (Resolve); else the target's alone, so that a name over a Claude model
// and then a Codex one tells Codex the Codex model's window and levels.
func (s *Service) resolveTarget(ctx context.Context, m Model, t Target) (Resolved, error) {
	if len(m.Targets) > 0 && m.Targets[0] == t {
		return s.Resolve(ctx, m)
	}

	return s.Resolve(ctx, Model{Name: m.Name, Capability: m.Capability, Targets: []Target{t}})
}

// encodeCatalog writes a catalog as Codex reads it, its instructions' < and > as they are.
func encodeCatalog(entries []codexEntry) ([]byte, error) {
	var out bytes.Buffer

	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)

	if err := enc.Encode(map[string]any{"models": entries}); err != nil {
		return nil, fmt.Errorf("encoding the Codex catalog: %w", err)
	}

	return out.Bytes(), nil
}

// codexCatalogEntry is the catalog entry of one gateway model whose target is upstream at a provider of the kind,
// and whether it is Codex's bundled entry for the model.
func codexCatalogEntry(name string, order int, upstream, kind string, r Resolved) (codexEntry, bool) {
	bundled, levels := codexBundle()

	entry := codexUnknownModel()
	b, found := bundled[upstream]

	if found {
		entry = codexEntry{}
		for k, v := range b {
			entry[k] = v
		}
	}

	entry.set("slug", name)
	entry.set("display_name", DisplayName(name))
	entry.set("visibility", "list")
	entry.set("supported_in_api", true)
	entry.set("priority", order)
	// An upgrade, a notice or a review model names a model of Codex's own catalog, not of this one.
	entry.set("upgrade", nil)
	entry.set("availability_nux", nil)
	entry.set("auto_review_model_override", nil)

	if w := r.ContextWindow.Value; w != nil {
		entry.set("context_window", *w)

		if entry.int64("max_context_window") < *w {
			entry.set("max_context_window", *w)
		}
	}

	if in := r.Input.Value; in != nil {
		entry.set("input_modalities", *in)
	}

	setCodexEfforts(entry, kind, r, levels)

	return entry, found
}

// setCodexEfforts gives an entry the effort levels the gateway gives its model, each with the words Codex has for
// it; else, when the gateway does not know them, the entry's own that the provider takes. Its default stays Codex's
// when it is among them, else medium, else the first.
func setCodexEfforts(entry codexEntry, kind string, r Resolved, words map[string]string) {
	var own []codexLevel
	_ = json.Unmarshal(entry["supported_reasoning_levels"], &own)

	var efforts []string

	switch accepted, limited := effortsOf(kind, ""); {
	case r.Efforts.Value != nil:
		efforts = *r.Efforts.Value
	default:
		for _, l := range own {
			if !limited || slices.Contains(accepted, l.Effort) {
				efforts = append(efforts, l.Effort)
			}
		}
	}

	presets := make([]codexLevel, 0, len(efforts))

	for _, e := range efforts {
		description := words[e]

		for _, l := range own {
			if l.Effort == e {
				description = l.Description
			}
		}

		if description == "" {
			description = e
		}

		presets = append(presets, codexLevel{Effort: e, Description: description})
	}

	entry.set("supported_reasoning_levels", presets)

	var current string
	_ = json.Unmarshal(entry["default_reasoning_level"], &current)

	switch {
	case len(efforts) == 0:
		entry.set("default_reasoning_level", nil)
	case slices.Contains(efforts, current):
	case slices.Contains(efforts, "medium"):
		entry.set("default_reasoning_level", "medium")
	default:
		entry.set("default_reasoning_level", efforts[0])
	}
}

// codexModels answers GET /v1/codex/models: the key's catalog for Codex (issue #907). Codex sends the key it sends
// with its requests, and its version as client_version, which the catalog does not depend on.
func (s *Server) codexModels(w http.ResponseWriter, r *http.Request) {
	k, ok := s.authenticate(w, r)
	if !ok {
		return
	}

	body, err := s.svc.CodexCatalog(r.Context(), k)
	if err != nil {
		s.internal(w, r, "modelgateway.codex_models_failed", err)

		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body) // The client went away; nothing is left to tell it.
}

// listed is a model as GET /v1/models lists it (modelItem): its first target says its wire and Codex's
// description of it, and Resolve its window and levels.
func (s *Service) listed(ctx context.Context, m Model) (modelItem, error) {
	item := modelItem{
		ID: m.Name, Object: "model", Created: m.CreatedAt.Unix(), OwnedBy: "hypomnema", Name: DisplayName(m.Name),
		DisplayName: DisplayName(m.Name), SupportedEndpointTypes: []string{"openai"},
	}

	r, err := s.Resolve(ctx, m)
	if err != nil {
		return modelItem{}, err
	}

	item.ContextLength, item.Reasoning = r.ContextWindow.Value, r.Reasoning.Value
	if r.Efforts.Value != nil {
		item.SupportedReasoningLevels = *r.Efforts.Value
	}

	if len(m.Targets) == 0 {
		return item, nil
	}

	p, err := s.providers(ctx, m.Targets[0].Provider)
	if err != nil {
		return item, nil //nolint:nilerr // a provider that is gone says nothing more of the model
	}

	if agentconfig.Wires[p.Kind] == agentconfig.WireMessages {
		item.SupportedEndpointTypes = []string{"anthropic", "openai"}
	}

	if bundled, _ := codexBundle(); p.Kind == "codex_subscription" {
		if entry, found := bundled[m.Targets[0].UpstreamModel]; found {
			_ = json.Unmarshal(entry["description"], &item.Description)
		}
	}

	return item, nil
}
