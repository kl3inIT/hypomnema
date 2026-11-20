package subscription

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

// OpenCode Go (issue #854): OpenCode's subscription for open models, reached
// with a Console API key the owner pastes (`accounts add opencode_go <label>
// --token -`); there is no OAuth and no refresh. Each key is an account, so
// several keys rotate as the other subscriptions' accounts do. What it does is
// learned from (docs/research/2026-11-18-opencode-go-provider.md):
//
//   - the model list is GET /inference/go/v1/models, whose rows name the wire
//     each model is served on (api: chat-completions, responses, messages,
//     generate-content), its window and its largest output (opencode
//     services/site/src/docs/content/console/go.mdx; openclaw, hermes and pi
//     read the live list);
//   - the limits are GET /zen/go/v1/usage, three windows of a shared pool,
//     usage.{rolling,weekly,monthly}.{percent,status,resetsAt}; a 403 is a key
//     whose workspace has no Go subscription (opencode
//     packages/core/src/usage-limits.ts readGo; omp
//     packages/ai/src/usage/opencode-go.ts).

const (
	// OpenCodeGoRuntime is the runtime of an OpenCode Go key's account
	// (orchestration.OpenCodeGoRuntime).
	OpenCodeGoRuntime = "opencode_go"
	// openCodeGoBase is OpenCode's origin: inference under /inference/go, the
	// usage under /zen/go (omp opencode-go.ts DEFAULT_ENDPOINT, MODEL_ENDPOINT).
	openCodeGoBase = "https://opencode.ai"
	// WindowMonth is Go's third window, which starts again on the
	// subscription's anniversary (omp opencode-go.ts OPENCODE_GO_WINDOWS).
	WindowMonth = "month"
	// openCodeGoNoPlan is the plan an account is marked with when Go answers
	// 403 to its usage: the key's workspace has no Go subscription (opencode
	// usage-limits.ts readGo; omp opencode-go.ts).
	openCodeGoNoPlan = "no Go subscription"
)

// The wires a Go model is served on, as the model list names them; the gateway
// calls each on its own route (modelgateway opencode_adapter.go).
const (
	WireChat      = "chat-completions"
	WireResponses = "responses"
	WireMessages  = "messages"
)

// openCodeKey is what OpenCode's two providers keyed by a Console API key share (OpenCode Go, issue #854, and OpenCode
// Zen, issue #965): a key the owner pastes, never a login, nothing to refresh, and the gateway's name on each call.
type openCodeKey struct {
	runtime, product string
	// base is OpenCode's origin; a test names its own.
	base   string
	client *http.Client
	clock  platform.Clock
}

func newOpenCodeKey(runtime, product, base string, clock platform.Clock) openCodeKey {
	if base == "" {
		base = openCodeGoBase
	}

	return openCodeKey{
		runtime: runtime, product: product, base: strings.TrimRight(base, "/"),
		client: &http.Client{Timeout: limitsTimeout}, clock: clock,
	}
}

// Runtime implements Vendor.
func (k *openCodeKey) Runtime() string { return k.runtime }

// RefreshLead implements Vendor: a key does not end.
func (*openCodeKey) RefreshLead() time.Duration { return 0 }

// RefreshSpacing implements Vendor.
func (*openCodeKey) RefreshSpacing() time.Duration { return 0 }

// Begin implements Vendor: the account is a key the owner pastes, never a login.
func (k *openCodeKey) Begin(context.Context) (*Attempt, error) {
	return nil, fmt.Errorf("%w: an %s account is a key; add it with `accounts add %s <label> --token -`",
		ErrUnknownVendor, k.product, k.runtime)
}

// Refresh implements Vendor: a key has nothing to refresh.
func (k *openCodeKey) Refresh(context.Context, vault.Payload) (vault.Refreshed, error) {
	return vault.Refreshed{}, fmt.Errorf("an %s key is not refreshed", k.product)
}

// Identify implements Vendor: a key names nobody.
func (*openCodeKey) Identify(vault.Payload) (Identity, error) { return Identity{}, nil }

// header is what every call to OpenCode carries: the key and the gateway's name, as Go asks of a client (go.mdx
// "Where can I use it?"); Zen is called the same way, with no OpenCode identity (owner, 2026-11-20).
func (*openCodeKey) header(token Token) http.Header {
	header := http.Header{}
	header.Set("Authorization", "Bearer "+token.AccessToken)
	header.Set("User-Agent", "hypomnema/"+platform.Version)

	return header
}

// OpenCodeGo is the Vendor for OpenCode Go keys.
type OpenCodeGo struct{ openCodeKey }

