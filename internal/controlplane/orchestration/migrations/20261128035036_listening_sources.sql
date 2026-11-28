-- Social listening's sources as the owner sets them and as they last read (issue #1290, the Social listening page).
-- listening_sources holds the owner's switch of a source, whether he was told it broke (postiz's refreshNeeded,
-- flagged once and cleared by the next reading that read items: prisma/schema.prisma:388,
-- integration.repository.ts:603-616, refresh.integration.service.ts:20-54), and
-- its last success and last error (harken's source_metrics last_success_at, last_error_at and last_error,
-- store.py:102-121). listening_backends holds the owner's order of a source's backends (as
-- gateway_search_providers.position orders the keyed web search providers) and each backend's last answer.
-- It also holds how its last reading ended and how the one right before it ended, a real search's or a live
-- test's, for its group and "regressed" (Agent-Reach keeps each check's result and the previous one,
-- runtime/manage.py:189-216). listening_readings is the items real searches read each UTC day, kept 7 days, for its
-- items this week.

-- +goose Up
CREATE TABLE listening_sources (
    source             TEXT    NOT NULL PRIMARY KEY CHECK (length(source) > 0),
    off                INTEGER NOT NULL DEFAULT 0 CHECK (off IN (0, 1)),
    told               INTEGER NOT NULL DEFAULT 0 CHECK (told IN (0, 1)),
    -- The last reading and the one right before it: passed, partial or failed ('' before any), with the last's code.
    last_read_at       INTEGER,
    last_result        TEXT    NOT NULL DEFAULT '' CHECK (last_result IN ('', 'passed', 'partial', 'failed')),
    last_code          TEXT    NOT NULL DEFAULT '',
    previous_result    TEXT    NOT NULL DEFAULT '' CHECK (previous_result IN ('', 'passed', 'partial', 'failed')),
    last_success_at    INTEGER,
    last_error_at      INTEGER,
    last_error_code    TEXT    NOT NULL DEFAULT '',
    last_error_detail  TEXT    NOT NULL DEFAULT '',
    last_error_backend TEXT    NOT NULL DEFAULT '',
    updated_at         INTEGER NOT NULL
) STRICT;

CREATE TABLE listening_backends (
    source        TEXT    NOT NULL CHECK (length(source) > 0),
    backend       TEXT    NOT NULL CHECK (length(backend) > 0),
    -- Its place in the owner's order, from 1; NULL until he sets one, when the registry's order holds.
    position      INTEGER,
    last_ok_at    INTEGER,
    last_error_at INTEGER,
    last_code     TEXT    NOT NULL DEFAULT '',
    last_detail   TEXT    NOT NULL DEFAULT '',
    updated_at    INTEGER NOT NULL,
    PRIMARY KEY (source, backend)
) STRICT;

CREATE TABLE listening_readings (
    source      TEXT    NOT NULL CHECK (length(source) > 0),
    -- The UTC day, as YYYY-MM-DD.
    day         TEXT    NOT NULL CHECK (length(day) = 10),
    -- Items the day's real searches read, before duplicates are removed; a live test's are not counted.
    items       INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (source, day)
) STRICT;

-- +goose Down
DROP TABLE listening_readings;
DROP TABLE listening_backends;
DROP TABLE listening_sources;
