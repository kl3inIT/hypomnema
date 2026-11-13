package claude

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// A web search answered by a Claude account (issue #354), as omp asks one
// (packages/coding-agent/src/web/search/providers/anthropic.ts): one Messages
// request, not streamed, with Anthropic's web_search_20250305 server tool and
// the query as the user's message; the pages are the tool's
// web_search_tool_result blocks. Under a subscription token the request wears
// the disguise every subscription request wears (cloak.go), as omp's does
// under OAuth.

const (
	// SearchModel is the model a search runs on: omp's web-search-model for
	// Anthropic (packages/catalog compat/rules/classes/anthropic.kdl), the
	// small one.
	SearchModel = "claude-haiku-4-5"
	// SearchBeta is the beta omp adds for the search tool
	// (buildAnthropicSearchHeaders).
	SearchBeta = "web-search-2025-03-05"
	// searchMaxTokens is omp's DEFAULT_MAX_TOKENS.
	searchMaxTokens = 4096
	// searchInstruction is the instruction omp adds under OAuth (buildSystemBlocks).
	searchInstruction = "You are a helpful AI assistant with web search capabilities."
)

// SearchResult is one page a search found.
type SearchResult struct {
	Title, URL, Snippet string
	// PublishedAt is the page's age as Anthropic gives it ("2 days ago"); empty when none.
	PublishedAt string
}

// SearchBody is the request for a search of query under a subscription token;
// session names the metadata session, a new one when empty.
func SearchBody(query, token, session string) ([]byte, error) {
	body := item{
		"model":      SearchModel,
		"max_tokens": searchMaxTokens,
		"messages":   []any{item{"role": "user", "content": query}},
		"tools":      []any{item{"type": "web_search_20250305", "name": "web_search"}},
		"system":     searchInstruction,
	}

	cloak(body, token, session)

	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encoding the search request: %w", err)
	}

	return encoded, nil
}

// ParseSearch reads a search's answer into at most limit results: the pages
// of its web_search_tool_result blocks, in order (omp's parseResponse), each
// with the first passage the answer cited from it as its snippet.
func ParseSearch(raw []byte, limit int) ([]SearchResult, error) {
	var answer struct {
		Content []struct {
			Type      string          `json:"type"`
			Content   json.RawMessage `json:"content"`
			Citations []struct {
				URL       string `json:"url"`
				CitedText string `json:"cited_text"`
			} `json:"citations"`
		} `json:"content"`
	}

	if err := json.Unmarshal(raw, &answer); err != nil {
		return nil, fmt.Errorf("reading Claude's search: %w", err)
	}

	var (
		out    []SearchResult
		failed string
	)

	cited := map[string]string{}

	for _, block := range answer.Content {
		switch block.Type {
		case "web_search_tool_result":
			var pages []struct {
				Type    string `json:"type"`
				Title   string `json:"title"`
				URL     string `json:"url"`
				PageAge string `json:"page_age"`
			}

			if json.Unmarshal(block.Content, &pages) != nil {
				// The tool failed: its content is an error, not pages.
				var e struct {
					ErrorCode string `json:"error_code"`
				}

				_ = json.Unmarshal(block.Content, &e)
				failed = e.ErrorCode

				continue
			}

			for _, p := range pages {
				if p.Type == "web_search_result" && p.URL != "" {
					out = append(out, SearchResult{Title: p.Title, URL: p.URL, PublishedAt: p.PageAge})
				}
			}
		case "text":
			for _, c := range block.Citations {
				if _, ok := cited[c.URL]; !ok && c.URL != "" {
					cited[c.URL] = strings.TrimSpace(c.CitedText)
				}
			}
		}
	}

	if len(out) == 0 && failed != "" {
		return nil, errors.New("Claude's web search failed: " + failed)
	}

	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}

	for i := range out {
		out[i].Snippet = cited[out[i].URL]
	}

	return out, nil
}
