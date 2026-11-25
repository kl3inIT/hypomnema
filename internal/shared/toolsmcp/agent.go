package toolsmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// The control plane's tools (the shared browser, issue #212; the gateway's,
// issues #354 and #492; the phone's, issue #417) are the control plane's, not
// the machine's: the server calls the control plane's agent API itself with
// the agent's own credential, as OpenMausBot's stdio proxy calls its server
// with a turn's token (server/host/browser.ts) and as this repository's tool
// launcher does (agenttool.Launcher).
const (
	// listTimeout bounds asking for the tools. The first worker to ask may
	// wait for the control plane to fetch its browser.
	listTimeout = 2 * time.Minute
	// CallTimeout is longer than the control plane gives a tool, so that its
	// answer is the one the agent reads: the browser's 3 minutes, and a
	// command on a node (issue #543), 5 minutes and its node's answer
	// (nodeproto.ExecTimeoutMax and ExecAnswerSlack).
	CallTimeout = 7 * time.Minute
	// NodeCallTimeout is longer than a command on a node may take at the
	// control plane: the phone's node's machine woken first (2 minutes, issue
	// #1083), then the command and its node's answer, as CallTimeout.
	NodeCallTimeout = CallTimeout + 2*time.Minute
	// DeviceCallTimeout is longer than a phone's call may take at the control
	// plane: its machine woken (2 minutes), then the node's answer
	// (nodeproto.DeviceAnswerWithin and half a minute).
	DeviceCallTimeout = 11 * time.Minute
	// maxAnswerBytes bounds an answer; a screenshot is the largest.
	maxAnswerBytes = 16 << 20
)

// Agent calls the control plane's agent API for one agent, with its
// credential: a Dispatch's key, a Bot's pi's token, or the owner's token.
type Agent struct {
	url    string
	key    string
	client *http.Client
}

// NewAgent is the agent API at url, called with key; nil when either is
// missing: the agent then has none of the control plane's tools.
func NewAgent(url, key string) *Agent {
	return NewAgentWithClient(url, key, &http.Client{})
}

// NewAgentWithClient uses client for the requests, including its redirect policy.
func NewAgentWithClient(url, key string, client *http.Client) *Agent {
	if url == "" || key == "" {
		return nil
	}

	return &Agent{url: strings.TrimSuffix(url, "/"), key: key, client: client}
}

// Do sends one request and returns the answer's body when it is a 200. Its
// errors hold the control plane's own message and never the key.
func (a *Agent) Do(ctx context.Context, method, path string, body any) ([]byte, error) {
	return a.doWithin(ctx, method, path, body, maxAnswerBytes)
}

// doWithin is Do with an answer of up to limit bytes.
func (a *Agent) doWithin(ctx context.Context, method, path string, body any, limit int64) ([]byte, error) {
	var reader io.Reader

	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}

		reader = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, a.url+path, reader)
	if err != nil {
		return nil, fmt.Errorf("building the request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+a.key)
	req.Header.Set("Content-Type", "application/json")
	if path == agenttool.PathTools || path == agenttool.PathToolsCall {
		req.Header.Set(agenttool.HeaderWhatsAppMentions, "1")
	}

	resp, err := a.client.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}

		return nil, unreachable{fmt.Errorf("the control plane could not be reached: %w", err)}
	}
	defer resp.Body.Close() //nolint:errcheck // The answer was read or the read failed; closing adds nothing.

	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return nil, unreachable{fmt.Errorf("reading the control plane's answer: %w", err)}
	}

	if resp.StatusCode != http.StatusOK {
		var refused struct {
			Message string `json:"message"`
		}

		if json.Unmarshal(raw, &refused) == nil && refused.Message != "" {
			return nil, errors.New(refused.Message)
		}

		// A proxy in front of the control plane answers so, without the control plane's message, while it
		// restarts, as Nginx does when its upstream closes or refuses the connection (issue #1060): the answer did
		// not arrive.
		if resp.StatusCode == http.StatusBadGateway || resp.StatusCode == http.StatusServiceUnavailable ||
			resp.StatusCode == http.StatusGatewayTimeout {
			return nil, unreachable{fmt.Errorf("the control plane answered %d", resp.StatusCode)}
		}

		return nil, fmt.Errorf("the control plane answered %d", resp.StatusCode)
	}

	return raw, nil
}

// Tools is the tools the control plane lists on path (agenttool's
// PathTools, PathDeviceTools), in the shape of tools/list. A control plane
// that cannot give them gives none, with the error: the agent still has the
// others, and the server does not take a failed list for a changed one.
func (a *Agent) Tools(ctx context.Context, path string) ([]map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()

	raw, err := a.Do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}

	var list agenttool.BrowserToolList
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("reading the tools of %s: %w", path, err)
	}

	out := make([]map[string]any, 0, len(list.Data))
	for _, t := range list.Data {
		out = append(out, map[string]any{"name": t.Name, "description": t.Description, "inputSchema": t.InputSchema})
	}

	return out, nil
}

// ToolCall runs one tool on path (PathToolsCall, PathDeviceCall) within the
// time given and returns its result as the control plane gave it: text, or a
// screenshot as image content.
func (a *Agent) ToolCall(ctx context.Context, path string, within time.Duration, tool string,
	arguments json.RawMessage,
) (json.RawMessage, error) {
	return a.toolCall(ctx, path, within, maxAnswerBytes, tool, arguments)
}

func (a *Agent) toolCall(ctx context.Context, path string, within time.Duration, limit int64, tool string,
	arguments json.RawMessage,
) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, within)
	defer cancel()

	raw, err := a.doWithin(ctx, http.MethodPost, path, agenttool.BrowserCall{Tool: tool, Arguments: arguments}, limit)
	if err != nil {
		return nil, err
	}

	var result struct {
		Content json.RawMessage `json:"content"`
	}

	if json.Unmarshal(raw, &result) != nil || len(result.Content) == 0 {
		return nil, errors.New("the control plane's answer could not be read")
	}

	return json.RawMessage(raw), nil
}

// unreachable is a request whose answer did not arrive: the control plane could not be reached, or the connection
// ended before its answer did, as when it restarts.
type unreachable struct{ error }

func (u unreachable) Unwrap() error { return u.error }
