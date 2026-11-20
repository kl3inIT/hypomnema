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
// pastes, each an account of runtime opencode_go. What it does is learned
// from the references (docs/research/2026-11-18-opencode-go-provider.md):
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
//     QUOTA_CODES, not retried).

const (
	// openCodeGoKind is the provider kind (orchestration.ProviderOpenCodeGo).
	openCodeGoKind = "opencode_go"
	// openCodeGoRuntime is the runtime of a Go key's account.
	openCodeGoRuntime = "opencode_go"
	// openCodeGoBaseURL is Go's inference root (opencode go.mdx; litellm
	// llms/opencode/common_utils.py).
	openCodeGoBaseURL = "https://opencode.ai/inference/go"
	// openCodeGoAnthropicVersion is the Messages API version sent on Go's
	// Anthropic path, as Anthropic's SDK sends it.
	openCodeGoAnthropicVersion = "2023-06-01"
	// openCodeGoSessionHeader carries the conversation's id (go.mdx).
	openCodeGoSessionHeader = "X-Opencode-Session"
)

// The wires a Go model is served on, as Go's model list names them
// (subscription.WireChat, WireResponses, WireMessages).
const (
	goWireChat      = "chat-completions"
	goWireResponses = "responses"
	goWireMessages  = "messages"
)

// openCodeGoUserAgent is how the gateway names itself to Go, which asks a
// client for its own name rather than a library's (go.mdx).
func openCodeGoUserAgent() string { return "hypomnema/" + platform.Version }

// goWire is the wire Go serves a model on: what its model list said, else
// chat, the provider's own API in models.dev (opencode-go: npm
// @ai-sdk/openai-compatible, api .../openai/v1).
func (s *Service) goWire(ctx context.Context, model string) string {
	if s.wire != nil {
		if wire, ok := s.wire(ctx, openCodeGoKind, model); ok {
			return wire
		}
	}

	return goWireChat
}

// openCodeGoSession is the x-opencode-session of a conversation: a hash of the
// gateway's own session, so it is stable for the conversation and names
// nothing of the client to Go.
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

// openCodeGo serves chat from OpenCode Go.
type openCodeGo struct{}

func (openCodeGo) declared() bool { return false }

func (openCodeGo) do(ctx context.Context, s *Server, call chatCall) (*http.Response, error) {
	if s.accounts == nil {
		return nil, fmt.Errorf("%w: provider %q has no accounts to run under", ErrNoAccount, call.upstream.Provider)
	}

	session := openCodeGoSession(chatSession(call))

	var (
		resp *http.Response
		err  error
	)

	switch s.svc.goWire(ctx, call.upstream.Model) {
	case goWireMessages:
		resp, err = s.goChatAsMessages(ctx, call, session)
	case goWireResponses:
		resp, err = s.goChatAsResponses(ctx, call, session)
	default:
		var body []byte
		if body, err = call.req.forUpstream(call.upstream.Model); err != nil {
			return nil, err
		}

		accept := "application/json"
		if call.req.stream {
			accept = "text/event-stream"
		}

		resp, err = s.underGo(ctx, session, goWireChat, body, accept, nil)
	}

	if err != nil {
		return nil, fmt.Errorf("provider %q: %w", call.upstream.Provider, err)
	}

	return resp, nil
}

// goChatAsMessages asks a Messages model with a chat request, translated as
// for a Claude subscription but without the disguise, and its answer turned
// back into chat.
func (s *Server) goChatAsMessages(ctx context.Context, call chatCall, session string) (*http.Response, error) {
	built, err := claude.BuildRequest(call.req.raw, claude.Options{Model: call.upstream.Model, Plain: true})
	if err != nil {
		//nolint:nilerr // A request that cannot be translated is the client's to fix: a 400, not a failover.
		return claudeRefusal(&claude.Failure{
			Status: http.StatusBadRequest, Type: typeInvalidRequest, Code: "invalid_request", Message: err.Error(),
		}, s.svc.clock.Now()), nil
	}

	return s.underGo(ctx, session, goWireMessages, built.Body, "text/event-stream",
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
							Message: "OpenCode Go ended its answer early",
						}
					}

					s.goCool(ctx, token, failure.Status, []byte(failure.Message), failure.ResetsAt)

					return claudeRefusal(failure, now)
				},
			})
		})
}

