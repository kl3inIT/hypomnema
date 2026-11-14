package modelgateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway/claude"
)

// The Claude subscription (issue #20, step 5): a built-in provider kind. The
// client speaks chat completions; the backend takes Anthropic's Messages API,
// always as a stream, under a Claude Pro/Max account the owner logged in on
// the control plane. The translation and the disguise are package claude's;
// this file is the call itself: the account's token, the headers Anthropic
// requires of Claude Code, a token it refuses, a usage limit, and an error
// inside the stream. It follows 9router-go (internal/providers/providers.go
// the "claude" entry, internal/proxy/executor/claude_messages.go).

const (
	// claudeBaseURL is the backend Claude Code calls with a subscription token.
	claudeBaseURL = "https://api.anthropic.com"
	// claudeAccountRuntime is the kind of account the provider runs under.
	claudeAccountRuntime = "claude"
	// claudeAnthropicVersion is the API version Claude Code pins.
	claudeAnthropicVersion = "2023-06-01"
	// claudeUserAgent and claudeApp are how Claude Code names itself.
	claudeUserAgent = "claude-cli/" + claude.CLIVersion + " (external, sdk-cli)"
	claudeApp       = "cli"
	// claudeBeta is the beta set Claude Code sends; oauth-2025-04-20 is what
	// lets a subscription OAuth token stand in a Bearer header (9router-go
	// providers.go, the "claude" StaticHeaders); extended-cache-ttl-2025-04-11
	// goes with the one-hour breakpoints (CLIProxyAPI, issue #397).
	claudeBeta = "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14," +
		"context-management-2025-06-27,prompt-caching-scope-2026-01-05,advanced-tool-use-2025-11-20," +
		"effort-2025-11-24,structured-outputs-2025-12-15,fast-mode-2026-02-01,redact-thinking-2026-02-12," +
		"token-efficient-tools-2026-03-28,extended-cache-ttl-2025-04-11"
)

// claudeSubscription serves chat from the Anthropic Messages backend.
type claudeSubscription struct{}

func (claudeSubscription) declared() bool { return false }

func (claudeSubscription) do(ctx context.Context, s *Server, call chatCall) (*http.Response, error) {
	if s.accounts == nil {
		return nil, fmt.Errorf("%w: provider %q has no accounts to run under", ErrNoAccount, call.upstream.Provider)
	}

	built, err := claude.BuildRequest(call.req.raw, claude.Options{
		Model: call.upstream.Model, Session: claudeSession(call.keyID),
	})
	if err != nil {
		//nolint:nilerr // A request that cannot be translated is the client's to fix: a 400, not a failover.
		return claudeRefusal(&claude.Failure{
			Status: http.StatusBadRequest, Type: typeInvalidRequest, Code: "invalid_request", Message: err.Error(),
		}, s.svc.clock.Now()), nil
	}

	resp, err := s.underAccounts(ctx, claudeAccountRuntime, built.Session, nil,
		func(token AccountToken) (*http.Response, error) {
			resp, err := s.claudeCall(ctx, &token, built)
			if err != nil {
				return nil, err
			}

			if resp.StatusCode != http.StatusOK {
				return s.claudeRefused(ctx, token, resp), nil
			}

			s.accountState.served(token.AccountID)

			return s.claudeAnswer(ctx, call, token, built, resp), nil
		})
	if err != nil {
		return nil, fmt.Errorf("provider %q: %w", call.upstream.Provider, err)
	}

	return resp, nil
}

// claudeSession is the session a request without one is sent under, so one
// client keeps one prompt cache and one metadata session id.
func claudeSession(keyID string) string {
	if keyID == "" {
		return ""
	}

	return "claude:" + keyID
}

// claudeCall sends a built request under an account. A token Anthropic refuses
// is refreshed once and the request sent again; token is then the new one.
func (s *Server) claudeCall(ctx context.Context, token *AccountToken, built claude.Request) (*http.Response, error) {
	resp, err := s.claudePost(ctx, *token, built)
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}

	drain(resp)

	if *token, err = s.accounts.TokenAfterRefusal(ctx, *token); err != nil {
		return nil, err
	}

	return s.claudePost(ctx, *token, built)
}

