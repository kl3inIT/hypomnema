-- Signed-in accounts of social listening's sources (issue #1291): X and LinkedIn, read as the owner's own account.
-- listening_sources.consented_at is when the owner agreed to a source's consent card, before its first sign-in and
-- separately for each source (last30days references/reddit-session.md: "It needs its own yes. X consent never implies
-- it"); a source with a card stays off until then. listening_accounts holds a source's one signed-in account
-- (Agent-Reach sessions.md:20 uses one chosen session per site and never switches at random): its label, the vault
-- secret of its cookie jar (the cookies themselves never come here), its state as the last real call saw it
-- (signed_in, need_login after the site asked for a sign-in, refused after a challenge or a block, which only a new
-- sign-in clears: a login or a block stops an account until a person acts, research note §3), the cause, and until
-- when it is stopped after a throttling page (linkedin-mcp-server AGENTS.md:209-223: "on a login challenge, a
-- CAPTCHA, or a rate-limit page, stop all live checks for 24 hours"), kept across restarts.

-- +goose Up
ALTER TABLE listening_sources ADD COLUMN consented_at INTEGER;

CREATE TABLE listening_accounts (
    source        TEXT    NOT NULL PRIMARY KEY CHECK (length(source) > 0),
    label         TEXT    NOT NULL CHECK (length(label) > 0),
    secret        TEXT    NOT NULL CHECK (length(secret) > 0),
    state         TEXT    NOT NULL DEFAULT 'signed_in' CHECK (state IN ('signed_in', 'need_login', 'refused')),
    cause         TEXT    NOT NULL DEFAULT '',
    stopped_until INTEGER,
    signed_in_at  INTEGER NOT NULL,
    checked_at    INTEGER,
    updated_at    INTEGER NOT NULL
) STRICT;

-- +goose Down
DROP TABLE listening_accounts;
ALTER TABLE listening_sources DROP COLUMN consented_at;
