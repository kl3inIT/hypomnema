package workertools

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

	"github.com/kl3inIT/hypomnema/internal/node/bridge"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// The shared browser (issue #212) is the control plane's, not the node's: the
// server calls the control plane's agent API itself with the Dispatch's key,
// as OpenMausBot's stdio proxy calls its server with a turn's token
// (server/host/browser.ts) and as this repository's tool launcher does
// (agenttool.Launcher).
const (
	// listTimeout bounds asking for the tools. The first worker to ask may
	// wait for the control plane to fetch its browser.
	listTimeout = 2 * time.Minute
	// callTimeout is longer than the control plane gives a tool (3 minutes),
	// so that its answer is the one the worker reads.
	callTimeout = 4 * time.Minute
	// maxAnswerBytes bounds an answer; a screenshot is the largest.
	maxAnswerBytes = 16 << 20
)

// agentAPI calls the control plane's agent API for a worker.
type agentAPI struct {
	url    string
	key    string
	client *http.Client
}

// agentOf is the agent API of the Dispatch whose bridge directory dir is, or
// nil when the runner gave none: the worker then has no browser.
func agentOf(dir string) *agentAPI {
	agent, ok, err := bridge.AgentOf(dir)
	if err != nil || !ok || agent.URL == "" || agent.Key == "" {
		return nil
	}

	return &agentAPI{url: strings.TrimSuffix(agent.URL, "/"), key: agent.Key, client: &http.Client{}}
}

// do sends one request and returns the answer's body when it is a 200. Its
// errors hold the control plane's own message and never the key.
func (a *agentAPI) do(ctx context.Context, method, path string, body any) ([]byte, error) {
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

	resp, err := a.client.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}

		return nil, fmt.Errorf("the control plane could not be reached: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // The answer was read or the read failed; closing adds nothing.

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswerBytes))
	if err != nil {
		return nil, fmt.Errorf("reading the control plane's answer: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var refused struct {
			Message string `json:"message"`
		}

		if json.Unmarshal(raw, &refused) == nil && refused.Message != "" {
			return nil, errors.New(refused.Message)
		}

		return nil, fmt.Errorf("the control plane answered %d", resp.StatusCode)
	}

	return raw, nil
}

// browserTools is the browser's tools as the control plane lists them, in
// the shape of tools/list. A control plane that cannot give them gives none:
// the worker still has its own tools.
func (a *agentAPI) browserTools(ctx context.Context) []map[string]any {
	ctx, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()

	raw, err := a.do(ctx, http.MethodGet, agenttool.PathBrowserTools, nil)
	if err != nil {
		return nil
	}

	var list agenttool.BrowserToolList
	if json.Unmarshal(raw, &list) != nil {
		return nil
	}

	out := make([]map[string]any, 0, len(list.Data))
	for _, t := range list.Data {
		out = append(out, map[string]any{"name": t.Name, "description": t.Description, "inputSchema": t.InputSchema})
	}

	return out
}

// browserCall runs one browser tool in the worker's own tab and returns the
// tool's result as the control plane gave it: text, or a screenshot as image
// content.
func (a *agentAPI) browserCall(ctx context.Context, tool string, arguments json.RawMessage) (any, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	raw, err := a.do(ctx, http.MethodPost, agenttool.PathBrowserCall,
		agenttool.BrowserCall{Tool: tool, Arguments: arguments})
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
