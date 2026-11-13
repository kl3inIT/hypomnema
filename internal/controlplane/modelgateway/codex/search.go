package codex

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// A web search answered by a Codex account (issue #354), as OmniRoute asks
// the Codex backend for one (open-sse/handlers/search/codexSearch.ts): one
// Responses request that must use the hosted web_search tool and asks for
// the sources it read, read from the stream's final items, not its deltas.
// Only a URL the search saw is kept.

const (
	// SearchModel is the model a search runs on: OmniRoute's default for
	// codex-search, and omp's web-search-model for Codex (packages/catalog
	// compat/rules/classes/openai.kdl), the small one.
	SearchModel = "gpt-6-luna"
	// searchEffort is OmniRoute's default reasoning effort for it.
	searchEffort = "low"
	// maxSearchResults is OmniRoute's codex-search maxMaxResults.
	maxSearchResults = 10
)

// SearchResult is one page a search found.
type SearchResult struct {
	Title, URL, Snippet string
}

// SearchBody is the request for a search of query, for at most limit results.
func SearchBody(query string, limit int) []byte {
	n := min(maxSearchResults, max(1, limit))
	instructions := fmt.Sprintf(`Search the web and return only JSON: `+
		`{"results":[{"title":"...","url":"...","description":"..."}]}. Return up to %d relevant results.`, n) +
		"\nUse the web_search tool. Only return URLs actually observed in search sources or citations. " +
		"Do not invent URLs."

	return mustJSON(item{
		"model":        SearchModel,
		"instructions": instructions,
		"input": []any{item{
			"role": "user", "content": []any{item{"type": "input_text", "text": query}},
		}},
		"tools":       []any{item{"type": "web_search"}},
		"tool_choice": "required",
		"include":     []any{"web_search_call.action.sources"},
		"reasoning":   item{"effort": searchEffort},
		"stream":      true,
		"store":       false,
	})
}

// searchState is what a search's stream said, as OmniRoute's
// CodexSearchParseState keeps it.
type searchState struct {
	sources, annotations []SearchResult
	messages, texts      []string
	observed             map[string]bool
	calls                int
	completed            bool
}

// ParseSearch reads a search's event stream into at most limit results: the
// model's JSON rows whose URL the search saw, else the citations of its
// answer, else the sources it read (OmniRoute's selectCodexSearchRows). A
// stream that failed, did not complete or did not search is a failure.
func ParseSearch(stream []byte, limit int) ([]SearchResult, *Failure) {
	st := &searchState{observed: map[string]bool{}}

	for block := range strings.SplitSeq(strings.ReplaceAll(string(stream), "\r\n", "\n"), "\n\n") {
		kind, payload, ok := searchEvent(block)
		if !ok {
			continue
		}

		if payload == nil {
			return nil, searchFailed()
		}

		if f := st.event(kind, payload); f != nil {
			return nil, f
		}
	}

	if !st.completed || st.calls == 0 {
		return nil, searchFailed()
	}

	return st.rows(limit), nil
}

func searchFailed() *Failure {
	return &Failure{Status: http.StatusBadGateway, Type: "server_error", Code: "upstream_failed",
		Message: "Codex web search failed"}
}

// searchEvent is one event of the stream: its type (the payload's, else the
// event's name) and its payload; a payload that is not JSON is nil.
func searchEvent(block string) (string, item, bool) {
	var name string

	var data []string

	for line := range strings.SplitSeq(block, "\n") {
		if v, ok := strings.CutPrefix(line, "event:"); ok {
			name = strings.TrimSpace(v)
		}

		if v, ok := strings.CutPrefix(line, "data:"); ok {
			data = append(data, strings.TrimLeft(v, " "))
		}
	}

	joined := strings.Join(data, "\n")
	if joined == "" || joined == "[DONE]" {
		return "", nil, false
	}

	decoder := json.NewDecoder(strings.NewReader(joined))
	decoder.UseNumber()

	var payload item
	if decoder.Decode(&payload) != nil || payload == nil {
		return "", nil, true
	}

	if t, _ := payload["type"].(string); t != "" {
		name = t
	}

	return name, payload, true
}

