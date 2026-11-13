package antigravity

import (
	"encoding/json"
	"fmt"
	"strings"
)

// A web search answered by an Antigravity account (issue #354): Google Search
// grounding, one generateContent request with the googleSearch tool in the
// envelope the IDE sends for a search, the pages read from the answer's
// grounding chunks, each with the parts of the answer it grounds as its
// snippet. It is 9router's (open-sse/handlers/search/chatSearch.js, the
// antigravity entry) and 9router-go's
// (internal/handlers/media/antigravity_search.go), which agree.

const (
	// SearchModel is the model a search runs on: 9router's searchViaChat
	// default (providers/registry/antigravity.js), the small one.
	SearchModel = "gemini-2.5-flash"
	// searchMaxOutputTokens and the temperature are 9router's
	// AG_SEARCH_GENERATION_CONFIG.
	searchMaxOutputTokens = 8192
)

// SearchResult is one page a search found.
type SearchResult struct {
	Title, URL, Snippet string
}

// SearchBody is the request for a search of query in the account's project.
func SearchBody(query, project string) []byte {
	body, _ := json.Marshal(map[string]any{ //nolint:errchkjson // Strings and numbers always encode.
		"project":     project,
		"model":       SearchModel,
		"userAgent":   "antigravity",
		"requestType": "search",
		"request": map[string]any{
			"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": query}}}},
			"tools":    []any{map[string]any{"googleSearch": map[string]any{}}},
			"generationConfig": map[string]any{
				"temperature": 1.0, "maxOutputTokens": searchMaxOutputTokens,
			},
		},
	})

	return body
}

type groundedAnswer struct {
	Candidates []struct {
		GroundingMetadata *struct {
			GroundingChunks []struct {
				Web *struct {
					URI   string `json:"uri"`
					URL   string `json:"url"`
					Title string `json:"title"`
				} `json:"web"`
			} `json:"groundingChunks"`
			GroundingSupports []struct {
				Segment *struct {
					Text string `json:"text"`
				} `json:"segment"`
				GroundingChunkIndices []int `json:"groundingChunkIndices"`
			} `json:"groundingSupports"`
		} `json:"groundingMetadata"`
	} `json:"candidates"`
}

// ParseSearch reads a search's answer into at most limit results: one per
// page of the first candidate's grounding chunks, in their order, its
// snippet the answer's grounded segments joined by " | ", else its title.
// The backend wraps the Gemini answer in {"response": …}, or does not.
func ParseSearch(raw []byte, limit int) ([]SearchResult, error) {
	var envelope struct {
		Response *groundedAnswer `json:"response"`
		groundedAnswer
	}

	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("reading Antigravity's search: %w", err)
	}

	answer := envelope.groundedAnswer
	if envelope.Response != nil && len(envelope.Response.Candidates) > 0 {
		answer = *envelope.Response
	}

	if len(answer.Candidates) == 0 || answer.Candidates[0].GroundingMetadata == nil {
		return nil, nil
	}

	grounding := answer.Candidates[0].GroundingMetadata

	type source struct {
		title    string
		snippets []string
	}

	var order []string

	sources := map[string]*source{}
	byIndex := make([]*source, len(grounding.GroundingChunks))

	for i, chunk := range grounding.GroundingChunks {
		if chunk.Web == nil {
			continue
		}

		u := chunk.Web.URI
		if u == "" {
			u = chunk.Web.URL
		}

		if u == "" {
			continue
		}

		if sources[u] == nil {
			sources[u] = &source{title: chunk.Web.Title}
			order = append(order, u)
		}

		byIndex[i] = sources[u]
	}

	for _, support := range grounding.GroundingSupports {
		if support.Segment == nil || strings.TrimSpace(support.Segment.Text) == "" {
			continue
		}

		for _, i := range support.GroundingChunkIndices {
			if i >= 0 && i < len(byIndex) && byIndex[i] != nil {
				byIndex[i].snippets = append(byIndex[i].snippets, strings.TrimSpace(support.Segment.Text))
			}
		}
	}

	if limit > 0 && len(order) > limit {
		order = order[:limit]
	}

	out := make([]SearchResult, 0, len(order))

	for _, u := range order {
		s := sources[u]

		snippet := joinDistinct(s.snippets, " | ")
		if snippet == "" {
			snippet = s.title
		}

		out = append(out, SearchResult{Title: s.title, URL: u, Snippet: snippet})
	}

	return out, nil
}

// joinDistinct joins the pieces, each once (9router-go's joinDeduped).
func joinDistinct(pieces []string, sep string) string {
	seen := map[string]bool{}

	var kept []string

	for _, p := range pieces {
		if p != "" && !seen[p] {
			seen[p] = true
			kept = append(kept, p)
		}
	}

	return strings.Join(kept, sep)
}
