-- What the owner set for each web search provider on Settings → Gateway → Web search (issue #354): its switch, and
-- for SearXNG its address. A provider with no row is on and has no address (owner, 2026-11-13: each is on until
-- the owner turns it off). Read and written only by the model gateway. Replaces HYPOMNEMA_WEB_SEARCH_OFF and
-- HYPOMNEMA_WEB_SEARCH_SEARXNG_URL.

-- +goose Up
CREATE TABLE gateway_search_providers (
    provider   TEXT    NOT NULL PRIMARY KEY CHECK (length(provider) > 0),
    off        INTEGER NOT NULL DEFAULT 0 CHECK (off IN (0, 1)),
    -- The address of the owner's SearXNG; NULL for every other provider.
    base_url   TEXT,
    updated_at INTEGER NOT NULL
) STRICT;

-- +goose Down
DROP TABLE gateway_search_providers;