func (st *searchState) event(kind string, payload item) *Failure {
	switch kind {
	case "error":
		return failureOf(firstRaw(rawOf(payload["error"]), mustJSON(payload)))
	case "response.failed":
		response, _ := payload["response"].(item)

		return failureOf(rawOf(response["error"]))
	case "response.output_item.done":
		it, _ := payload["item"].(item)

		switch it["type"] {
		case "web_search_call":
			st.calls++

			action, _ := it["action"].(item)
			for _, source := range arrayOf(action["sources"]) {
				s, _ := source.(item)
				st.observe(s["url"], nil, &st.sources)
			}

			if action["type"] == "open_page" {
				st.observe(action["url"], nil, &st.sources)
			}
		case "message":
			for _, c := range arrayOf(it["content"]) {
				part, _ := c.(item)
				if text, ok := part["text"].(string); ok {
					st.messages = append(st.messages, text)
				}

				for _, a := range arrayOf(part["annotations"]) {
					if citation, _ := a.(item); citation["type"] == "url_citation" {
						st.observe(citation["url"], citation["title"], &st.annotations)
					}
				}
			}
		}
	case "response.output_text.done":
		if text, ok := payload["text"].(string); ok {
			st.texts = append(st.texts, text)
		}
	case "response.completed":
		response, _ := payload["response"].(item)
		if status, _ := response["status"].(string); status != "" && status != "completed" {
			return searchFailed()
		}

		st.completed = true
	}

	return nil
}

// observe notes a URL the search saw, and adds it to rows.
func (st *searchState) observe(raw, title any, rows *[]SearchResult) {
	u, ok := cleanURL(raw)
	if !ok {
		return
	}

	st.observed[groundingKey(u)] = true

	name, _ := title.(string)
	if strings.TrimSpace(name) == "" {
		name = u.Hostname()
	}

	*rows = append(*rows, SearchResult{Title: strings.TrimSpace(name), URL: u.String()})
}

// rows are the results, each page once.
func (st *searchState) rows(limit int) []SearchResult {
	texts := st.texts
	if len(texts) == 0 {
		texts = st.messages
	}

	var grounded []SearchResult

	for _, text := range texts {
		for _, row := range modelRows(text) {
			if st.observed[groundingKeyOf(row.URL)] {
				grounded = append(grounded, row)
			}
		}
	}

	rows := grounded

	switch {
	case len(rows) > 0:
	case len(st.annotations) > 0:
		rows = st.annotations
	default:
		rows = st.sources
	}

	n := min(maxSearchResults, max(1, limit))
	seen := map[string]bool{}
	out := make([]SearchResult, 0, n)

	for _, row := range rows {
		key := groundingKeyOf(row.URL)
		if seen[key] {
			continue
		}

		seen[key] = true
		out = append(out, row)

		if len(out) == n {
			break
		}
	}

	return out
}

// modelRows reads the JSON the model was asked to answer, in a code fence or not.
func modelRows(text string) []SearchResult {
	text = strings.TrimSpace(text)
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(strings.TrimSpace(text), "```")

	var answer struct {
		Results []struct {
			Title       string `json:"title"`
			URL         string `json:"url"`
			Description string `json:"description"`
			Snippet     string `json:"snippet"`
		} `json:"results"`
	}

	if json.Unmarshal([]byte(strings.TrimSpace(text)), &answer) != nil {
		return nil
	}

	var out []SearchResult

	for _, r := range answer.Results {
		u, ok := cleanURL(r.URL)
		if !ok {
			continue
		}

		title := strings.TrimSpace(r.Title)
		if title == "" {
			title = u.Hostname()
		}

		snippet := strings.TrimSpace(r.Description)
		if snippet == "" {
			snippet = strings.TrimSpace(r.Snippet)
		}

		out = append(out, SearchResult{Title: title, URL: u.String(), Snippet: snippet})
	}

	return out
}

// cleanURL keeps an http(s) URL without a user or password, its utm_source
// taken out (OmniRoute's cleanUrl).
func cleanURL(raw any) (*url.URL, bool) {
	text, ok := raw.(string)
	if !ok {
		return nil, false
	}

	u, err := url.Parse(text)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return nil, false
	}

	if q := u.Query(); q.Has("utm_source") {
		q.Del("utm_source")
		u.RawQuery = q.Encode()
	}

	return u, true
}

// groundingKey is a page's host without www. and its path without a trailing
// slash, so that two spellings of a page are one (OmniRoute's groundingKey).
func groundingKey(u *url.URL) string {
	return strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.") + strings.TrimRight(u.Path, "/")
}

func groundingKeyOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}

	return groundingKey(u)
}

func arrayOf(v any) []any {
	a, _ := v.([]any)

	return a
}

func rawOf(v any) json.RawMessage {
	if v == nil {
		return nil
	}

	return bytes.TrimSpace(mustJSON(v))
}