// goChatAsResponses asks a Responses model with a chat request, translated as
// for a Codex subscription but without the lite shape, and its answer turned
// back into chat.
func (s *Server) goChatAsResponses(ctx context.Context, call chatCall, session string) (*http.Response, error) {
	built, err := codex.BuildRequest(call.req.raw, codex.Options{
		Model: call.upstream.Model, CacheKey: session, Plain: true,
	})
	if err != nil {
		//nolint:nilerr // A request that cannot be translated is the client's to fix: a 400, not a failover.
		return codexRefusal(&codex.Failure{
			Status: http.StatusBadRequest, Type: typeInvalidRequest, Code: "invalid_request", Message: err.Error(),
		}, s.svc.clock.Now()), nil
	}

	return s.underGo(ctx, session, goWireResponses, built.Body, "text/event-stream",
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
							Message: "OpenCode Go ended its answer early",
						}
					}

					s.goCool(ctx, token, failure.Status, []byte(failure.Message), failure.ResetsAt)

					return codexRefusal(failure, now)
				},
			})
		})
}

// goPaths are where each wire is called under Go's inference root.
var goPaths = map[string]string{
	goWireChat:      "/openai/v1/chat/completions",
	goWireResponses: "/openai/v1/responses",
	goWireMessages:  "/anthropic/v1/messages",
}

// underGo sends a body to Go on a wire under its keys in turn: a key Go
// refuses rests and the next is tried (underAccounts). answer, when given,
// turns a 200 into the answer; without it the 200 is the answer.
func (s *Server) underGo(
	ctx context.Context, session, wire string, body []byte, accept string,
	answer func(AccountToken, *http.Response) *http.Response,
) (*http.Response, error) {
	attempt := func(token AccountToken) (*http.Response, error) {
		resp, err := s.goPost(ctx, token, session, wire, body, accept)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode != http.StatusOK {
			return s.goRefused(ctx, token, resp), nil
		}

		s.accountState.served(token.AccountID)

		if answer == nil {
			return resp, nil
		}

		return answer(token, resp), nil
	}
	return s.underModelAccounts(ctx, openCodeGoRuntime, forModel{}, session, nil, nil, attempt)
}

// goPost sends a body to Go on a wire under a key.
func (s *Server) goPost(
	ctx context.Context, token AccountToken, session, wire string, body []byte, accept string,
) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.goURL+goPaths[wire], bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("building the request to OpenCode Go: %w", err)
	}

	if wire == goWireMessages {
		req.Header.Set("X-Api-Key", token.AccessToken)
		req.Header.Set("Anthropic-Version", openCodeGoAnthropicVersion)
	} else {
		req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", openCodeGoUserAgent())
	req.Header.Set(openCodeGoSessionHeader, session)

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling OpenCode Go: %w", err)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		s.markTokenRefused(ctx, token)
	}

	return resp, nil
}

// goRefused passes on a request Go refused, its status and its body, and
// rests the key for as long as the refusal calls for (goCool).
func (s *Server) goRefused(ctx context.Context, token AccountToken, resp *http.Response) *http.Response {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBytes))
	_ = resp.Body.Close()

	now := s.svc.clock.Now()

	var reset time.Time
	if secs, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); err == nil && secs > 0 {
		reset = now.Add(time.Duration(secs) * time.Second)
	}

	until := s.goCool(ctx, token, resp.StatusCode, body, reset)

	out := &http.Response{
		StatusCode: resp.StatusCode, Header: http.Header{"Content-Type": {resp.Header.Get("Content-Type")}},
		Body: io.NopCloser(bytes.NewReader(body)),
	}

	if out.Header.Get("Content-Type") == "" {
		out.Header.Set("Content-Type", "application/json")
	}

	if until.After(now) {
		out.Header.Set("Retry-After", strconv.Itoa(int(until.Sub(now).Seconds())+1))
	}

	return out
}

// goCool rests a key after a refusal and says until when, when it knows: a
// used-up allowance until the reset Go names, uncapped as omniroute keeps its
// cooldown, else an hour (quotaCooldown); anything else as every built-in
// provider's account rests (cool).
func (s *Server) goCool(ctx context.Context, token AccountToken, status int, body []byte, reset time.Time) time.Time {
	now := s.svc.clock.Now()

	usedUp, quotaReset := goUsageLimit(status, body, now)
	if usedUp && quotaReset.After(now) {
		s.park(ctx, openCodeGoRuntime, token, quotaReset, "OpenCode Go usage limit")

		return quotaReset
	}

	s.cool(ctx, openCodeGoRuntime, token, status, usedUp, reset, "OpenCode Go refused the key")

	return reset
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
