package claude

import (
	"context"
	"encoding/json"

	"github.com/kl3inIT/hypomnema/internal/node/bridge"
	"github.com/kl3inIT/hypomnema/internal/node/runner"
	"github.com/kl3inIT/hypomnema/internal/node/workertools"
)

func (t *turn) recoverFinish(ctx context.Context, w *runner.Worker, e event) {
	var message struct {
		Content []struct {
			Type      string          `json:"type"`
			ID        string          `json:"id"`
			Name      string          `json:"name"`
			Input     json.RawMessage `json:"input"`
			ToolUseID string          `json:"tool_use_id"`
			IsError   bool            `json:"is_error"`
			Content   json.RawMessage `json:"content"`
		} `json:"content"`
	}
	if json.Unmarshal(e.Message, &message) != nil {
		return
	}

	for _, block := range message.Content {
		switch {
		case e.Type == "assistant" && block.Type == "tool_use" && block.ID != "" &&
			block.Name == "mcp__"+workertools.Name+"__finish":
			if t.finishCalls == nil {
				t.finishCalls = make(map[string]json.RawMessage)
			}
			t.finishCalls[block.ID] = block.Input
		case e.Type == "user" && block.Type == "tool_result":
			input, ok := t.finishCalls[block.ToolUseID]
			delete(t.finishCalls, block.ToolUseID)
			if ok && block.IsError && closedPermissionStream(block.Content) {
				recordRecoveredFinish(ctx, w, input)
			}
		}
	}
}

func recordRecoveredFinish(ctx context.Context, w *runner.Worker, input json.RawMessage) {
	final, err := workertools.ParseFinish(input)
	if err == nil {
		err = bridge.RecoverFinish(bridge.Dir(w.Dir()), final)
	}
	w.Report(ctx, err)
}

func closedPermissionStream(raw json.RawMessage) bool {
	var message string
	if json.Unmarshal(raw, &message) != nil {
		return false
	}

	switch message {
	case "Tool permission request failed: Stream closed", "Tool permission request failed: AbortError: Stream closed":
		return true
	default:
		return false
	}
}
