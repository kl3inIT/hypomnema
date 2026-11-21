package modelgateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway/claude"
	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway/codex"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

// OpenCode Go (issue #854): OpenCode's subscription for open models, a
// built-in provider served as opencode-go/<model> under the Go keys the owner
// pastes, each an account of runtime opencode_go. OpenCode Zen (issue #965):
// OpenCode's models paid from the workspace's balance, served the same way as
// opencode-zen/<model> under the Zen keys, each an account of runtime
// opencode_zen, from OpenCode's inference root rather than Go's (opencode
// console/api/inference.mdx "Endpoints"). What it does is learned from the
// references (docs/research/2026-11-18-opencode-go-provider.md,
// docs/research/2026-11-20-opencode-zen-provider.md):
//
//   - each model is called on its own wire, not translated: OpenAI's chat at
//     /openai/v1/chat/completions, OpenAI's Responses at /openai/v1/responses,
//     Anthropic's Messages at /anthropic/v1/messages, as Go's model list names
//     it (opencode console/go.mdx; 9router supportedFormats; hermes, litellm,
//     pi). A route that speaks that wire passes the body on; the chat route
//     translates to the other two with the Claude and Codex translations;
//   - a Bearer key, x-api-key and anthropic-version on the Messages path, a
//     stable x-opencode-session per conversation and the client's own
//     User-Agent (go.mdx "Where can I use it?"; bifrost
//     core/providers/opencode/utils.go; pi utils/identity.ts);
//   - a 429 "Monthly usage limit reached. Resets in N days" or a
//     GoUsageLimitError is a used-up allowance: the key rests until the reset
//     and the next key is tried, the same key never again for that request
//     (omniroute open-sse/config/providerErrorRules.ts
//     opencode-monthly-quota-resets-in; opencode packages/ai/src/provider-error.ts
//     QUOTA_CODES, not retried);
//   - on Zen, a 402 or one of OpenCode's quota codes is a used-up balance or
//     budget: the key rests for an hour (quotaCooldown) and the next key is
//     tried (opencode provider-error.ts QUOTA_CODES and its 402; omniroute
//     providerErrorRules.ts catchAllReason, 402 is quota_exhausted);
//   - a 401 marks the key refused, on either provider.

const (
	// openCodeGoKind is the provider kind (orchestration.ProviderOpenCodeGo).
	openCodeGoKind = "opencode_go"
	// openCodeGoRuntime is the runtime of a Go key's account.
	openCodeGoRuntime = "opencode_go"
	// openCodeGoBaseURL is Go's inference root (opencode go.mdx; litellm
	// llms/opencode/common_utils.py).
	openCodeGoBaseURL = "https://opencode.ai/inference/go"
	// openCodeZenKind is the provider kind (orchestration.ProviderOpenCodeZen).
	openCodeZenKind = "opencode_zen"
	// openCodeZenRuntime is the runtime of a Zen key's account.
	openCodeZenRuntime = "opencode_zen"
	// openCodeZenBaseURL is OpenCode's inference root, where Zen's models are
	// served (opencode console/api/inference.mdx), beside Go's /inference/go
	// rather than the references' older /zen/v1.
	openCodeZenBaseURL = "https://opencode.ai/inference"
	// openCodeGoAnthropicVersion is the Messages API version sent on Go's
	// Anthropic path, as Anthropic's SDK sends it.
	openCodeGoAnthropicVersion = "2023-06-01"
	// openCodeGoSessionHeader carries the conversation's id (go.mdx).
	openCodeGoSessionHeader = "X-Opencode-Session"
)

// The wires an OpenCode model is served on, as its model list names them
// (subscription.WireChat, WireResponses, WireMessages).
const (
	goWireChat      = "chat-completions"
	goWireResponses = "responses"
	goWireMessages  = "messages"
)

// openCodeProvider is one of OpenCode's providers keyed by a Console API key.
type openCodeProvider struct {
	kind, runtime string
	// name is what the provider is called in what the gateway says.
	name string
	// zen says the provider is Zen: its own inference root and its refusals.
	zen bool
}

