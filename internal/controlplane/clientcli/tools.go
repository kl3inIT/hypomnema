package clientcli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"

	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
	"github.com/kl3inIT/hypomnema/internal/shared/toolsmcp"
)

func toolAgent(home string) (*toolsmcp.Agent, error) {
	cfg, err := LoadConfig(home)
	if err != nil {
		return nil, err
	}

	return toolsmcp.NewAgentWithClient(cfg.URL, cfg.Token, &http.Client{CheckRedirect: sameOrigin}), nil
}

func listAgentTools(ctx context.Context, agent *toolsmcp.Agent) ([]map[string]any, error) {
	tools, err := agent.Tools(ctx, agenttool.PathTools)
	if err != nil {
		return nil, err
	}

	device, err := agent.Tools(ctx, agenttool.PathDeviceTools)

	return append(tools, device...), err
}

func toolsList(ctx context.Context, env *env, fs *flag.FlagSet) error {
	agent, err := toolAgent(env.home)
	if err != nil {
		return err
	}

	tools, err := listAgentTools(ctx, agent)
	if err != nil {
		return err
	}

	return env.print(fs, tools, func() {
		for _, tool := range tools {
			env.printf("%s\n", tool["name"])
		}
	})
}

func toolsCall(ctx context.Context, env *env, fs *flag.FlagSet) error {
	agent, err := toolAgent(env.home)
	if err != nil {
		return err
	}

	args := "{}"
	if file := str(fs, "args-file"); file != "" {
		if args, err = env.read(file); err != nil {
			return err
		}
	}

	var object map[string]json.RawMessage
	if json.Unmarshal([]byte(args), &object) != nil || object == nil {
		return fmt.Errorf("%w: --args-file must contain one JSON object", ErrUsage)
	}

	result, err := callAgentTool(ctx, agent, fs.Arg(0), json.RawMessage(args))
	if err != nil {
		return err
	}

	return printToolResult(env, fs, result)
}

func printToolResult(env *env, fs *flag.FlagSet, result any) error {
	raw, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("encoding the tool's answer: %w", err)
	}

	var parsed struct {
		IsError bool `json:"isError"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return fmt.Errorf("reading the tool's answer: %w", err)
	}

	if err := env.print(fs, json.RawMessage(raw), func() {
		for _, item := range parsed.Content {
			if item.Type == "text" {
				env.printf("%s\n", item.Text)
			} else {
				env.printf("[%s; use --json for the full result]\n", item.Type)
			}
		}
	}); err != nil {
		return err
	}

	if parsed.IsError {
		return errors.New("the tool did not complete; see its answer")
	}

	return nil
}
