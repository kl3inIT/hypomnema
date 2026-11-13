-- Why the control plane stopped a Dispatch that looped (issue #359): "Stopped after 3 identical errors: …", as
-- OpenMausBot's runtime ends a node `failed(stalled)` with that text (docs/runtime-contract.md §2, §6). Set when the
-- stop is requested; the Dispatch then ends failed with the text as its result's summary, not canceled.

-- +goose Up
ALTER TABLE dispatches ADD COLUMN stalled_reason TEXT;

-- +goose Down
ALTER TABLE dispatches DROP COLUMN stalled_reason;
