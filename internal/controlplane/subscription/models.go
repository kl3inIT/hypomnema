package subscription

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// The model lists of Codex and Claude (issue #623). Antigravity's is fetchAvailableModels (antigravity.go).

// codexClientVersion is the Codex CLI version a models request names, the one the gateway sends as the CLI
// (modelgateway/codex_adapter.go, codexCLIVersion): the backend answers the models that version knows. It is
// Codex's latest release, rust-v0.169.1 (2026-11-17), whose catalog has gpt-6.2-sol and gpt-6.2-astra (issue #795).
const codexClientVersion = "0.169.1"

// maxModelsAnswer bounds a vendor's model list.
const maxModelsAnswer = 4 << 20

// Models implements ModelLister with the Codex backend's own catalog, as the Codex CLI reads it
// (codex-rs/codex-api/src/endpoint/models.rs: GET {base}/models?client_version=; models-manager/src/manager.rs) and
// OpenClaw (extensions/openai/openai-provider-catalog.ts) and omp (packages/catalog/src/discovery/codex.ts) do: a
// bearer token and the ChatGPT account. A model is kept when Codex lists it in its picker (visibility "list") and
// serves it to the API, as the CLI's picker shows them.
func (c *Codex) Models(ctx context.Context, token Token) ([]ServedModel, error) {
	header := http.Header{}
	header.Set("Authorization", "Bearer "+token.AccessToken)
	header.Set("Originator", "codex_cli_rs")
	header.Set("Version", codexClientVersion)

	if token.ExternalID != "" {
		header.Set("Chatgpt-Account-Id", token.ExternalID)
	}

	var answer struct {
		Models []struct {
			Slug           string `json:"slug"`
			Visibility     string `json:"visibility"`
			SupportedInAPI bool   `json:"supported_in_api"`
		} `json:"models"`
	}

	address := c.usageBase + "/backend-api/codex/models?client_version=" + url.QueryEscape(codexClientVersion)
	if err := getModels(ctx, c.client, address, header, &answer); err != nil {
		return nil, err
	}

	models := []ServedModel{}

	for _, m := range answer.Models {
		if m.Slug != "" && m.Visibility == "list" && m.SupportedInAPI {
			models = append(models, ServedModel{Name: m.Slug, Remaining: -1})
		}
	}

	return sortedModels(models), nil
}

// Models implements ModelLister with Anthropic's model list, read with the account's OAuth token as omp
// (packages/catalog/src/provider-models/openai-compat.ts), OpenClaw (extensions/anthropic/register.runtime.ts) and
// Hermes (hermes_cli/models.py) read it: the bearer, the OAuth beta and the API version, page by page. Hermes keeps
// the plain beta: some plans refuse the long-context one. The list does not name every model the account is served
// (OpenClaw and Hermes keep a curated list beside it), so it adds to the provider's models and never replaces them.
func (c *Claude) Models(ctx context.Context, token Token) ([]ServedModel, error) {
	models := []ServedModel{}
	after := ""

	for range 20 {
		var page struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
			HasMore bool   `json:"has_more"`
			LastID  string `json:"last_id"`
		}

		query := url.Values{"limit": {"1000"}}
		if after != "" {
			query.Set("after_id", after)
		}

		if err := getModels(ctx, c.client, c.profileBase+"/v1/models?"+query.Encode(), c.usageHeader(token),
			&page); err != nil {
			return nil, err
		}

		for _, m := range page.Data {
			if m.ID != "" {
				models = append(models, ServedModel{Name: m.ID, Remaining: -1})
			}
		}

		if !page.HasMore || page.LastID == "" || page.LastID == after {
			break
		}

		after = page.LastID
	}

	return sortedModels(models), nil
}

// getModels reads a vendor's model list into answer.
func getModels(ctx context.Context, client *http.Client, address string, header http.Header, answer any) error {
	ctx, cancel := context.WithTimeout(ctx, limitsTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return err
	}

	req.Header = header
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("asking which models the account is served: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // read only

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxModelsAnswer))
	if err != nil {
		return fmt.Errorf("reading which models the account is served: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return statusError("asking which models the account is served", resp.StatusCode, body)
	}

	if err := json.Unmarshal(body, answer); err != nil {
		return fmt.Errorf("reading which models the account is served: %w", err)
	}

	return nil
}

func sortedModels(models []ServedModel) []ServedModel {
	slices.SortFunc(models, func(x, y ServedModel) int { return strings.Compare(x.Name, y.Name) })

	return slices.CompactFunc(models, func(x, y ServedModel) bool { return x.Name == y.Name })
}
