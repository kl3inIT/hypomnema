-- Web search's free tiers, several keys per provider and how much is used (issue #393). SearXNG is gone (owner,
-- 2026-11-14): its row and the base_url column that held its address go. A keyed provider's place in the order the
-- owner dragged it to is its position (NULL: after those that have one, in the default order). Each call to a
-- provider is counted by its UTC day, for its key or without one (key ''): what answered, what failed, and what the
-- provider said it cost (Exa's costDollars, in millionths of a dollar). Read and written only by the model gateway.

-- +goose Up
DELETE FROM gateway_search_providers WHERE provider = 'searxng';
ALTER TABLE gateway_search_providers DROP COLUMN base_url;
ALTER TABLE gateway_search_providers ADD COLUMN position INTEGER;

CREATE TABLE gateway_search_usage (
    provider     TEXT    NOT NULL CHECK (length(provider) > 0),
    key          TEXT    NOT NULL DEFAULT '',
    -- The UTC day, as YYYY-MM-DD.
    day          TEXT    NOT NULL CHECK (length(day) = 10),
    answered     INTEGER NOT NULL DEFAULT 0,
    failed       INTEGER NOT NULL DEFAULT 0,
    cost_micros  INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (provider, key, day)
) STRICT;

CREATE INDEX gateway_search_usage_day ON gateway_search_usage (day);

-- +goose Down
DROP TABLE gateway_search_usage;
ALTER TABLE gateway_search_providers DROP COLUMN position;
ALTER TABLE gateway_search_providers ADD COLUMN base_url TEXT;