var (
	openCodeGoProvider  = openCodeProvider{kind: openCodeGoKind, runtime: openCodeGoRuntime, name: "OpenCode Go"}
	openCodeZenProvider = openCodeProvider{
		kind: openCodeZenKind, runtime: openCodeZenRuntime, name: "OpenCode Zen", zen: true,
	}
)

// openCodeOf is the OpenCode provider of a kind, or ok false for another kind.
func openCodeOf(kind string) (openCodeProvider, bool) {
	switch kind {
	case openCodeGoKind:
		return openCodeGoProvider, true
	case openCodeZenKind:
		return openCodeZenProvider, true
	default:
		return openCodeProvider{}, false
	}
}

// openCodeURL is the provider's inference root.
func (s *Server) openCodeURL(p openCodeProvider) string {
	if p.zen {
		return s.zenURL
	}

	return s.goURL
}

// openCodeUserAgent is how the gateway names itself to OpenCode, which asks a
// client for its own name rather than a library's (go.mdx); it never names
// itself OpenCode (owner, 2026-11-20).
func openCodeUserAgent() string { return "hypomnema/" + platform.Version }

// openCodeWire is the wire an OpenCode provider serves a model on: what its
// model list said, else chat, the provider's own API in models.dev
// (opencode-go: npm @ai-sdk/openai-compatible, api .../openai/v1).
func (s *Service) openCodeWire(ctx context.Context, kind, model string) string {
	if s.wire != nil {
		if wire, ok := s.wire(ctx, kind, model); ok {
			return wire
		}
	}

	return goWireChat
}

// openCodeGoSession is the x-opencode-session of a conversation: a hash of the
// gateway's own session, so it is stable for the conversation and names
// nothing of the client to OpenCode. Go and Zen are sent the same one.
func openCodeGoSession(session string) string {
	sum := sha256.Sum256([]byte("hypomnema:opencode-go:" + session))

	return hex.EncodeToString(sum[:16])
}

// chatSession is a chat request's session: the client's prompt cache key,
// else its gateway key, as the Codex adapter keeps one per key (cacheKey).
func chatSession(call chatCall) string {
	var key string
	if raw, ok := call.req.fields["prompt_cache_key"]; ok && json.Unmarshal(raw, &key) == nil && key != "" {
		return key
	}

	return "key:" + call.keyID
}

// openCode serves chat from OpenCode Go or OpenCode Zen.
type openCode struct{ p openCodeProvider }

func (openCode) declared() bool { return false }

func (a openCode) do(ctx context.Context, s *Server, call chatCall) (*http.Response, error) {
	if s.accounts == nil {
		return nil, fmt.Errorf("%w: provider %q has no accounts to run under", ErrNoAccount, call.upstream.Provider)
	}

	session := openCodeGoSession(chatSession(call))

	var (
		resp *http.Response
		err  error
	)

	switch s.svc.openCodeWire(ctx, a.p.kind, call.upstream.Model) {
	case goWireMessages:
		resp, err = s.openCodeChatAsMessages(ctx, a.p, call, session)
	case goWireResponses:
		resp, err = s.openCodeChatAsResponses(ctx, a.p, call, session)
	default:
		var body []byte
		if body, err = call.req.forUpstream(call.upstream.Model); err != nil {
			return nil, err
		}

		accept := "application/json"
		if call.req.stream {
			accept = "text/event-stream"
		}

		resp, err = s.underOpenCode(ctx, a.p, call.upstream.Model, session, goWireChat, body, accept, nil)
	}

	if err != nil {
		return nil, fmt.Errorf("provider %q: %w", call.upstream.Provider, err)
	}

	return resp, nil
}

