-- Routines (issue #211): work a Bot does on a schedule, a prompt it works on with its model or a script that runs
-- with no model. After OpenMausBot's Routine, Hermes's cron job (no_agent, a script that is a file) and OpenClaw's
-- cron jobs with their run history and standing grants (docs/research/2026-11-12-work-on-a-schedule.md).

-- +goose Up
CREATE TABLE routines (
    id              TEXT PRIMARY KEY,
    bot             TEXT    NOT NULL REFERENCES bots (name),
    name            TEXT    NOT NULL CHECK (length(name) > 0),
    kind            TEXT    NOT NULL CHECK (kind IN ('prompt', 'script')),
    -- What a prompt Routine gives its Bot in a new chat.
    prompt          TEXT    NOT NULL DEFAULT '',
    -- A script Routine names a file under the Bot's routines/ directory, never a command line. Its text is kept
    -- here, where the owner reads it, and the launcher writes the file from it before each run: the control
    -- plane's user cannot read the Bot's directory.
    script          TEXT    NOT NULL DEFAULT '',
    script_text     TEXT    NOT NULL DEFAULT '',
    -- The schedule: every N minutes, on the grid of anchor_at (its first due time), or a 5-field cron expression
    -- with its IANA time zone.
    every_minutes   INTEGER,
    anchor_at       INTEGER,
    cron            TEXT,
    timezone        TEXT,
    timeout_seconds INTEGER NOT NULL,
    -- Counts the changes of what runs and when (the prompt, the script, the schedule). An "always for this
    -- Routine" holds for the revision it was given at.
    revision        INTEGER NOT NULL DEFAULT 1,
    state           TEXT    NOT NULL CHECK (state IN ('on', 'paused')),
    paused_reason   TEXT    NOT NULL DEFAULT '',
    -- What the work is, in the words of whoever made it; shown to the owner with the schedule.
    summary         TEXT    NOT NULL DEFAULT '',
    -- The chat it was made in: where a run's result and a failure are told, unless announce is 0.
    chat_id         TEXT,
    announce        INTEGER NOT NULL DEFAULT 1,
    next_run_at     INTEGER,
    -- Runs that failed in a row, and when the owner was last told of them.
    failures        INTEGER NOT NULL DEFAULT 0,
    alerted_at      INTEGER,
    -- Due times that passed while a run was still live: they are skipped and counted, with no run of their own.
    skipped         INTEGER NOT NULL DEFAULT 0,
    last_skipped_at INTEGER,
    created_by      TEXT    NOT NULL,
    -- What undoes a Bot's last change, as JSON: when it was applied and what the Routine was before, or nothing
    -- before for a Routine the Bot made. NULL when the last change was not a Bot's.
    undo            TEXT,
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL,
    UNIQUE (bot, name)
) STRICT;

CREATE INDEX routines_due ON routines (state, next_run_at);

-- One run of a Routine, started by the schedule or by hand. key_hash is the digest of the key the launcher of a script was given for this run, once:
-- a request that presents the key is this run's.
CREATE TABLE routine_runs (
    id            TEXT PRIMARY KEY,
    routine_id    TEXT    NOT NULL REFERENCES routines (id) ON DELETE CASCADE,
    started_by    TEXT    NOT NULL CHECK (started_by IN ('schedule', 'manual')),
    state         TEXT    NOT NULL CHECK (state IN ('running', 'waiting', 'succeeded', 'failed', 'canceled')),
    started_at    INTEGER NOT NULL,
    ended_at      INTEGER,
    exit_code     INTEGER,
    output        TEXT    NOT NULL DEFAULT '',
    error         TEXT    NOT NULL DEFAULT '',
    chat_id       TEXT,
    key_hash      TEXT
) STRICT;

-- One live run a Routine, kept by the database (MemoryOS's partial unique index on live attempts).
CREATE UNIQUE INDEX routine_runs_live ON routine_runs (routine_id) WHERE state IN ('running', 'waiting');
CREATE INDEX routine_runs_of ON routine_runs (routine_id, started_at DESC, id DESC);
CREATE INDEX routine_runs_chat ON routine_runs (chat_id) WHERE chat_id IS NOT NULL;
CREATE INDEX routine_runs_key ON routine_runs (key_hash) WHERE key_hash IS NOT NULL;

-- "Always for this Routine": an action of a Connection that passes in the Routine's later runs without asking
-- (OpenClaw's standing grant, bound to the job and its config revision). It holds while the Routine is at the
-- revision it was given at, and until the owner takes it back.
CREATE TABLE routine_grants (
    routine_id  TEXT    NOT NULL REFERENCES routines (id) ON DELETE CASCADE,
    integration TEXT    NOT NULL,
    label       TEXT    NOT NULL,
    action      TEXT    NOT NULL,
    action_name TEXT    NOT NULL,
    revision    INTEGER NOT NULL,
    approval_id TEXT    NOT NULL,
    created_at  INTEGER NOT NULL,
    PRIMARY KEY (routine_id, integration, label, action)
) STRICT;

-- An Approval asked in a Routine's run names the Routine and the run. standing says the owner answered "always for
-- this Routine": the approvals table's check on scope cannot be changed without rebuilding the table, so that
-- answer is kept as scope 'session' with standing 1. expired says nobody answered while the run's call waited, or
-- the run ended first: the Approval was refused for him.
ALTER TABLE approvals ADD COLUMN routine_id TEXT;
ALTER TABLE approvals ADD COLUMN routine_run_id TEXT;
ALTER TABLE approvals ADD COLUMN standing INTEGER NOT NULL DEFAULT 0;
ALTER TABLE approvals ADD COLUMN expired INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE approvals DROP COLUMN expired;
ALTER TABLE approvals DROP COLUMN standing;
ALTER TABLE approvals DROP COLUMN routine_run_id;
ALTER TABLE approvals DROP COLUMN routine_id;

DROP TABLE routine_grants;

DROP TABLE routine_runs;

DROP TABLE routines;
