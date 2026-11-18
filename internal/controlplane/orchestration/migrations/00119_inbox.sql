-- The Inbox (issues #838, #845): one row per to-do, whoever does it, and its history. Its fields follow Northstar's
-- Task and Omi's action item (models/action_item.py: owner, due, typed evidence; models/task_due.py), its states
-- Plane's Intake (db/models/intake.py) and Hermes's kanban; its events MemoryOS's meeting_minutes_event. The states
-- move only as inboxstore's transitions allow; the checks here keep each field to its state.

-- +goose Up
CREATE TABLE todos (
    id               TEXT    PRIMARY KEY,
    -- Starts at 1 and grows by 1 with every change; a mutation carries the one it read.
    revision         INTEGER NOT NULL DEFAULT 1,
    title            TEXT    NOT NULL CHECK (length(title) BETWEEN 1 AND 300),
    notes            TEXT    NOT NULL DEFAULT '' CHECK (length(notes) <= 20000),
    done_when        TEXT    NOT NULL DEFAULT '' CHECK (length(done_when) <= 1000),
    -- none: no due; unknown: due, when is not known; date: a calendar day (due_date, YYYY-MM-DD) in due_zone, the
    -- owner's; instant: a moment (due_at) with its due_zone. due_at for a date and overdue_at are derived for
    -- ordering and the overdue test only: due_at is the date's first moment in its zone and overdue_at the next
    -- day's, never shown as a time.
    due_kind         TEXT    NOT NULL DEFAULT 'none' CHECK (due_kind IN ('none', 'unknown', 'date', 'instant')),
    due_date         TEXT,
    due_zone         TEXT,
    due_at           INTEGER,
    overdue_at       INTEGER,
    -- me, person (assignee is the name), bot (assignee is the Bot's id) or unknown.
    assignee_kind    TEXT    NOT NULL DEFAULT 'me' CHECK (assignee_kind IN ('me', 'person', 'bot', 'unknown')),
    assignee         TEXT    NOT NULL DEFAULT '' CHECK (length(assignee) <= 100),
    state            TEXT    NOT NULL
        CHECK (state IN ('proposed', 'open', 'doing', 'review', 'done', 'dropped', 'duplicate')),
    snoozed_until    INTEGER,
    -- owner: by hand; chat: source_id is the chat; routine: source_id is the Routine's run.
    source_kind      TEXT    NOT NULL CHECK (source_kind IN ('owner', 'chat', 'routine')),
    source_id        TEXT    NOT NULL DEFAULT '',
    source_quote     TEXT    NOT NULL DEFAULT '' CHECK (length(source_quote) <= 2000),
    -- A typed reference {kind, ref, label} to where it came from, JSON; NULL when none.
    source_ref       TEXT,
    -- Who created it (owner, bot, system) and which one.
    created_by_kind  TEXT    NOT NULL CHECK (created_by_kind IN ('owner', 'bot', 'system')),
    created_by       TEXT    NOT NULL DEFAULT '',
    -- The Bot's current attempt while doing or review: its chat or Run.
    work_kind        TEXT    CHECK (work_kind IN ('chat', 'run')),
    work_id          TEXT,
    -- The one pending Approval while review.
    approval_id      TEXT,
    -- Who did it (me, person, bot) and who recorded it, kept apart.
    done_by_kind     TEXT    CHECK (done_by_kind IN ('me', 'person', 'bot')),
    done_by          TEXT,
    recorded_by_kind TEXT    CHECK (recorded_by_kind IN ('owner', 'bot', 'system')),
    recorded_by      TEXT,
    closed_how       TEXT    CHECK (closed_how IN ('hand', 'bot', 'ai', 'time')),
    close_reason     TEXT    NOT NULL DEFAULT '' CHECK (length(close_reason) <= 2000),
    -- A JSON array of {kind, ref, label}, at most 20.
    evidence         TEXT    NOT NULL DEFAULT '[]',
    closed_at        INTEGER,
    duplicate_of     TEXT    REFERENCES todos (id),
    -- A create's key and the hash of what it asked, so a retry returns the first item and another payload is refused.
    idempotency_key  TEXT,
    idempotency_hash TEXT,
    created_at       INTEGER NOT NULL,
    updated_at       INTEGER NOT NULL,
    CHECK ((due_kind IN ('none', 'unknown') AND due_date IS NULL AND due_zone IS NULL AND due_at IS NULL
            AND overdue_at IS NULL)
        OR (due_kind = 'date' AND due_date IS NOT NULL AND due_zone IS NOT NULL AND due_at IS NOT NULL
            AND overdue_at IS NOT NULL)
        OR (due_kind = 'instant' AND due_date IS NULL AND due_zone IS NOT NULL AND due_at IS NOT NULL
            AND overdue_at = due_at)),
    CHECK ((assignee_kind IN ('me', 'unknown')) = (assignee = '')),
    CHECK (snoozed_until IS NULL OR state IN ('proposed', 'open')),
    CHECK ((state IN ('doing', 'review')) = (work_kind IS NOT NULL AND work_id IS NOT NULL)),
    CHECK ((state = 'review') = (approval_id IS NOT NULL)),
    CHECK ((state = 'duplicate') = (duplicate_of IS NOT NULL)),
    CHECK (duplicate_of IS NULL OR duplicate_of <> id),
    CHECK ((state IN ('done', 'dropped', 'duplicate')) = (closed_at IS NOT NULL)),
    CHECK (state = 'done' OR done_by_kind IS NULL),
    CHECK ((source_kind = 'owner') = (source_id = ''))
) STRICT;

CREATE UNIQUE INDEX todos_idempotency ON todos (idempotency_key) WHERE idempotency_key IS NOT NULL;
CREATE INDEX todos_state_due ON todos (state, overdue_at, created_at);
CREATE INDEX todos_duplicate_of ON todos (duplicate_of) WHERE duplicate_of IS NOT NULL;

CREATE TABLE todo_events (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    todo_id    TEXT    NOT NULL REFERENCES todos (id) ON DELETE CASCADE,
    at         INTEGER NOT NULL,
    -- The revision the change made.
    revision   INTEGER NOT NULL,
    actor_kind TEXT    NOT NULL CHECK (actor_kind IN ('owner', 'bot', 'system')),
    actor      TEXT    NOT NULL DEFAULT '',
    kind       TEXT    NOT NULL,
    -- JSON objects of the fields that changed, before and after.
    before     TEXT    NOT NULL DEFAULT '{}',
    after      TEXT    NOT NULL DEFAULT '{}'
) STRICT;

CREATE INDEX todo_events_todo ON todo_events (todo_id, id);

-- +goose Down
DROP TABLE todo_events;

DROP TABLE todos;
