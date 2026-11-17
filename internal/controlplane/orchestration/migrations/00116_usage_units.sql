-- Each kind of usage in its own unit (issue #723, decisions 3.1 to 3.3): a request's web searches, the search a
-- web/search row is and a model's own, which Anthropic reports as usage.server_tool_use.web_search_requests and the
-- Responses API as its web_search_call items, counted on the model call's own row as LiteLLM counts them
-- (llms/anthropic/cost_calculation.py:71-95, litellm_core_utils/llm_cost_calc/tool_call_cost_tracking.py:577-584);
-- and the images an image request made, as LiteLLM's image_count (cost_calculator.py:3151-3156) and OmniRoute's
-- image unit (src/lib/usage/mediaRequestAccounting.ts:88-92). 0 for every other request. Read and written only by
-- the model gateway.

-- +goose Up
ALTER TABLE gateway_usage ADD COLUMN web_searches INTEGER NOT NULL DEFAULT 0;
ALTER TABLE gateway_usage ADD COLUMN images INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE gateway_usage DROP COLUMN images;
ALTER TABLE gateway_usage DROP COLUMN web_searches;