// claudePost sends a built request under an account, as Claude Code would. The
// subscription token stands in the Bearer header; the body already carries the
// billing block and the fake user id (package claude).
func (s *Server) claudePost(ctx context.Context, token AccountToken, built claude.Request) (*http.Response, error) {
	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, s.claudeURL+"/v1/messages?beta=true", bytes.NewReader(built.Body))
	if err != nil {
		return nil, fmt.Errorf("building the request to Claude: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("User-Agent", claudeUserAgent)
	req.Header.Set("Anthropic-Version", claudeAnthropicVersion)
	req.Header.Set("Anthropic-Beta", claudeBeta)
	req.Header.Set("Anthropic-Dangerous-Direct-Browser-Access", "true")
	req.Header.Set("X-App", claudeApp)
	req.Header.Set("X-Stainless-Helper-Method", "stream")
	req.Header.Set("X-Stainless-Retry-Count", "0")

	return s.claudeDo(token, req)
}

// claudeDo sends a request to the backend under an account and notes what the
// answer's headers say of the account's limits.
func (s *Server) claudeDo(token AccountToken, req *http.Request) (*http.Response, error) {
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling Claude: %w", err)
	}

	s.noteQuota(token, resp.Header, claudeQuotaLeft)
	s.accounts.NoteAnswer(claudeAccountRuntime, token.AccountID, resp.Header)

	return resp, nil
}

// claudeRefused passes on a request Anthropic refused: its status and its body.
// The account cools down for as long as the kind of failure calls for.
func (s *Server) claudeRefused(ctx context.Context, token AccountToken, resp *http.Response) *http.Response {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBytes))
	_ = resp.Body.Close()

	failure := claude.ParseFailure(resp.StatusCode, body)
	failure.ResetsAt = claudeResetsAt(resp, s.svc.clock.Now())
	s.claudeCool(ctx, token, failure)

	out := &http.Response{
		StatusCode: failure.Status, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(bytes.NewReader(body)),
	}

	if retry := claudeRetryAfter(failure, s.svc.clock.Now()); retry != "" {
		out.Header.Set("Retry-After", retry)
	}

	return out
}

func (s *Server) claudeCool(ctx context.Context, token AccountToken, failure *claude.Failure) {
	usageLimit := failure.Status == http.StatusTooManyRequests
	s.cool(ctx, claudeAccountRuntime, token, failure.Status, usageLimit, failure.ResetsAt, "usage limit")
}

// claudeAnswer turns the backend's event stream into the answer.
func (s *Server) claudeAnswer(
	ctx context.Context, call chatCall, token AccountToken, built claude.Request, resp *http.Response,
) *http.Response {
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
					Message: "Claude ended its answer early",
				}
			}

			s.claudeCool(ctx, token, failure)

			return claudeRefusal(failure, now)
		},
	})
}

// claudeRefusal is an error of the backend as an answer in OpenAI's shape.
func claudeRefusal(failure *claude.Failure, now time.Time) *http.Response {
	var body errorBody

	body.Error.Message, body.Error.Type, body.Error.Code = failure.Message, failure.Type, failure.Code
	if body.Error.Type == "" {
		body.Error.Type = typeServer
	}

	encoded, _ := json.Marshal(body) //nolint:errchkjson // A struct of strings always encodes.
	resp := &http.Response{
		StatusCode: failure.Status, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(bytes.NewReader(encoded)),
	}

	if retry := claudeRetryAfter(failure, now); retry != "" {
		resp.Header.Set("Retry-After", retry)
	}

	return resp
}

// claudeResetsAt reads when a usage limit ends from the answer's headers:
// Anthropic's unified reset (a Unix second), else Retry-After (seconds from now).
func claudeResetsAt(resp *http.Response, now time.Time) time.Time {
	if reset := resp.Header.Get("Anthropic-Ratelimit-Unified-Reset"); reset != "" {
		if secs, ok := claude.RetrySeconds(reset); ok && int64(secs) > now.Unix() {
			return time.Unix(int64(secs), 0)
		}
	}

	if retry := resp.Header.Get("Retry-After"); retry != "" {
		if secs, ok := claude.RetrySeconds(retry); ok {
			return now.Add(time.Duration(secs) * time.Second)
		}
	}

	return time.Time{}
}

func claudeRetryAfter(failure *claude.Failure, now time.Time) string {
	if failure.ResetsAt.IsZero() || !failure.ResetsAt.After(now) {
		return ""
	}

	return strconv.Itoa(int(failure.ResetsAt.Sub(now).Seconds()) + 1)
}
