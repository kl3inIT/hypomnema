package websearch

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
)

// The providers of issue #393, each asked as its references ask it.

const (
	// firecrawlTimeout is search-rotation's for Firecrawl's search
	// (engines/firecrawl.ts, timeoutMs 45 000): it reads the pages it finds.
	firecrawlTimeout = 45 * time.Second
	// youTimeout is search-rotation's for a hosted MCP (engines/you.ts, 30 000 ms):
	// its call is three requests.
	youTimeout = 30 * time.Second
	// snippetCut is search-rotation's cut of a snippet (Tavily, Firecrawl,
	// You.com: 500); markdownCut its cut of Firecrawl's markdown, and
	// parallelCut of Parallel's excerpts.
	snippetCut  = 500
	markdownCut = 300
	parallelCut = 600
	// tinyFishAnswer is web-search-plus's bound on TinyFish's answer
	// (providers.d/tinyfish.py, _MAX_RESPONSE_BYTES).
	tinyFishAnswer = 2 << 20
	// mcpProtocol is the MCP version opencode's TinyFish initialize names.
	mcpProtocol = "2025-06-18"
)

// askTavily asks Tavily's search, with a key or without: with one as
// Authorization: Bearer, without one in its keyless mode, the header
// X-Tavily-Access-Mode: keyless and 3 chunks a source (opencode
// plugin/websearch/tavily.ts; search-rotation engines/tavily.ts). The
// search is the basic one, a credit a call.
func (s *Service) askTavily(ctx context.Context, r route, query string, limit int) (reply, error) {
	headers := map[string]string{"X-Client-Name": userAgent}
	body := map[string]any{"query": query, "max_results": limit, "search_depth": "basic"}

	if r.key.Value != "" {
		headers["Authorization"] = "Bearer " + r.key.Value
	} else {
		headers["X-Tavily-Access-Mode"] = "keyless"
		body["chunks_per_source"] = 3
	}

	var answer struct {
		Results []struct {
			Title         string `json:"title"`
			URL           string `json:"url"`
			Content       string `json:"content"`
			PublishedDate string `json:"published_date"`
		} `json:"results"`
	}

	if err := s.postJSON(ctx, s.cfg.TavilyURL+"/search", headers, body, "Tavily", &answer); err != nil {
		return reply{}, err
	}

	out := make([]Result, 0, len(answer.Results))

	for _, item := range answer.Results {
		if item.URL != "" {
			out = append(out, Result{
				Title: item.Title, URL: item.URL, Snippet: clip(item.Content, snippetCut), PublishedAt: item.PublishedDate,
			})
		}
	}

	return reply{results: out}, nil
}

// askFirecrawl asks Firecrawl's search, v2, with a key as Authorization:
// Bearer or without one, which Firecrawl allows a few calls a day from an
// address (search-rotation engines/firecrawl.ts; Hermes's keyless client
// sends no Authorization). An answer `success: false` is a failure; the
// results are data.web, or data itself.
func (s *Service) askFirecrawl(ctx context.Context, r route, query string, limit int) (reply, error) {
	headers := map[string]string{}
	if r.key.Value != "" {
		headers["Authorization"] = "Bearer " + r.key.Value
	}

	var answer struct {
		Success *bool           `json:"success"`
		Error   string          `json:"error"`
		Data    json.RawMessage `json:"data"`
	}

	body := map[string]any{"query": query, "limit": limit}
	if err := s.postJSON(ctx, s.cfg.FirecrawlURL+"/search", headers, body, "Firecrawl", &answer); err != nil {
		return reply{}, err
	}

	if answer.Success != nil && !*answer.Success {
		return reply{}, errors.New("its answer says it failed: " + clip(answer.Error, 200))
	}

	type item struct {
		Title       string `json:"title"`
		URL         string `json:"url"`
		Description string `json:"description"`
		Markdown    string `json:"markdown"`
	}

	var (
		web   struct{ Web []item }
		items []item
	)

	if json.Unmarshal(answer.Data, &web) == nil && web.Web != nil {
		items = web.Web
	} else if json.Unmarshal(answer.Data, &items) != nil {
		return reply{}, errors.New("its answer could not be read")
	}

	out := make([]Result, 0, len(items))

	for _, it := range items {
		if it.URL == "" {
			continue
		}

		snippet := clip(it.Description, snippetCut)
		if snippet == "" {
			snippet = clip(it.Markdown, markdownCut)
		}

		out = append(out, Result{Title: it.Title, URL: it.URL, Snippet: snippet})
	}

	return reply{results: out}, nil
}

// askParallelKey asks Parallel's search API with a key, in search-rotation's
// shape (engines/parallel.ts): x-api-key, the query as the objective and the
// one search query, the fast mode, the count, 400 characters a result.
func (s *Service) askParallelKey(ctx context.Context, r route, query string, limit int) (reply, error) {
	var answer parallelAnswer

	err := s.postJSON(ctx, s.cfg.ParallelURL, map[string]string{"X-Api-Key": r.key.Value}, map[string]any{
		"objective": query, "search_queries": []string{query}, "mode": "fast", "max_results": limit,
		"max_chars_per_result": 400,
	}, "Parallel", &answer)
	if err != nil {
		return reply{}, err
	}

	out := answer.read(" … ")
	for i := range out {
		out[i].Snippet = clip(out[i].Snippet, parallelCut)
	}

	return reply{results: out}, nil
}