// NewOpenCodeGo returns the Vendor for OpenCode Go keys; base is OpenCode's
// origin, empty for opencode.ai.
func NewOpenCodeGo(base string, clock platform.Clock) *OpenCodeGo {
	return &OpenCodeGo{newOpenCodeKey(OpenCodeGoRuntime, "OpenCode Go", base, clock)}
}

// openCodeModel is one row of Go's model list, or one model of the Console list (opencode
// console/api/models.mdx: the same models.dev fields).
type openCodeModel struct {
	ID    string                     `json:"id"`
	Type  string                     `json:"type"`
	API   map[string]json.RawMessage `json:"api"`
	Limit struct {
		Context int64 `json:"context"`
		Output  int64 `json:"output"`
	} `json:"limit"`
	Cost struct {
		Text struct {
			Input struct {
				Price *float64 `json:"price"`
			} `json:"input"`
			Output struct {
				Price *float64 `json:"price"`
			} `json:"output"`
		} `json:"text"`
	} `json:"cost"`
}

// wire is the first of the wires the gateway calls that the model is served
// on; empty for a model served on none of them (Gemini's generate-content,
// left out by issues #854 and #965).
func (m openCodeModel) wire() string {
	for _, w := range []string{WireChat, WireResponses, WireMessages} {
		if _, ok := m.API[w]; ok {
			return w
		}
	}

	return ""
}

// Models implements ModelLister with Go's own list: every chat model on a
// wire the gateway calls, with its window and largest output.
func (g *OpenCodeGo) Models(ctx context.Context, token Token) ([]ServedModel, error) {
	var answer struct {
		Data []openCodeModel `json:"data"`
	}

	if err := getModels(ctx, g.client, g.base+"/inference/go/v1/models", g.header(token), &answer); err != nil {
		return nil, err
	}

	models := []ServedModel{}

	for _, m := range answer.Data {
		if served, ok := m.served(); ok {
			models = append(models, served)
		}
	}

	return sortedModels(models), nil
}

// served is the model as the gateway serves it, or ok false for a model that is not chat or is on no wire the
// gateway calls.
func (m openCodeModel) served() (ServedModel, bool) {
	wire := m.wire()
	if m.ID == "" || wire == "" || (m.Type != "" && m.Type != "chat") {
		return ServedModel{}, false
	}

	return ServedModel{Name: m.ID, Remaining: -1, Window: m.Limit.Context, Output: m.Limit.Output, Wire: wire}, true
}

// openCodeGoWindow is one window of Go's usage answer.
type openCodeGoWindow struct {
	Percent  *float64        `json:"percent"`
	Status   string          `json:"status"`
	ResetsAt json.RawMessage `json:"resetsAt"`
}

// Limits implements LimitReader with Go's usage call: the 5 hours (rolling),
// the week and the month, each a percent used and its reset. A window Go says
// is rate-limited is used up whatever its percent (omp opencode-go.ts
// resolveStatus).
func (g *OpenCodeGo) Limits(ctx context.Context, token Token) ([]Window, error) {
	body, err := getLimits(ctx, g.client, g.base+"/zen/go/v1/usage", g.header(token))
	if err != nil {
		return nil, err
	}

	return openCodeGoWindows(body)
}

func openCodeGoWindows(body []byte) ([]Window, error) {
	var answer struct {
		Usage map[string]openCodeGoWindow `json:"usage"`
	}

	if err := json.Unmarshal(body, &answer); err != nil {
		return nil, fmt.Errorf("reading OpenCode Go's usage: %w", err)
	}

	var windows []Window

	for _, w := range []struct{ key, name string }{
		{"rolling", WindowFiveHours}, {"weekly", WindowWeek}, {"monthly", WindowMonth},
	} {
		got, ok := answer.Usage[w.key]
		if !ok || got.Percent == nil {
			continue
		}

		used := percent(*got.Percent)
		if got.Status == "rate-limited" {
			used = 100
		}

		windows = append(windows, Window{Name: w.name, UsedPercent: used, ResetsAt: resetTime(got.ResetsAt)})
	}

	if len(windows) == 0 {
		return nil, errors.New("OpenCode Go's usage named no window")
	}

	return windows, nil
}

// limitsPlan is the plan to mark an account with after its limits call
// answered status, and whether there is one to set: a 403 is a key with no Go
// subscription, and an answer is a key with one, as omp flags a key on each
// check (opencode-go.ts: 401 and 403 throw so checkCredentials marks it, an
// answer marks it good).
func (*OpenCodeGo) limitsPlan(status int, current string) (string, bool) {
	switch {
	case status == http.StatusForbidden:
		return openCodeGoNoPlan, current != openCodeGoNoPlan
	case status == http.StatusOK && current == openCodeGoNoPlan:
		return "", true
	default:
		return "", false
	}
}
