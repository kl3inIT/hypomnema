-- What a Bot's chats show of its tool calls (issue #441), after OpenClaw's verboseDefault per agent (off, on, full;
-- src/agents/agent-scope-config.ts): off shows none, on a row per call with its tool's name, full its arguments,
-- output and time as well. Every Bot shows a row per call, as it did before.

-- +goose Up
ALTER TABLE bots ADD COLUMN tool_display TEXT NOT NULL DEFAULT 'on' CHECK (tool_display IN ('off', 'on', 'full'));

-- +goose Down
ALTER TABLE bots DROP COLUMN tool_display;