// askTinyFish asks TinyFish's search API with a key, as web-search-plus
// does (providers.d/tinyfish.py): GET with the query and the web domain
// type, X-API-Key, its results' url, title, snippet and date, the count
// kept here.
func (s *Service) askTinyFish(ctx context.Context, r route, query string, limit int) (reply, error) {
	values := url.Values{"query": {query}, "domain_type": {"web"}}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.cfg.TinyFishURL+"?"+values.Encode(), nil)
	if err != nil {
		return reply{}, err
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("X-Api-Key", r.key.Value)

	raw, err := s.send(req, tinyFishAnswer)
	if err != nil {
		return reply{}, err
	}

	var answer struct {
		Results []struct {
			URL     string `json:"url"`
			Title   string `json:"title"`
			Snippet string `json:"snippet"`
			Date    string `json:"date"`
		} `json:"results"`
	}

	if err := json.Unmarshal(raw, &answer); err != nil || answer.Results == nil {
		return reply{}, errors.New("its answer could not be read")
	}

	out := make([]Result, 0, len(answer.Results))

	for _, item := range answer.Results {
		if item.URL != "" && len(out) < limit {
			out = append(out, Result{Title: item.Title, URL: item.URL, Snippet: item.Snippet, PublishedAt: item.Date})
		}
	}

	return reply{results: out}, nil
}

// askYou asks You.com's hosted MCP in its free profile, with no key, its
// you-search tool with no extraction (search-rotation engines/you.ts). Its
// client opens an MCP session first, as opencode opens TinyFish's
// (plugin/websearch/tinyfish.ts, initialize). The results are the web ones,
// then the news ones whose page is not among them, the count kept here.
func (s *Service) askYou(ctx context.Context, _ route, query string, limit int) (reply, error) {
	session, err := s.openMCP(ctx, s.cfg.YouMCPURL)
	if err != nil {
		return reply{}, err
	}

	headers := map[string]string{}
	if session != "" {
		headers["Mcp-Session-Id"] = session
	}

	result, err := s.callMCP(ctx, s.cfg.YouMCPURL, "you-search",
		map[string]any{"query": query, "extraction": "none", "count": limit}, headers)
	if err != nil {
		return reply{}, err
	}

	payload := result.StructuredContent
	if len(payload) == 0 || string(payload) == "null" {
		var text []string

		for _, c := range result.Content {
			if c.Type == "text" {
				text = append(text, c.Text)
			}
		}

		payload = json.RawMessage(strings.TrimSpace(strings.Join(text, "\n")))
	}

	type item struct {
		URL         string   `json:"url"`
		Title       string   `json:"title"`
		Description string   `json:"description"`
		Snippets    []string `json:"snippets"`
		PageAge     string   `json:"page_age"`
		Contents    struct {
			Highlights []string `json:"highlights"`
		} `json:"contents"`
	}

	var answer struct {
		Results *struct {
			Web  []item `json:"web"`
			News []item `json:"news"`
		} `json:"results"`
	}

	if json.Unmarshal(payload, &answer) != nil || answer.Results == nil {
		return reply{}, errors.New("its answer could not be read")
	}

	seen := map[string]bool{}

	var out []Result

	for _, it := range append(answer.Results.Web, answer.Results.News...) {
		address := strings.TrimSpace(it.URL)
		if address == "" || seen[address] || len(out) >= limit {
			continue
		}

		seen[address] = true

		snippet := it.Description
		for _, more := range [][]string{it.Contents.Highlights, it.Snippets} {
			if strings.TrimSpace(snippet) == "" && len(more) > 0 {
				snippet = more[0]
			}
		}

		out = append(out, Result{
			Title: strings.TrimSpace(it.Title), URL: address,
			Snippet: clip(strings.Join(strings.Fields(snippet), " "), snippetCut), PublishedAt: it.PageAge,
		})
	}

	return reply{results: out}, nil
}

// openMCP opens a session with an MCP server, as an MCP client connects
// (opencode's initialize for TinyFish): initialize, the session the answer
// names in Mcp-Session-Id, then notifications/initialized. A server that
// names no session is asked without one.
func (s *Service) openMCP(ctx context.Context, endpoint string) (string, error) {
	post := func(message map[string]any, session string) (*http.Response, error) {
		body, err := json.Marshal(message)
		if err != nil {
			return nil, err
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}

		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("User-Agent", userAgent)

		if session != "" {
			req.Header.Set("Mcp-Session-Id", session)
		}

		resp, err := s.client.Do(req)
		if err != nil {
			var urlErr *url.Error
			if errors.As(err, &urlErr) {
				err = urlErr.Err
			}

			return nil, fmt.Errorf("no answer: %w", err)
		}

		return resp, nil
	}

	resp, err := post(map[string]any{
		"jsonrpc": "2.0", "id": 0, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": mcpProtocol, "capabilities": map[string]any{},
			"clientInfo": map[string]any{"name": userAgent, "version": "1.0"},
		},
	}, "")
	if err != nil {
		return "", err
	}

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxMCPAnswer))
	_ = resp.Body.Close()

	if readErr != nil {
		return "", fmt.Errorf("reading the answer: %w", readErr)
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", refusal(resp, body, s.now())
	}

	session := resp.Header.Get("Mcp-Session-Id")

	notified, err := post(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}, session)
	if err != nil {
		return "", err
	}

	_, _ = io.Copy(io.Discard, io.LimitReader(notified.Body, maxMCPAnswer))
	_ = notified.Body.Close()

	return session, nil
}
