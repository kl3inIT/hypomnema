package workertools

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// memoryTimeout bounds one change: the control plane writes a row.
const memoryTimeout = 30 * time.Second

// memoryTool changes the worker's memory (issue #21), through the control
// plane's agent API with the Dispatch's key, as OpenMausBot's memory_update
// goes through its MCP bridge to its server. One tool with a target, as
// Hermes's memory tool (target: memory | user). Its description never
// changes, so the tools the provider caches stay the same; how full the
// memory is goes in each call's result.
func memoryTool() map[string]any {
	return map[string]any{
		"name": "memory",
		"description": "Change a memory that later tasks get in their first message. target agent is the memory of " +
			"the agent you run as, shared by all its workers; owner is what every agent and Bot knows about the " +
			"owner. add one entry (one line), or replace or remove the one entry that contains match. A change that " +
			"would pass the memory's limit is refused: replace or remove entries first.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"target": map[string]any{"type": "string", "enum": []string{agenttool.MemoryAgent, agenttool.MemoryOwner}},
				"action": map[string]any{"type": "string", "enum": []string{"add", "replace", "remove"}},
				"text":   map[string]any{"type": "string", "description": "The entry to add, or what replaces the one found."},
				"match":  map[string]any{"type": "string", "description": "Part of the one entry to replace or remove."},
			},
			"required": []string{"target", "action"},
		},
	}
}

// memoryCall makes one change and answers with how full the memory is.
func (a *agentAPI) memoryCall(ctx context.Context, arguments json.RawMessage) (map[string]any, error) {
	var change agenttool.MemoryChange
	if err := json.Unmarshal(arguments, &change); err != nil {
		return nil, errors.New("memory needs a target and an action")
	}

	ctx, cancel := context.WithTimeout(ctx, memoryTimeout)
	defer cancel()

	raw, err := a.do(ctx, http.MethodPost, agenttool.PathMemory, change)
	if err != nil {
		// A refusal (the limit, an entry not found) is the model's to fix, not a failure of the tool.
		return toolText(err.Error(), true), nil
	}

	var answer agenttool.MemoryAnswer
	if json.Unmarshal(raw, &answer) != nil {
		return nil, errors.New("the control plane's answer could not be read")
	}

	return toolText("Saved. "+agenttool.MemoryUsage(answer.Used, answer.Limit), false), nil
}
