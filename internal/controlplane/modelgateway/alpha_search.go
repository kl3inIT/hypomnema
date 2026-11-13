package modelgateway

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"
)

// Codex's standalone search (issue #354). A Codex CLI whose provider is the
// gateway (internal/node/codex: base URL <control plane>/v1, and
// supports_standalone_web_search) has a `web.run` tool when its model takes
// the lite shape; the tool posts its commands to <base URL>/alpha/search
// (codex-rs/codex-api/src/endpoint/search.rs) and gives the model the
// answer's `output`. The gateway answers it from its one search chain, as
// OmniRoute does (src/app/api/v1/alpha/search/route.ts): only the
// search_query command, each query searched in turn, the others refused
// plainly; a page is read with the worker's own web_fetch.
//
// The answer is Codex's SearchResponse (codex-rs/codex-api/src/search.rs):
// encrypted_output null, output the text the model reads, and results, which
// Codex keeps opaque; OmniRoute's usage beside them. The output is what a
// worker's web_search answers, marked as outside content, so that what
// reaches an agent from the web is marked wherever it comes in.

// alphaSearchResults is how many results a query gets for each
// response_length (OmniRoute's RESPONSE_LENGTH_MAX_RESULTS); medium when none.
var alphaSearchResults = map[string]int{"short": 3, "medium": 5, "long": 10}

type alphaSearchQuery struct {
	Q string `json:"q"`
}

type alphaSearchRequest struct {
	// Commands are Codex's SearchCommands, read by name: search_query and
	// response_length are used, any other non-empty one is refused.
	Commands map[string]json.RawMessage `json:"commands"`
	// ResponseLength is where OmniRoute reads it; Codex sends it in commands.
	ResponseLength string `json:"response_length"`
}

type alphaSearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

type alphaSearchUsage struct {
	QueriesUsed   int     `json:"queries_used"`
	SearchCostUSD float64 `json:"search_cost_usd"`
}

type alphaSearchResponse struct {
	EncryptedOutput *string             `json:"encrypted_output"`
	Output          string              `json:"output"`
	Results         []alphaSearchResult `json:"results"`
	Usage           alphaSearchUsage    `json:"usage"`
}

// alphaSearch answers POST /v1/alpha/search.
func (s *Server) alphaSearch(w http.ResponseWriter, r *http.Request) {
	k, ok := s.authenticate(w, r)
	if !ok {
		return
	}

	if s.web == nil {
		writeError(w, http.StatusServiceUnavailable, typeServer, "web_unavailable", "this gateway has no web search")

		return
	}

	// The body carries the conversation's last turns beside the commands: a chat request's bound.
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, typeInvalidRequest, "invalid_request", "the body could not be read")

		return
	}

	queries, limit, problem := readAlphaSearch(raw)
	if problem != "" {
		writeError(w, http.StatusBadRequest, typeInvalidRequest, "invalid_request", problem)

		return
	}

	out := alphaSearchResponse{Results: []alphaSearchResult{}}
	texts := make([]string, 0, len(queries))

	for _, q := range queries {
		start := s.svc.clock.Now()
		answer, err := s.web.Search(r.Context(), q, limit, "")
		s.recordWeb(r, WebCall{
			KeyID: k.ID, Model: ModelWebSearch, Provider: answer.Provider, Attempts: len(answer.Errors) + 1, Err: err,
			Duration: s.svc.clock.Now().Sub(start),
		})

		if err != nil {
			s.webFailed(w, r, err)

			return
		}

		out.Usage.QueriesUsed++
		texts = append(texts, answer.Text())

		for _, res := range answer.Results {
			out.Results = append(out.Results, alphaSearchResult{Title: res.Title, URL: res.URL, Snippet: res.Snippet})
		}
	}

	out.Output = strings.Join(texts, "\n\n")
	writeJSON(w, http.StatusOK, out)
}

// readAlphaSearch reads Codex's request: its queries and how many results
// each gets, or what is wrong with it, in OmniRoute's words.
func readAlphaSearch(raw []byte) ([]string, int, string) {
	var in alphaSearchRequest
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, 0, "Invalid JSON body"
	}

	for _, name := range slices.Sorted(maps.Keys(in.Commands)) {
		if name == "search_query" || name == "response_length" {
			continue
		}

		var operations []json.RawMessage
		if json.Unmarshal(in.Commands[name], &operations) == nil && len(operations) > 0 {
			return nil, 0, "Unsupported operation for /v1/alpha/search: " + name
		}
	}

	length := in.ResponseLength
	if v, ok := in.Commands["response_length"]; ok {
		if err := json.Unmarshal(v, &length); err != nil {
			return nil, 0, "commands.response_length is short, medium or long"
		}
	}

	if length == "" {
		length = "medium"
	}

	limit, ok := alphaSearchResults[length]
	if !ok {
		return nil, 0, "commands.response_length is short, medium or long"
	}

	var asked []alphaSearchQuery
	if v, ok := in.Commands["search_query"]; ok && json.Unmarshal(v, &asked) != nil {
		return nil, 0, "commands.search_query is a list of {q, recency?, domains?}"
	}

	if len(asked) == 0 {
		return nil, 0, "commands.search_query must contain at least one query"
	}

	queries := make([]string, 0, len(asked))

	for i, q := range asked {
		if strings.TrimSpace(q.Q) == "" {
			return nil, 0, fmt.Sprintf("commands.search_query[%d].q is required", i)
		}

		queries = append(queries, q.Q)
	}

	return queries, limit, ""
}
