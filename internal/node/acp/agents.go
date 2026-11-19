package acp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/kl3inIT/hypomnema/internal/shared/agentconfig"
)

// The agents this runner drives, as data: what differs between two CLIs that
// both speak ACP is their command, their configuration and a few settings
// (Orca's AcpLaunchSpec, OpenHands' table of ACP providers). Each is read in
// the CLI's own source (docs/research/2026-11-10-acp-omp-opencode.md).

// keyVariable is the environment variable the CLI reads the gateway's key from.
const keyVariable = "HYPOMNEMA_MODEL_KEY"

// launch is how to start one turn of an agent.
type launch struct {
	// Dir is the Conversation's own directory for this agent: its
	// configuration and its sessions, kept between turns.
	Dir string
	// URL is the gateway's base URL, ending in /v1.
	URL   string
	Model string
	// API is the wire for the model when it is a subscription's (nodeproto.Offer.ModelAPI); empty is chat.
	API    string
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
// false) and sessions kept beside it (--session-dir). omp's own web_search
// and generate_image are off (web_search.enabled, generate_image.enabled,
// omp src/tools/settings.ts), so that the worker has one search and one image
// tool, the tools server's (issues #354, #492): a CLI's own tool is off when
// Hypomnema serves the same, as OpenClaw turns a CLI's search off for its
// own (resolveCliNativeWebSearchEnabled).
func prepareOmp(l launch) ([]string, []string, error) {
	agentDir, sessions := filepath.Join(l.Dir, "agent"), filepath.Join(l.Dir, "sessions")

	// The size of a model the gateway serves is not known to the node: omp is given the defaults
	// (agentconfig.DefaultContextWindow, DefaultMaxOutput). omp reads the key from keyVariable.
	gw := agentconfig.Gateway{URL: l.URL, Key: keyVariable, Model: l.Model, API: l.API, Reasoning: l.Effort != ""}
	models := agentconfig.OmpModels(gw)
	config := "tools:\n  xdev: false\nweb_search:\n  enabled: false\ngenerate_image:\n  enabled: false\n"

	for name, content := range map[string]string{
		filepath.Join(agentDir, "models.yml"): models,
		filepath.Join(agentDir, "config.yml"): config,
	} {
		if err := writeFile(name, content); err != nil {
			return nil, nil, err
		}
	}

	if err := os.MkdirAll(sessions, 0o700); err != nil {
		return nil, nil, fmt.Errorf("making omp's session directory: %w", err)
	}

	args := []string{"acp", "--model", agentconfig.OmpModel(gw), "--session-dir", sessions}
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
// Its own `websearch`, `webfetch` and `experimental_image_generate` are
// denied too, which takes them out of its tools (core/src/tool.ts,
// whollyDisabled; for the image tool "permissions are the supported way to
// turn it off", core/src/tool/plugin/image-generate.ts), so that the worker
// has the tools server's web and image tools only (issues #354, #492). It is
// given no image model either, without which its image tool is not offered.
func prepareOpencode(l launch) ([]string, []string, error) {
	settings := agentconfig.OpencodeConfig(agentconfig.Gateway{
		URL: l.URL, Key: "{env:" + keyVariable + "}", Model: l.Model,
	})
	settings["permissions"] = []map[string]string{
		{"action": "*", "resource": "*", "effect": "allow"},
		{"action": "execute", "resource": "*", "effect": "deny"},
		{"action": "websearch", "resource": "*", "effect": "deny"},
		{"action": "webfetch", "resource": "*", "effect": "deny"},
		{"action": "experimental_image_generate", "resource": "*", "effect": "deny"},
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
