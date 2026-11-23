-- Sources a Bot reads in any of its chats (issue #1105): a thread of the owner's Gmail (gmail, named by his Google
-- Connection, the thread's id and the mail) and a meeting he recorded (meeting, named by its id and, when it has one,
-- the action's line). The owner asked on 2026-11-23 that the Thư ký Bot put what it finds in the Inbox wherever it
-- works; a mail and a meeting were only a reference of a Routine's run before.
--
-- SQLite cannot change a table's checks in place: todos is rebuilt, as 00153 rebuilt it, and its events kept aside
-- and put back, since dropping todos deletes them (ON DELETE CASCADE).

-- +goose Up
PRAGMA defer_foreign_keys = ON;

CREATE TABLE todos_kept AS SELECT * FROM todos;
CREATE TABLE todo_events_kept AS SELECT * FROM todo_events;

DROP INDEX todo_events_todo;
DROP TABLE todo_events;
DROP INDEX todos_idempotency;
DROP INDEX todos_state_due;
DROP INDEX todos_duplicate_of;
DROP INDEX todos_approval;
DROP INDEX todos_attempt;
DROP INDEX todos_work;
DROP TABLE todos;

CREATE TABLE todos (
    id               TEXT    PRIMARY KEY,
    revision         INTEGER NOT NULL DEFAULT 1,
    title            TEXT    NOT NULL CHECK (length(title) BETWEEN 1 AND 300),
    notes            TEXT    NOT NULL DEFAULT '' CHECK (length(notes) <= 20000),
    done_when        TEXT    NOT NULL DEFAULT '' CHECK (length(done_when) <= 1000),
    -- The reply drafted for the owner to send himself.
    draft            TEXT    NOT NULL DEFAULT '' CHECK (length(draft) <= 20000),
    due_kind         TEXT    NOT NULL DEFAULT 'none' CHECK (due_kind IN ('none', 'unknown', 'date', 'instant')),
    due_date         TEXT,
    due_zone         TEXT,
    due_at           INTEGER,
    overdue_at       INTEGER,
    assignee_kind    TEXT    NOT NULL DEFAULT 'me' CHECK (assignee_kind IN ('me', 'person', 'bot', 'unknown')),
    assignee         TEXT    NOT NULL DEFAULT '' CHECK (length(assignee) <= 100),
    state            TEXT    NOT NULL
        CHECK (state IN ('proposed', 'open', 'doing', 'review', 'done', 'dropped', 'duplicate')),
    snoozed_until    INTEGER,
    -- owner: by hand; chat: source_id is the chat; routine: source_id is the Routine's run; lark, zalo, whatsapp: a
    -- chat of the owner's own account there, source_id the chat's id at the service and source_ref the message
    -- (lark_message, zalo_message, whatsapp_message); gmail: a thread of the owner's Gmail, source_id the thread's id
    -- and source_ref the mail (mail); meeting: a meeting he recorded, source_id the meeting and source_ref, when the
    -- action has one, its line (meeting_line). source_account is the owner's Connection the chat or thread is in: set
    -- for zalo, whatsapp and gmail; optional for lark; never for a meeting, which is the control plane's own.
    source_kind      TEXT    NOT NULL
        CHECK (source_kind IN ('owner', 'chat', 'routine', 'lark', 'zalo', 'whatsapp', 'gmail', 'meeting')),
    source_id        TEXT    NOT NULL DEFAULT '',
    source_account   TEXT    NOT NULL DEFAULT '',
    source_quote     TEXT    NOT NULL DEFAULT '' CHECK (length(source_quote) <= 2000),
    source_ref       TEXT,
    created_by_kind  TEXT    NOT NULL CHECK (created_by_kind IN ('owner', 'bot', 'system')),
    created_by       TEXT    NOT NULL DEFAULT '',
    work_attempt     TEXT,
    work_kind        TEXT    CHECK (work_kind IN ('chat', 'run')),
    work_id          TEXT,
    work_bot         TEXT,
    work_started_at  INTEGER,
    approval_id      TEXT,
    done_by_kind     TEXT    CHECK (done_by_kind IN ('me', 'person', 'bot')),
    done_by          TEXT,
    recorded_by_kind TEXT    CHECK (recorded_by_kind IN ('owner', 'bot', 'system')),
    recorded_by      TEXT,
    closed_how       TEXT    CHECK (closed_how IN ('hand', 'bot', 'ai', 'time')),
    close_reason     TEXT    NOT NULL DEFAULT '' CHECK (length(close_reason) <= 2000),
    evidence         TEXT    NOT NULL DEFAULT '[]',
    closed_at        INTEGER,
    duplicate_of     TEXT    REFERENCES todos (id),
    idempotency_key  TEXT,
    idempotency_hash TEXT,
    created_at       INTEGER NOT NULL,
    updated_at       INTEGER NOT NULL,
    why_open         TEXT    CHECK (why_open IN ('asked_directly', 'promised')),
    CHECK ((due_kind IN ('none', 'unknown') AND due_date IS NULL AND due_zone IS NULL AND due_at IS NULL
            AND overdue_at IS NULL)
        OR (due_kind = 'date' AND due_date IS NOT NULL AND due_zone IS NOT NULL AND due_at IS NOT NULL
            AND overdue_at IS NOT NULL)
        OR (due_kind = 'instant' AND due_date IS NULL AND due_zone IS NOT NULL AND due_at IS NOT NULL
            AND overdue_at = due_at)),
    CHECK ((assignee_kind IN ('me', 'unknown')) = (assignee = '')),
    CHECK (snoozed_until IS NULL OR state IN ('proposed', 'open')),
    CHECK ((state IN ('doing', 'review')) = (work_attempt IS NOT NULL AND work_kind IS NOT NULL
        AND work_id IS NOT NULL AND work_bot IS NOT NULL AND work_started_at IS NOT NULL)),
    CHECK ((work_attempt IS NULL) = (work_kind IS NULL) AND (work_kind IS NULL) = (work_id IS NULL)
        AND (work_kind IS NULL) = (work_bot IS NULL) AND (work_kind IS NULL) = (work_started_at IS NULL)),
    CHECK ((state = 'review') = (approval_id IS NOT NULL)),
    CHECK ((state = 'duplicate') = (duplicate_of IS NOT NULL)),
    CHECK (duplicate_of IS NULL OR duplicate_of <> id),
    CHECK ((state IN ('done', 'dropped', 'duplicate')) = (closed_at IS NOT NULL)),
    CHECK (state = 'done' OR done_by_kind IS NULL),
    CHECK ((source_kind = 'owner') = (source_id = '')),
    CHECK (source_kind NOT IN ('lark', 'zalo', 'whatsapp', 'gmail') OR source_ref IS NOT NULL),
    CHECK (source_kind NOT IN ('zalo', 'whatsapp', 'gmail') OR source_account <> ''),
    CHECK (source_kind IN ('lark', 'zalo', 'whatsapp', 'gmail') OR source_account = '')
) STRICT;

INSERT INTO todos (id, revision, title, notes, done_when, draft, due_kind, due_date, due_zone, due_at, overdue_at,
                   assignee_kind, assignee, state, snoozed_until, source_kind, source_id, source_account,
                   source_quote, source_ref, created_by_kind, created_by, work_attempt, work_kind, work_id, work_bot,
                   work_started_at, approval_id, done_by_kind, done_by, recorded_by_kind, recorded_by, closed_how,
                   close_reason, evidence, closed_at, duplicate_of, idempotency_key, idempotency_hash, created_at,
                   updated_at, why_open)
SELECT id, revision, title, notes, done_when, draft, due_kind, due_date, due_zone, due_at, overdue_at,
       assignee_kind, assignee, state, snoozed_until, source_kind, source_id, source_account,
       source_quote, source_ref, created_by_kind, created_by, work_attempt, work_kind, work_id, work_bot,
       work_started_at, approval_id, done_by_kind, done_by, recorded_by_kind, recorded_by, closed_how,
       close_reason, evidence, closed_at, duplicate_of, idempotency_key, idempotency_hash, created_at,
       updated_at, why_open
FROM todos_kept;

CREATE UNIQUE INDEX todos_idempotency ON todos (idempotency_key) WHERE idempotency_key IS NOT NULL;
CREATE INDEX todos_state_due ON todos (state, overdue_at, created_at);
CREATE INDEX todos_duplicate_of ON todos (duplicate_of) WHERE duplicate_of IS NOT NULL;
CREATE INDEX todos_approval ON todos (approval_id) WHERE approval_id IS NOT NULL;
CREATE INDEX todos_attempt ON todos (work_attempt) WHERE work_attempt IS NOT NULL;
CREATE INDEX todos_work ON todos (work_kind, work_id) WHERE work_kind IS NOT NULL;

CREATE TABLE todo_events (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    todo_id    TEXT    NOT NULL REFERENCES todos (id) ON DELETE CASCADE,
    at         INTEGER NOT NULL,
    revision   INTEGER NOT NULL,
    actor_kind TEXT    NOT NULL CHECK (actor_kind IN ('owner', 'bot', 'system')),
    actor      TEXT    NOT NULL DEFAULT '',
    kind       TEXT    NOT NULL,
    before     TEXT    NOT NULL DEFAULT '{}',
    after      TEXT    NOT NULL DEFAULT '{}'
) STRICT;

INSERT INTO todo_events (id, todo_id, at, revision, actor_kind, actor, kind, before, after)
SELECT id, todo_id, at, revision, actor_kind, actor, kind, before, after FROM todo_events_kept;

CREATE INDEX todo_events_todo ON todo_events (todo_id, id);

DROP TABLE todo_events_kept;
DROP TABLE todos_kept;

-- +goose Down
PRAGMA defer_foreign_keys = ON;

-- An item from Gmail or a meeting has no place in the table before: it goes with its history, and so does an item
-- marked a duplicate of one, as 00153's Down does.
CREATE TABLE todos_kept AS SELECT * FROM todos WHERE source_kind NOT IN ('gmail', 'meeting');
CREATE TABLE todo_events_kept AS
SELECT * FROM todo_events WHERE todo_id IN (SELECT id FROM todos_kept);

DROP INDEX todo_events_todo;
DROP TABLE todo_events;
DROP INDEX todos_idempotency;
DROP INDEX todos_state_due;
DROP INDEX todos_duplicate_of;
DROP INDEX todos_approval;
DROP INDEX todos_attempt;
DROP INDEX todos_work;
DROP TABLE todos;

CREATE TABLE todos (
    id               TEXT    PRIMARY KEY,
    revision         INTEGER NOT NULL DEFAULT 1,
    title            TEXT    NOT NULL CHECK (length(title) BETWEEN 1 AND 300),
    notes            TEXT    NOT NULL DEFAULT '' CHECK (length(notes) <= 20000),
    done_when        TEXT    NOT NULL DEFAULT '' CHECK (length(done_when) <= 1000),
    -- The reply drafted for the owner to send himself.
    draft            TEXT    NOT NULL DEFAULT '' CHECK (length(draft) <= 20000),
    due_kind         TEXT    NOT NULL DEFAULT 'none' CHECK (due_kind IN ('none', 'unknown', 'date', 'instant')),
    due_date         TEXT,
    due_zone         TEXT,
    due_at           INTEGER,
    overdue_at       INTEGER,
    assignee_kind    TEXT    NOT NULL DEFAULT 'me' CHECK (assignee_kind IN ('me', 'person', 'bot', 'unknown')),
    assignee         TEXT    NOT NULL DEFAULT '' CHECK (length(assignee) <= 100),
    state            TEXT    NOT NULL
        CHECK (state IN ('proposed', 'open', 'doing', 'review', 'done', 'dropped', 'duplicate')),
    snoozed_until    INTEGER,
    -- owner: by hand; chat: source_id is the chat; routine: source_id is the Routine's run; lark, zalo, whatsapp: a
    -- chat of the owner's own account there, source_id the chat's id at the service and source_ref the message
    -- (lark_message, zalo_message, whatsapp_message). source_account is the owner's Connection the chat is in: set
    -- for zalo and whatsapp, whose stores key a message by account, chat and id; optional for lark.
    source_kind      TEXT    NOT NULL CHECK (source_kind IN ('owner', 'chat', 'routine', 'lark', 'zalo', 'whatsapp')),
    source_id        TEXT    NOT NULL DEFAULT '',
    source_account   TEXT    NOT NULL DEFAULT '',
    source_quote     TEXT    NOT NULL DEFAULT '' CHECK (length(source_quote) <= 2000),
    source_ref       TEXT,
    created_by_kind  TEXT    NOT NULL CHECK (created_by_kind IN ('owner', 'bot', 'system')),
    created_by       TEXT    NOT NULL DEFAULT '',
    work_attempt     TEXT,
    work_kind        TEXT    CHECK (work_kind IN ('chat', 'run')),
    work_id          TEXT,
    work_bot         TEXT,
    work_started_at  INTEGER,
    approval_id      TEXT,
    done_by_kind     TEXT    CHECK (done_by_kind IN ('me', 'person', 'bot')),
    done_by          TEXT,
    recorded_by_kind TEXT    CHECK (recorded_by_kind IN ('owner', 'bot', 'system')),
    recorded_by      TEXT,
    closed_how       TEXT    CHECK (closed_how IN ('hand', 'bot', 'ai', 'time')),
    close_reason     TEXT    NOT NULL DEFAULT '' CHECK (length(close_reason) <= 2000),
    evidence         TEXT    NOT NULL DEFAULT '[]',
    closed_at        INTEGER,
    duplicate_of     TEXT    REFERENCES todos (id),
    idempotency_key  TEXT,
    idempotency_hash TEXT,
    created_at       INTEGER NOT NULL,
    updated_at       INTEGER NOT NULL,
    why_open         TEXT    CHECK (why_open IN ('asked_directly', 'promised')),
    CHECK ((due_kind IN ('none', 'unknown') AND due_date IS NULL AND due_zone IS NULL AND due_at IS NULL
            AND overdue_at IS NULL)
        OR (due_kind = 'date' AND due_date IS NOT NULL AND due_zone IS NOT NULL AND due_at IS NOT NULL
            AND overdue_at IS NOT NULL)
        OR (due_kind = 'instant' AND due_date IS NULL AND due_zone IS NOT NULL AND due_at IS NOT NULL
            AND overdue_at = due_at)),
    CHECK ((assignee_kind IN ('me', 'unknown')) = (assignee = '')),
    CHECK (snoozed_until IS NULL OR state IN ('proposed', 'open')),
    CHECK ((state IN ('doing', 'review')) = (work_attempt IS NOT NULL AND work_kind IS NOT NULL
        AND work_id IS NOT NULL AND work_bot IS NOT NULL AND work_started_at IS NOT NULL)),
    CHECK ((work_attempt IS NULL) = (work_kind IS NULL) AND (work_kind IS NULL) = (work_id IS NULL)
        AND (work_kind IS NULL) = (work_bot IS NULL) AND (work_kind IS NULL) = (work_started_at IS NULL)),
    CHECK ((state = 'review') = (approval_id IS NOT NULL)),
    CHECK ((state = 'duplicate') = (duplicate_of IS NOT NULL)),
    CHECK (duplicate_of IS NULL OR duplicate_of <> id),
    CHECK ((state IN ('done', 'dropped', 'duplicate')) = (closed_at IS NOT NULL)),
    CHECK (state = 'done' OR done_by_kind IS NULL),
    CHECK ((source_kind = 'owner') = (source_id = '')),
    CHECK (source_kind NOT IN ('lark', 'zalo', 'whatsapp') OR source_ref IS NOT NULL),
    CHECK (source_kind NOT IN ('zalo', 'whatsapp') OR source_account <> ''),
    CHECK (source_kind IN ('lark', 'zalo', 'whatsapp') OR source_account = '')
) STRICT;

INSERT INTO todos (id, revision, title, notes, done_when, draft, due_kind, due_date, due_zone, due_at, overdue_at,
                   assignee_kind, assignee, state, snoozed_until, source_kind, source_id, source_account,
                   source_quote, source_ref, created_by_kind, created_by, work_attempt, work_kind, work_id, work_bot,
                   work_started_at, approval_id, done_by_kind, done_by, recorded_by_kind, recorded_by, closed_how,
                   close_reason, evidence, closed_at, duplicate_of, idempotency_key, idempotency_hash, created_at,
                   updated_at, why_open)
SELECT id, revision, title, notes, done_when, draft, due_kind, due_date, due_zone, due_at, overdue_at,
       assignee_kind, assignee, state, snoozed_until, source_kind, source_id, source_account,
       source_quote, source_ref, created_by_kind, created_by, work_attempt, work_kind, work_id, work_bot,
       work_started_at, approval_id, done_by_kind, done_by, recorded_by_kind, recorded_by, closed_how,
       close_reason, evidence, closed_at, duplicate_of, idempotency_key, idempotency_hash, created_at,
       updated_at, why_open
FROM todos_kept
WHERE state <> 'duplicate' OR duplicate_of IN (SELECT id FROM todos_kept);

CREATE UNIQUE INDEX todos_idempotency ON todos (idempotency_key) WHERE idempotency_key IS NOT NULL;
CREATE INDEX todos_state_due ON todos (state, overdue_at, created_at);
CREATE INDEX todos_duplicate_of ON todos (duplicate_of) WHERE duplicate_of IS NOT NULL;
CREATE INDEX todos_approval ON todos (approval_id) WHERE approval_id IS NOT NULL;
CREATE INDEX todos_attempt ON todos (work_attempt) WHERE work_attempt IS NOT NULL;
CREATE INDEX todos_work ON todos (work_kind, work_id) WHERE work_kind IS NOT NULL;

CREATE TABLE todo_events (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    todo_id    TEXT    NOT NULL REFERENCES todos (id) ON DELETE CASCADE,
    at         INTEGER NOT NULL,
    revision   INTEGER NOT NULL,
    actor_kind TEXT    NOT NULL CHECK (actor_kind IN ('owner', 'bot', 'system')),
    actor      TEXT    NOT NULL DEFAULT '',
    kind       TEXT    NOT NULL,
    before     TEXT    NOT NULL DEFAULT '{}',
    after      TEXT    NOT NULL DEFAULT '{}'
) STRICT;

INSERT INTO todo_events (id, todo_id, at, revision, actor_kind, actor, kind, before, after)
SELECT id, todo_id, at, revision, actor_kind, actor, kind, before, after FROM todo_events_kept
WHERE todo_id IN (SELECT id FROM todos);

CREATE INDEX todo_events_todo ON todo_events (todo_id, id);

DROP TABLE todo_events_kept;
DROP TABLE todos_kept;

