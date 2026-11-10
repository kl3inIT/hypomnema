-- Whether a node opens a terminal when asked (issue #116): it says so at each hello, since its build may not be
-- able to and its own configuration may have turned it off (Nezha's disable_command_execute is the agent's too).

-- +goose Up
ALTER TABLE nodes ADD COLUMN terminal INTEGER NOT NULL DEFAULT 0 CHECK (terminal IN (0, 1));

-- +goose Down
ALTER TABLE nodes DROP COLUMN terminal;
