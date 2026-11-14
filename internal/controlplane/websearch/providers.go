package websearch

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Each provider is asked as its references ask it, and its answer read into
// Results; nothing else of a provider's answer is kept.

const (
	// maxProviderAnswer bounds a provider's answer. The MCP endpoints answer
	// within opencode's 256 KiB (WebSearchMcp.MAX_RESPONSE_BYTES); Exa's REST
	// answer carries each page's text, as OmniRoute asks it to.
	maxMCPAnswer      = 256 << 10
	maxProviderAnswer = 4 << 20
	// userAgent names this program to the keyless providers, as omp and
	// opencode send their own.
	userAgent = "hypomnema"
	// exaRESTSnippet is OmniRoute's cut of a result's text when Exa gives no
	// highlight; mcpSnippet is omp's MAX_EXA_SNIPPET_CHARS.
	exaRESTSnippet = 300
	mcpSnippet     = 500
)

// send asks a provider and returns the body of a success, or its refusal.
func (s *Service) send(req *http.Request, limit int64) ([]byte, error) {
	resp, err := s.client.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}

		return nil, fmt.Errorf("no answer: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // The body was read or the read failed; closing adds nothing.

	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("reading the answer: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, refusal(resp, body, s.now())
	}

	if int64(len(body)) > limit {
		return nil, fmt.Errorf("the answer is larger than %d bytes", limit)
	}

	return body, nil
}

// askExa asks Exa's REST API with a key, in OmniRoute's request shape
// (open-sse/handlers/search.ts, buildExaRequest; owner 2026-11-14: auto,
// text and highlights) and read as its normalizeExaResponse reads it. What
// the answer cost, its costDollars.total, is kept for the month's spend on
// the Web search page (issue #393, owner 2026-11-14): Exa has no balance to
// read.
func (s *Service) askExa(ctx context.Context, r route, query string, limit int) (reply, error) {
	var answer struct {
		Results []struct {
			Title         string   `json:"title"`
			URL           string   `json:"url"`
			PublishedDate string   `json:"publishedDate"`
			Highlights    []string `json:"highlights"`
			Text          string   `json:"text"`
		} `json:"results"`
		CostDollars struct {
			Total float64 `json:"total"`
		} `json:"costDollars"`
	}

	err := s.postJSON(ctx, s.cfg.ExaURL, map[string]string{"X-Api-Key": r.key.Value}, map[string]any{
		"type": "auto", "query": query, "numResults": limit,
		"contents": map[string]any{"text": true, "highlights": true},
	}, "Exa", &answer)
	if err != nil {
		return reply{}, err
	}

	out := make([]Result, 0, len(answer.Results))

	for _, item := range answer.Results {
		if item.URL == "" {
			continue
		}

		snippet := clip(item.Text, exaRESTSnippet)
		if len(item.Highlights) > 0 && item.Highlights[0] != "" {
			snippet = item.Highlights[0]
		}

		out = append(out, Result{
			Title: item.Title, URL: item.URL, Snippet: snippet, PublishedAt: item.PublishedDate,
		})
	}

	return reply{results: out, costDollars: max(answer.CostDollars.Total, 0)}, nil
}

// postJSON posts a JSON body with headers and reads a JSON success into into.
func (s *Service) postJSON(ctx context.Context, endpoint string, headers map[string]string, body any, who string,
	into any,
) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	raw, err := s.send(req, maxProviderAnswer)
	if err != nil {
		return err
	}

	if err := json.Unmarshal(raw, into); err != nil {
		return fmt.Errorf("reading %s's answer: %w", who, err)
	}

	return nil
}

// askExaMCP asks Exa's public MCP without a key, its web_search_exa tool,
// as opencode does (plugin/websearch/exa.ts).
func (s *Service) askExaMCP(ctx context.Context, _ route, query string, limit int) (reply, error) {
	result, err := s.callMCP(ctx, s.cfg.ExaMCPURL, "web_search_exa",
		map[string]any{"query": query, "numResults": limit}, nil)
	if err != nil {
		return reply{}, err
	}

	for _, c := range result.Content {
		if c.Text != "" {
			return reply{results: parseExaText(c.Text)}, nil
		}
	}

	return reply{}, nil
}