// openCodeChatAsMessages asks a Messages model with a chat request, translated
// as for a Claude subscription but without the disguise, and its answer turned
// back into chat.
func (s *Server) openCodeChatAsMessages(
	ctx context.Context, p openCodeProvider, call chatCall, session string,
) (*http.Response, error) {
	built, err := claude.BuildRequest(call.req.raw, claude.Options{Model: call.upstream.Model, Plain: true})
	if err != nil {
		//nolint:nilerr // A request that cannot be translated is the client's to fix: a 400, not a failover.
		return claudeRefusal(&claude.Failure{
			Status: http.StatusBadRequest, Type: typeInvalidRequest, Code: "invalid_request", Message: err.Error(),
		}, s.svc.clock.Now()), nil
	}

	return s.underOpenCode(ctx, p, call.upstream.Model, session, goWireMessages, built.Body, "text/event-stream",
		func(token AccountToken, resp *http.Response) *http.Response {
			now := s.svc.clock.Now()
			stream := claude.NewStream(s.svc.newID("chatcmpl"), now.Unix(), call.model, built.Names)

			return s.translated(ctx, call, resp, translation{
				event: stream.Event, finished: stream.Finished, completion: stream.Completion,
				failed: func() bool { return stream.Failed() != nil },
				refusal: func() *http.Response {
					failure := stream.Failed()
					if failure == nil {
						failure = &claude.Failure{
							Status: http.StatusBadGateway, Type: typeServer, Code: "upstream_truncated",
							Message: p.name + " ended its answer early",
						}
					}

					s.openCodeCool(ctx, p, token, failure.Status, []byte(failure.Message), failure.ResetsAt)

					return claudeRefusal(failure, now)
				},
			})
		})
}

// openCodeChatAsResponses asks a Responses model with a chat request,
// translated as for a Codex subscription but without the lite shape, and its
// answer turned back into chat.
func (s *Server) openCodeChatAsResponses(
	ctx context.Context, p openCodeProvider, call chatCall, session string,
) (*http.Response, error) {
	built, err := codex.BuildRequest(call.req.raw, codex.Options{
		Model: call.upstream.Model, CacheKey: session, Plain: true,
	})
	if err != nil {
		//nolint:nilerr // A request that cannot be translated is the client's to fix: a 400, not a failover.
		return codexRefusal(&codex.Failure{
			Status: http.StatusBadRequest, Type: typeInvalidRequest, Code: "invalid_request", Message: err.Error(),
		}, s.svc.clock.Now()), nil
	}

	return s.underOpenCode(ctx, p, call.upstream.Model, session, goWireResponses, built.Body, "text/event-stream",
		func(token AccountToken, resp *http.Response) *http.Response {
			now := s.svc.clock.Now()
			stream := codex.NewStream(s.svc.newID("chatcmpl"), now.Unix(), call.model, built.Names)

			return s.translated(ctx, call, resp, translation{
				event: stream.Event, finished: stream.Finished, completion: stream.Completion,
				failed: func() bool { return stream.Failed() != nil },
				refusal: func() *http.Response {
					failure := stream.Failed()
					if failure == nil {
						failure = &codex.Failure{
							Status: http.StatusBadGateway, Type: typeServer, Code: "upstream_truncated",
							Message: p.name + " ended its answer early",
						}
					}

					s.openCodeCool(ctx, p, token, failure.Status, []byte(failure.Message), failure.ResetsAt)

					return codexRefusal(failure, now)
				},
			})
		})
}

// goPaths are where each wire is called under Go's or Zen's inference root
// (go.mdx; inference.mdx "Endpoints").
var goPaths = map[string]string{
	goWireChat:      "/openai/v1/chat/completions",
	goWireResponses: "/openai/v1/responses",
	goWireMessages:  "/anthropic/v1/messages",
}

// underOpenCode sends a body to an OpenCode provider on a wire under its keys
// in turn: a key it refuses rests and the next is tried (underAccounts).
// answer, when given, turns a 200 into the answer; without it the 200 is the
// answer. A Zen key goes only to a model its own workspace's Console list
// names, as Codex and Antigravity accounts do (issue #814): the list is the
// workspace's (opencode console/api/models.mdx "Every provider and model the
// caller can use"), and two keys may be of two workspaces. Go's list is the
// same for every key.
func (s *Server) underOpenCode(
	ctx context.Context, p openCodeProvider, model, session, wire string, body []byte, accept string,
	answer func(AccountToken, *http.Response) *http.Response,
) (*http.Response, error) {
	attempt := func(token AccountToken) (*http.Response, error) {
		resp, err := s.openCodePost(ctx, p, token, session, wire, body, accept)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode != http.StatusOK {
			return s.openCodeRefused(ctx, p, token, resp), nil
		}

		s.accountState.served(token.AccountID)

		if answer == nil {
			return resp, nil
		}

		return answer(token, resp), nil
	}
	accountsFor := forModel{}
	if p.zen {
		accountsFor = listedModel(model)
	}

	return s.underModelAccounts(ctx, p.runtime, accountsFor, session, nil, nil, attempt)
}

