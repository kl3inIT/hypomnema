-- The Usage page (issue #199, step 4): the subscription account that served a request, as 9router-go's tracker
-- (usagetracker/tracker.go) and new-api's log (model/log.go) record theirs, and the provider's error message for a
-- request that failed, as LiteLLM's and new-api's logs keep one. Empty for an API provider, for a request before
-- this, and for one that did not fail. Only the message is kept, never a prompt or an answer.

-- +goose Up
ALTER TABLE gateway_usage ADD COLUMN account_id TEXT NOT NULL DEFAULT '';
ALTER TABLE gateway_usage ADD COLUMN error TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE gateway_usage DROP COLUMN error;
ALTER TABLE gateway_usage DROP COLUMN account_id;
