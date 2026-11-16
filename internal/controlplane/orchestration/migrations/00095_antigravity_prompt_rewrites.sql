-- Owner-declared find→replace rules for the system instruction the gateway sends an Antigravity subscription
-- (issue #591). Google's Cloud Code backend answers a false 429 RESOURCE_EXHAUSTED to a system prompt whose
-- content its filter flags (an agent's identity sentence, a harness marker), while the quota is whole. The owner,
-- who accepts the account risk, declares each rule; the table is empty by default and nothing is rewritten.
-- Only internal/controlplane/modelgateway reads these, as it does the other gateway tables.

-- +goose Up
CREATE TABLE antigravity_prompt_rewrites (
    -- The order the rules are applied in, from 0.
    position   INTEGER NOT NULL PRIMARY KEY CHECK (position >= 0),
    -- The literal text to find; its replacement, which may be empty to delete the text.
    find       TEXT    NOT NULL CHECK (length(find) > 0),
    replacement TEXT   NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;

-- +goose Down
DROP TABLE antigravity_prompt_rewrites;