// openCodePost sends a body to an OpenCode provider on a wire under a key.
func (s *Server) openCodePost(
	ctx context.Context, p openCodeProvider, token AccountToken, session, wire string, body []byte, accept string,
) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.openCodeURL(p)+goPaths[wire],
		bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("building the request to %s: %w", p.name, err)
	}

	if wire == goWireMessages {
		req.Header.Set("X-Api-Key", token.AccessToken)
		req.Header.Set("Anthropic-Version", openCodeGoAnthropicVersion)
	} else {
		req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", openCodeUserAgent())
	req.Header.Set(openCodeGoSessionHeader, session)

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling %s: %w", p.name, err)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		s.markTokenRefused(ctx, token)
	}

	return resp, nil
}

// openCodeRefused passes on a request OpenCode refused, its status and its
// body, and rests the key for as long as the refusal calls for (openCodeCool).
func (s *Server) openCodeRefused(
	ctx context.Context, p openCodeProvider, token AccountToken, resp *http.Response,
) *http.Response {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBytes))
	_ = resp.Body.Close()

	now := s.svc.clock.Now()

	var reset time.Time
	if secs, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); err == nil && secs > 0 {
		reset = now.Add(time.Duration(secs) * time.Second)
	}

	until := s.openCodeCool(ctx, p, token, resp.StatusCode, body, reset)

	out := &http.Response{
		StatusCode: resp.StatusCode, Header: http.Header{"Content-Type": {resp.Header.Get("Content-Type")}},
		Body: io.NopCloser(bytes.NewReader(body)),
	}

	// A used-up Zen balance or budget that OpenCode answers on a status the next key is not tried on (a 400
	// insufficient_quota) is answered as the 402 opencode reads it as (provider-error.ts classifyProviderFailure:
	// a 402 and a quota code are one QuotaExceededError), so the next key is tried (movesOn) as on a 402.
	if p.zen && !movesOn(resp.StatusCode) && zenQuotaUsedUp(resp.StatusCode, body) {
		out.StatusCode = http.StatusPaymentRequired
	}

	if out.Header.Get("Content-Type") == "" {
		out.Header.Set("Content-Type", "application/json")
	}

	if until.After(now) {
		out.Header.Set("Retry-After", strconv.Itoa(int(until.Sub(now).Seconds())+1))
	}

	return out
}

// openCodeCool rests a key after a refusal and says until when, when it
// knows: a used-up Go allowance until the reset Go names, uncapped as
// omniroute keeps its cooldown, else an hour (quotaCooldown); a used-up Zen
// balance or budget an hour, whatever Retry-After says (zenQuotaUsedUp);
// anything else as every built-in
// provider's account rests (cool).
func (s *Server) openCodeCool(
	ctx context.Context, p openCodeProvider, token AccountToken, status int, body []byte, reset time.Time,
) time.Time {
	now := s.svc.clock.Now()

	if p.zen {
		if zenQuotaUsedUp(status, body) {
			// An hour whatever Retry-After says: a used-up balance or budget comes back only when the owner adds
			// credit or raises the budget, not after a throttle's wait (opencode provider-error.ts: a quota is
			// not retried; 9router-go QuotaExhaustedCooldownMs).
			until := now.Add(quotaCooldown)
			s.park(ctx, p.runtime, token, until, "OpenCode Zen balance or budget used up")

			return until
		}

		s.cool(ctx, p.runtime, token, status, false, reset, "OpenCode Zen refused the key")

		return reset
	}

	usedUp, quotaReset := goUsageLimit(status, body, now)
	if usedUp && quotaReset.After(now) {
		s.park(ctx, p.runtime, token, quotaReset, "OpenCode Go usage limit")

		return quotaReset
	}

	s.cool(ctx, p.runtime, token, status, usedUp, reset, "OpenCode Go refused the key")

	return reset
}

