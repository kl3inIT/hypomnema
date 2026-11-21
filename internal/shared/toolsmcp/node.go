package toolsmcp

import (
	"context"
	"encoding/json"
)

// NodeCall runs a node tool on path.
func (a *Agent) NodeCall(ctx context.Context, path, tool string, arguments json.RawMessage) (json.RawMessage, error) {
	return a.ToolCall(ctx, path, CallTimeout, tool, arguments)
}