// parseExaText reads the text Exa's MCP answers: one block a result, of
// `Title:`, `URL:`, `Published:` (or `Published Date:`) and its text after
// `Highlights:` or `Text:`. Blocks are parted by a line `---` (opencode's
// parseResults) or start at a `Title:` line (omp's parseExaMcpTextPayload).
func parseExaText(text string) []Result {
	var (
		blocks  [][]string
		current []string
	)

	sc := bufio.NewScanner(strings.NewReader(strings.ReplaceAll(text, "\r\n", "\n")))
	sc.Buffer(make([]byte, 64<<10), maxMCPAnswer)

	for sc.Scan() {
		line := sc.Text()

		if strings.TrimSpace(line) == "---" || (strings.HasPrefix(line, "Title:") && len(current) > 0) {
			blocks = append(blocks, current)
			current = nil

			if strings.TrimSpace(line) == "---" {
				continue
			}
		}

		current = append(current, line)
	}

	blocks = append(blocks, current)

	var out []Result

	for _, block := range blocks {
		var (
			r    Result
			body []string
			in   bool
		)

		for _, line := range block {
			if in {
				body = append(body, line)

				continue
			}

			field, value, _ := strings.Cut(line, ":")
			value = strings.TrimSpace(value)

			switch field {
			case "Title":
				r.Title = value
			case "URL":
				r.URL = value
			case "Published", "Published Date":
				if value != "N/A" {
					r.PublishedAt = value
				}
			case "Highlights", "Text":
				in = true

				if value != "" {
					body = append(body, value)
				}
			}
		}

		if r.URL == "" {
			continue
		}

		if r.Title == "N/A" {
			r.Title = ""
		}

		r.Snippet = clip(strings.TrimSpace(strings.Join(body, "\n")), mcpSnippet)
		out = append(out, r)
	}

	return out
}

// askParallel asks Parallel's public MCP without a key, its web_search tool
// with the query as the objective and the one search query, as opencode
// (plugin/websearch/parallel.ts) and omp (providers/parallel.ts,
// searchWithPublicMcp) do. The results are its structured content, or the
// JSON of its text when there is none (omp).
func (s *Service) askParallel(ctx context.Context, _ route, query string, _ int) (reply, error) {
	result, err := s.callMCP(ctx, s.cfg.ParallelMCPURL, "web_search",
		map[string]any{"objective": query, "search_queries": []string{query}}, nil)
	if err != nil {
		return reply{}, err
	}

	payload := result.StructuredContent
	if len(payload) == 0 || string(payload) == "null" {
		for _, c := range result.Content {
			if json.Valid([]byte(c.Text)) {
				payload = json.RawMessage(c.Text)

				break
			}
		}
	}

	var answer parallelAnswer
	if len(payload) == 0 || json.Unmarshal(payload, &answer) != nil {
		return reply{}, errors.New("its answer could not be read")
	}

	return reply{results: answer.read("\n\n")}, nil
}

// parallelAnswer is a Parallel search's answer, from its MCP and its API alike.
type parallelAnswer struct {
	Results []struct {
		URL         string   `json:"url"`
		Title       string   `json:"title"`
		PublishDate string   `json:"publish_date"`
		Excerpts    []string `json:"excerpts"`
	} `json:"results"`
}

func (a parallelAnswer) read(join string) []Result {
	out := make([]Result, 0, len(a.Results))

	for _, item := range a.Results {
		if item.URL == "" {
			continue
		}

		out = append(out, Result{
			Title: item.Title, URL: item.URL, Snippet: strings.Join(item.Excerpts, join), PublishedAt: item.PublishDate,
		})
	}

	return out
}

// mcpResult is a tools/call result.
type mcpResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StructuredContent json.RawMessage `json:"structuredContent"`
	IsError           bool            `json:"isError"`
}

// callMCP calls one tool of an MCP server over HTTP with one JSON-RPC
// request and no session, as opencode's WebSearchMcp.call does: the answer
// is JSON, or Server-Sent Events whose `data:` lines hold it.
func (s *Service) callMCP(ctx context.Context, endpoint, tool string, arguments map[string]any,
	headers map[string]string,
) (mcpResult, error) {
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": arguments},
	})
	if err != nil {
		return mcpResult{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return mcpResult{}, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("User-Agent", userAgent)

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	raw, err := s.send(req, maxMCPAnswer)
	if err != nil {
		return mcpResult{}, err
	}

	var message struct {
		Result *mcpResult `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}

	if !decodeMCP(raw, &message) {
		return mcpResult{}, errors.New("its answer could not be read")
	}

	switch {
	case message.Error != nil:
		return mcpResult{}, fmt.Errorf("MCP error %s: %s", strconv.Itoa(message.Error.Code), message.Error.Message)
	case message.Result == nil:
		return mcpResult{}, errors.New("its answer has no result")
	case message.Result.IsError:
		for _, c := range message.Result.Content {
			if c.Text != "" {
				return mcpResult{}, errors.New(clip(strings.TrimSpace(c.Text), 200))
			}
		}

		return mcpResult{}, errors.New("its tool failed")
	}

	return *message.Result, nil
}

// decodeMCP reads the JSON-RPC answer from a JSON body or from the first
// `data:` line of an event stream that holds one: a message with a result
// or an error, not a notification.
func decodeMCP(raw []byte, into any) bool {
	answer := func(text string) bool {
		var probe struct {
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}

		text = strings.TrimSpace(text)

		return strings.HasPrefix(text, "{") && json.Unmarshal([]byte(text), &probe) == nil &&
			(len(probe.Result) > 0 || len(probe.Error) > 0) && json.Unmarshal([]byte(text), into) == nil
	}

	if answer(string(raw)) {
		return true
	}

	for line := range strings.SplitSeq(string(raw), "\n") {
		if data, ok := strings.CutPrefix(strings.TrimRight(line, "\r"), "data: "); ok && answer(data) {
			return true
		}
	}

	return false
}
