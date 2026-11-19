package modelgateway

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway/internal/store"
	"github.com/kl3inIT/hypomnema/internal/shared/agentconfig"
)

// Connecting a tool (issue #907): Settings → Connect a tool shows, for an owner-made key and one of its models, the
// file that points Claude Code, Codex, omp or OpenCode at the gateway, as 9router's CLI tools page does
// (src/app/(dashboard)/dashboard/cli-tools: a card per tool, a key and a model picked, "Manual Config" shows each
// file with its path and a copy button). The files are agentconfig's, which also writes the workers' omp and
// OpenCode files (internal/node/acp/agents.go), so the two never drift.

// Tool is a tool the page connects.
type Tool string

// The tools, the four the owner named (2026-11-19).
const (
	ToolClaudeCode Tool = "claude_code"
	ToolCodex      Tool = "codex"
	ToolOmp        Tool = "omp"
	ToolOpencode   Tool = "opencode"
)

// Placeholders in a file whose key has no stored value (made before issue #887 or without a vault), or whose key
// may use no model the tool can call.
const (
	KeyPlaceholder   = "<your gateway key>"
	ModelPlaceholder = "<model>"
)

// ToolFile is one file a tool reads: where it goes, what it holds, and its format (json, toml, yaml).
type ToolFile struct {
	Path    string
	Content string
	Format  string
}

// ToolConfig is a tool's configuration for one key: the models the key may use from the tool, the one the files
// name, whether they hold the key's value, and the files.
type ToolConfig struct {
	Tool      Tool
	Models    []string
	Model     string
	KeyStored bool
	Files     []ToolFile
}

// ToolConfig writes a tool's files for an owner-made key that still works and the model asked for, else the key's
// first, at the gateway whose origin is baseURL (the web page's own, as the Gateway keys page gives a tool its
// address). A model the tool cannot call is not offered: Codex calls /v1/responses only (CodexModels).
func (s *Service) ToolConfig(ctx context.Context, keyID string, tool Tool, model, baseURL string) (ToolConfig, error) {
	origin, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || (origin.Scheme != "http" && origin.Scheme != "https") || origin.Host == "" {
		return ToolConfig{}, fmt.Errorf("%w: the base URL is an absolute http or https address", ErrInvalid)
	}

	row, err := liveOwnerKey(ctx, store.New(s.db), keyID)
	if err != nil {
		return ToolConfig{}, err
	}

	key, err := keyFromRow(row)
	if err != nil {
		return ToolConfig{}, err
	}

	var models []Model

	switch tool {
	case ToolCodex:
		models, err = s.CodexModels(ctx, key)
	case ToolClaudeCode, ToolOmp, ToolOpencode:
		models, err = s.ModelsFor(ctx, key)
	default:
		return ToolConfig{}, fmt.Errorf("%w: a tool is claude_code, codex, omp or opencode, not %q", ErrInvalid, tool)
	}

	if err != nil {
		return ToolConfig{}, err
	}

	out := ToolConfig{Tool: tool, Models: make([]string, 0, len(models))}
	for _, m := range models {
		out.Models = append(out.Models, m.Name)
	}

	gw := agentconfig.Gateway{URL: origin.String() + "/v1", Key: KeyPlaceholder, Model: ModelPlaceholder}

	if i := slices.Index(out.Models, model); i >= 0 || len(models) > 0 {
		chosen := models[max(i, 0)]
		out.Model, gw.Model = chosen.Name, chosen.Name

		if err := s.describe(ctx, chosen, &gw); err != nil {
			return ToolConfig{}, err
		}
	}

	if row.SecretVersionID.Valid {
		if gw.Key, err = s.KeyValue(ctx, keyID); err != nil {
			return ToolConfig{}, err
		}

		out.KeyStored = true
	}

	file, err := toolFile(tool, gw)
	if err != nil {
		return ToolConfig{}, err
	}

	out.Files = []ToolFile{file}

	return out, nil
}

// describe gives the gateway's view of a model what a tool is told of it: its window, largest output and whether
// it reasons (as pi is told them, clientcli/pimodels.go), and its subscription's own wire for a name
// "<subscription>/<model>" (as a worker's offer chooses it, orchestration modelAPI).
func (s *Service) describe(ctx context.Context, m Model, gw *agentconfig.Gateway) error {
	r, err := s.Resolve(ctx, m)
	if err != nil {
		return err
	}

	gw.ContextWindow, gw.MaxOutput = deref(r.ContextWindow.Value), deref(r.MaxOutput.Value)
	gw.Reasoning = deref(r.Reasoning.Value)

	if provider, _, found := strings.Cut(m.Name, "/"); found {
		if p, err := s.providers(ctx, provider); err == nil {
			gw.API = agentconfig.Wires[p.Kind]
		}
	}

	return nil
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}

	return *p
}

// toolFile is a tool's file at the path the tool reads it from: Claude Code's user settings
// (llm-gateway-connect, "Set in a settings file"), Codex's config.toml (9router's CodexToolCard.js), omp's agent
// directory (omp docs/models.md) and OpenCode's global configuration (opencode config.mdx).
func toolFile(tool Tool, gw agentconfig.Gateway) (ToolFile, error) {
	var (
		f   ToolFile
		err error
	)

	switch tool {
	case ToolClaudeCode:
		f.Path, f.Format = "~/.claude/settings.json", "json"
		f.Content, err = agentconfig.ClaudeSettings(gw)
	case ToolCodex:
		f.Path, f.Format, f.Content = "~/.codex/config.toml", "toml", agentconfig.CodexConfig(gw)
	case ToolOmp:
		f.Path, f.Format, f.Content = "~/.omp/agent/models.yml", "yaml", agentconfig.OmpModels(gw)
	case ToolOpencode:
		f.Path, f.Format = "~/.config/opencode/opencode.json", "json"
		f.Content, err = agentconfig.OpencodeFile(gw)
	}

	return f, err
}
