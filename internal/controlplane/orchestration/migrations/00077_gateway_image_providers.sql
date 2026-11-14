-- What the owner set for each image provider on Settings → Gateway → Images (issue #431), as web search's providers
-- are kept (00067, 00069): its switch, its place in the order the owner set (NULL: after those that have one, in the
-- default order Codex, Antigravity), and the model it makes images with when a request names none (NULL: the
-- provider's default, gpt-image-2 or gemini-3.1-flash-image). A provider with no row is on, in its default place,
-- with its default model. Read and written only by the model gateway.

-- +goose Up
CREATE TABLE gateway_image_providers (
    provider   TEXT    NOT NULL PRIMARY KEY CHECK (length(provider) > 0),
    off        INTEGER NOT NULL DEFAULT 0 CHECK (off IN (0, 1)),
    position   INTEGER,
    model      TEXT CHECK (model IS NULL OR length(model) > 0),
    updated_at INTEGER NOT NULL
) STRICT;

-- +goose Down
DROP TABLE gateway_image_providers;
