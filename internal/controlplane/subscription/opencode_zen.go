package subscription

import (
	"context"

	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

// OpenCode Zen (issue #965): OpenCode's models paid from the workspace's
// balance ("OpenCode Models" in the Console), reached with a Console API key
// the owner pastes (`accounts add opencode_zen <label> --token -`), each key an
// account as a Go key is. What it does is learned from
// (docs/research/2026-11-20-opencode-zen-provider.md):
//
//   - the model list is the Console's, GET /console/api/v1/models, provider
//     `opencode`: the models the key's workspace has enabled, each with the
//     wire it is served on, its window and its price (opencode
//     services/site/src/docs/content/console/api/models.mdx, "Use the Console
//     list when your client should offer the same models OpenCode does");
//   - a model priced 0 is a free model, which OpenCode serves only to OpenCode
//     itself and answers 403 to any other client, even with a key (opencode
//     console/api/inference.mdx "Authentication"): it is left out, as
//     omniroute tells a free model by its price
//     (open-sse/executors/opencodeFreeTierContract.ts, cost 0 in and out);
//   - there is no usage or balance call to read (left out by issue #965).

const (
	// OpenCodeZenRuntime is the runtime of an OpenCode Zen key's account
	// (orchestration.OpenCodeZenRuntime).
	OpenCodeZenRuntime = "opencode_zen"
	// openCodeZenProvider is Zen's provider id in the Console list
	// (models.mdx "Providers").
	openCodeZenProvider = "opencode"
)

// OpenCodeZen is the Vendor for OpenCode Zen keys.
type OpenCodeZen struct{ openCodeKey }

// NewOpenCodeZen returns the Vendor for OpenCode Zen keys; base is OpenCode's
// origin, empty for opencode.ai.
func NewOpenCodeZen(base string, clock platform.Clock) *OpenCodeZen {
	return &OpenCodeZen{newOpenCodeKey(OpenCodeZenRuntime, "OpenCode Zen", base, clock)}
}

// Models implements ModelLister with the Console list's `opencode` provider:
// every chat model on a wire the gateway calls that is not free, with its
// window and largest output.
func (z *OpenCodeZen) Models(ctx context.Context, token Token) ([]ServedModel, error) {
	var answer map[string]struct {
		Models map[string]openCodeModel `json:"models"`
	}

	if err := getModels(ctx, z.client, z.base+"/console/api/v1/models", z.header(token), &answer); err != nil {
		return nil, err
	}

	models := []ServedModel{}

	for id, m := range answer[openCodeZenProvider].Models {
		if m.ID == "" {
			m.ID = id
		}

		if m.free() {
			continue
		}

		if served, ok := m.served(); ok {
			models = append(models, served)
		}
	}

	return sortedModels(models), nil
}

// free says a model is priced 0 both in and out ("`0` means free", models.mdx
// "Fields"; omniroute opencodeFreeTierContract.ts). A model with no price is
// not taken for free.
func (m openCodeModel) free() bool {
	in, out := m.Cost.Text.Input.Price, m.Cost.Text.Output.Price

	return in != nil && out != nil && *in == 0 && *out == 0
}