// zenQuotaCodes are the codes of OpenCode's account caps that a Zen key can
// meet: a balance, a plan or a budget used up, not a throttle (opencode
// packages/ai/src/provider-error.ts QUOTA_CODES, less Go's and the free
// tier's, which a Zen key's paid models do not answer).
var zenQuotaCodes = map[string]bool{
	"insufficient_quota": true, "usage_not_included": true, "billing_error": true,
	"budgetlimiterror": true, "creditlimitexceeded": true,
}

// zenCodeLabel is the `[code]` OpenCode keeps at the start of a message whose
// upstream code it rewrote (provider-error.ts GATEWAY_CODE_LABEL).
var zenCodeLabel = regexp.MustCompile(`^[^:\n]+: \[([A-Za-z0-9_.-]+)\]`)

// zenQuotaUsedUp reads whether a Zen refusal says the key's balance or budget
// is used up: a 402, or a quota code where opencode reads one
// (provider-error.ts classifyProviderFailure; utils/provider-codes.ts).
func zenQuotaUsedUp(status int, body []byte) bool {
	if status == http.StatusPaymentRequired {
		return true
	}

	for _, code := range openCodeErrorCodes(body) {
		if zenQuotaCodes[strings.ToLower(code)] {
			return true
		}
	}

	return false
}

// openCodeErrorCodes are the codes an error body carries where opencode reads
// them (utils/provider-codes.ts providerCodes: code, error_type, error.code,
// error.type, error.error_type), and the `[code]` label of its message.
func openCodeErrorCodes(body []byte) []string {
	var codes []string

	var answer struct {
		Code      any `json:"code"`
		ErrorType any `json:"error_type"`
		Error     any `json:"error"`
		Message   any `json:"message"`
	}

	if json.Unmarshal(body, &answer) != nil {
		if m := zenCodeLabel.FindStringSubmatch(string(body)); m != nil {
			codes = append(codes, m[1])
		}

		return codes
	}

	str := func(v any) {
		if text, ok := v.(string); ok {
			codes = append(codes, text)
		}
	}

	label := func(v any) {
		if text, ok := v.(string); ok {
			if m := zenCodeLabel.FindStringSubmatch(text); m != nil {
				codes = append(codes, m[1])
			}
		}
	}

	str(answer.Code)
	str(answer.ErrorType)
	label(answer.Message)

	if inner, ok := answer.Error.(map[string]any); ok {
		str(inner["code"])
		str(inner["type"])
		str(inner["error_type"])
		label(inner["message"])
	}

	return codes
}

// goResetIn is Go's "Resets in N days" (omniroute parseResetCountdownMs).
var goResetIn = regexp.MustCompile(`resets?\s+in\s+(\d+)\s+(day|days|hour|hours|minute|minutes|second|seconds)\b`)

// goUsageLimit reads whether a refusal says the key's Go allowance is used up,
// and when it resets when it says: a 429 "Monthly usage limit reached"
// (omniroute opencode-monthly-quota-resets-in) or a GoUsageLimitError, which
// opencode does not retry (provider-error.ts QUOTA_CODES).
func goUsageLimit(status int, body []byte, now time.Time) (usedUp bool, reset time.Time) {
	text := strings.ToLower(string(body))

	usedUp = strings.Contains(text, "gousagelimiterror") ||
		(status == http.StatusTooManyRequests && strings.Contains(text, "monthly usage limit reached"))
	if !usedUp {
		return false, time.Time{}
	}

	m := goResetIn.FindStringSubmatch(text)
	if m == nil {
		return true, time.Time{}
	}

	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		return true, time.Time{}
	}

	unit := map[string]time.Duration{
		"day": 24 * time.Hour, "hour": time.Hour, "minute": time.Minute, "second": time.Second,
	}[strings.TrimSuffix(m[2], "s")]

	return true, now.Add(time.Duration(n) * unit)
}

// withModel is a JSON body with the model's name replaced, everything else as
// the client sent it.
func withModel(raw []byte, model string, extra map[string]json.RawMessage) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, fmt.Errorf("the body is not a JSON object")
	}

	name, err := json.Marshal(model)
	if err != nil {
		return nil, fmt.Errorf("encoding the model's name: %w", err)
	}

	fields["model"] = name
	maps.Copy(fields, extra)

	body, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("encoding the request: %w", err)
	}

	return body, nil
}
