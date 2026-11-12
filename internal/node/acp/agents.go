package acp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The agents this runner drives, as data: what differs between two CLIs that
// both speak ACP is their command, their configuration and a few settings
// (Orca's AcpLaunchSpec, OpenHands' table of ACP providers). Each is read in
// the CLI's own source (docs/research/2026-11-10-acp-omp-opencode.md).

const (
	// providerID names the gateway in a CLI's configuration.
	providerID = "hypomnema"
	// keyVariable is the environment variable the CLI reads the gateway's key from.
	keyVariable = "HYPOMNEMA_MODEL_KEY"
	// The size of a model the gateway serves is not known to the node: these
	// are the values omp's and opencode's own documentation use in their
	// examples of a custom model (omp docs/models.md, opencode models.mdx).
	contextWindow = 128000
	maxOutput     = 16384
)

// launch is how to start one turn of an agent.
type launch struct {
	// Dir is the Conversation's own directory for this agent: its
	// configuration and its sessions, kept between turns.
	Dir string
	// URL is the gateway's base URL, ending in /v1.
	URL    string
	Model  string
	Effort string
	// Skills is the folder the Dispatch's skills were placed in, which the
	// CLI is told of when it does not find it by itself; empty without skills.
	Skills string
}

// agent is one ACP CLI.
type agent struct {
	// prepare writes the CLI's configuration under l.Dir and returns the
	// arguments after the binary and the environment that names it.
	prepare func(l launch) (args, env []string, err error)
	// skills is the folder of the Conversation's directory the Dispatch's
	// skills are placed in (issue #21).
	skills func(dir string) string
}

var agents = map[string]agent{
	// omp reads the skills of its agent directory, PI_CODING_AGENT_DIR (omp
	// src/config.ts getConfigDirs, src/discovery/agents.ts loadSkills).
	"omp": {prepare: prepareOmp, skills: func(dir string) string { return filepath.Join(dir, "agent", "skills") }},
	// opencode is told of the folder in its configuration's skills, its
	// "additional paths ... to discover skills from" (schema/src/config.ts).
	"opencode": {prepare: prepareOpencode, skills: func(dir string) string { return filepath.Join(dir, "skills") }},
}

// Runtimes are the runtimes this runner drives.
func Runtimes() []string { return []string{"omp", "opencode"} }

// prepareOmp names the gateway in models.yml of an agent directory of the
// Conversation's own (PI_CODING_AGENT_DIR, as Orca gives an account its
// own), with the tools server's tools shown as plain tools (tools.xdev:
// false) and sessions kept beside it (--session-dir).
func prepareOmp(l launch) ([]string, []string, error) {
	agentDir, sessions := filepath.Join(l.Dir, "agent"), filepath.Join(l.Dir, "sessions")

	models := fmt.Sprintf(`providers:
  %s:
    baseUrl: %s
    apiKey: %s
    api: openai-completions
    models:
      - id: %s
        name: %s
        input: [text]
        reasoning: %t
        contextWindow: %d
        maxTokens: %d
`, providerID, quoteYAML(l.URL), keyVariable, quoteYAML(l.Model), quoteYAML(l.Model), l.Effort != "",
		contextWindow, maxOutput)

	for name, content := range map[string]string{
		filepath.Join(agentDir, "models.yml"): models,
		filepath.Join(agentDir, "config.yml"): "tools:\n  xdev: false\n",
	} {
		if err := writeFile(name, content); err != nil {
			return nil, nil, err
		}
	}

	if err := os.MkdirAll(sessions, 0o700); err != nil {
		return nil, nil, fmt.Errorf("making omp's session directory: %w", err)
	}

	args := []string{"acp", "--model", providerID + "/" + l.Model, "--session-dir", sessions}
	if l.Effort != "" {
		args = append(args, "--thinking", l.Effort)
	}

	// PI_NO_PTY: no terminal is behind the worker (Orca's launch spec).
	return args, []string{"PI_CODING_AGENT_DIR=" + agentDir, "PI_NO_PTY=1"}, nil
}

// prepareOpencode names the gateway in a configuration file of the
// Conversation's own (OPENCODE_CONFIG), away from the user's and the
// project's, with its data under the same directory. Every action is
// allowed but `execute`, the tool that would hide the tools server's tools
// behind code: denied, they are plain tools (opencode core/src/tool.ts).
func prepareOpencode(l launch) ([]string, []string, error) {
	model := map[string]any{
		"capabilities": map[string]any{"tools": true, "input": []string{"text"}, "output": []string{"text"}},
		"limit":        map[string]any{"context": contextWindow, "output": maxOutput},
	}

	settings := map[string]any{
		"model": providerID + "/" + l.Model,
		"providers": map[string]any{providerID: map[string]any{
			"package":  "@opencode/ai/providers/openai-compatible",
			"settings": map[string]any{"baseURL": l.URL, "apiKey": "{env:" + keyVariable + "}"},
			"models":   map[string]any{l.Model: model},
		}},
		"permissions": []map[string]string{
			{"action": "*", "resource": "*", "effect": "allow"},
			{"action": "execute", "resource": "*", "effect": "deny"},
		},
	}
	if l.Skills != "" {
		settings["skills"] = []string{l.Skills}
	}

	config, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return nil, nil, fmt.Errorf("encoding opencode's configuration: %w", err)
	}

	configFile := filepath.Join(l.Dir, "opencode.json")
	if err := writeFile(configFile, string(config)); err != nil {
		return nil, nil, err
	}

	env := []string{"OPENCODE_CONFIG=" + configFile}

	for name, sub := range map[string]string{
		"XDG_CONFIG_HOME": "config", "XDG_DATA_HOME": "data", "XDG_STATE_HOME": "state", "XDG_CACHE_HOME": "cache",
	} {
		dir := filepath.Join(l.Dir, sub)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, nil, fmt.Errorf("making opencode's %s directory: %w", sub, err)
		}

		env = append(env, name+"="+dir)
	}

	// The last three are Orca's: no fetch of the models list, no project
	// configuration, and no question tool, whose forms an ACP client drops.
	return []string{"acp"}, append(env, "OPENCODE_DISABLE_MODELS_FETCH=1", "OPENCODE_DISABLE_PROJECT_CONFIG=1",
		"OPENCODE_ENABLE_QUESTION_TOOL=false", "OPENCODE_CLIENT=acp"), nil
}

func writeFile(name, content string) error {
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		return fmt.Errorf("making %s: %w", filepath.Dir(name), err)
	}

	if err := os.WriteFile(name, []byte(content), 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", name, err)
	}

	return nil
}

// quoteYAML writes a string as a double-quoted YAML scalar.
func quoteYAML(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
