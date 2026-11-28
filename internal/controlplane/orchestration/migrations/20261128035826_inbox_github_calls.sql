-- The calls of the owner's GitHub App (issue #1232): a comment, a review comment or a review naming the App
-- (@<app-slug>) by someone who may write to the repository, which the control plane acts on. Each issue or pull
-- request has one Inbox item, given to Hypo, while it is open; the item's source is the new kind github. Omnara
-- launches an agent on a mention and later mentions reach that same agent (docs/integrations/github.mdx:258-304); the
-- Inbox item as its container is the design's (#1232, row 4).
--
-- SQLite cannot change a column's check in place: todos is rebuilt from its definition now, 00174's (its Up) with
-- project_id's reference to departments (00175), in the same order; its events, and the review limits reached that
-- name an item (review_stall_todos, 00193), are kept aside and put back as 00174 keeps the events, since dropping
-- todos deletes them (ON DELETE CASCADE). No other table has a foreign key to todos.

-- +goose Up

PRAGMA defer_foreign_keys = ON;

CREATE TABLE todos_kept AS SELECT * FROM todos;
CREATE TABLE todo_events_kept AS SELECT * FROM todo_events;
CREATE TABLE review_stall_todos_kept AS SELECT * FROM review_stall_todos;

DROP INDEX todo_events_todo;
DROP TABLE todo_events;
DROP INDEX todos_idempotency;
DROP INDEX todos_state_due;
DROP INDEX todos_duplicate_of;
DROP INDEX todos_approval;
DROP INDEX todos_attempt;
DROP INDEX todos_work;
DROP INDEX todos_chat;
DROP INDEX todos_project;
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
    -- for zalo, whatsapp and gmail; optional for lark; never for a meeting, which is the control plane's own. github: a
    -- call of the owner's GitHub App on an issue or pull request (issue #1232), source_id its owner/name#number in
    -- lower case and source_ref the first call (url).
    source_kind      TEXT    NOT NULL
        CHECK (source_kind IN ('owner', 'chat', 'routine', 'lark', 'zalo', 'whatsapp', 'gmail', 'meeting', 'github')),
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
    why_open         TEXT    CHECK (why_open IN ('asked_directly', 'promised', 'waiting')),
    chat_id          TEXT,
    passed_by        TEXT,
    priority         INTEGER NOT NULL DEFAULT 0 CHECK (priority BETWEEN 0 AND 4),
    priority_reason  TEXT    NOT NULL DEFAULT '' CHECK (length(priority_reason) <= 300),
    project_id       TEXT    REFERENCES departments (id) ON DELETE SET NULL,
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
    CHECK (source_kind NOT IN ('lark', 'zalo', 'whatsapp', 'gmail', 'github') OR source_ref IS NOT NULL),
    CHECK (source_kind NOT IN ('zalo', 'whatsapp', 'gmail') OR source_account <> ''),
    CHECK (source_kind IN ('lark', 'zalo', 'whatsapp', 'gmail') OR source_account = '')
) STRICT;

INSERT INTO todos (id, revision, title, notes, done_when, draft, due_kind, due_date, due_zone, due_at, overdue_at,
                   assignee_kind, assignee, state, snoozed_until, source_kind, source_id, source_account,
                   source_quote, source_ref, created_by_kind, created_by, work_attempt, work_kind, work_id, work_bot,
                   work_started_at, approval_id, done_by_kind, done_by, recorded_by_kind, recorded_by, closed_how,
                   close_reason, evidence, closed_at, duplicate_of, idempotency_key, idempotency_hash, created_at,
                   updated_at, why_open, chat_id, passed_by, priority, priority_reason, project_id)
SELECT id, revision, title, notes, done_when, draft, due_kind, due_date, due_zone, due_at, overdue_at,
       assignee_kind, assignee, state, snoozed_until, source_kind, source_id, source_account,
       source_quote, source_ref, created_by_kind, created_by, work_attempt, work_kind, work_id, work_bot,
       work_started_at, approval_id, done_by_kind, done_by, recorded_by_kind, recorded_by, closed_how,
       close_reason, evidence, closed_at, duplicate_of, idempotency_key, idempotency_hash, created_at,
       updated_at, why_open, chat_id, passed_by, priority, priority_reason, project_id
FROM todos_kept;

CREATE UNIQUE INDEX todos_idempotency ON todos (idempotency_key) WHERE idempotency_key IS NOT NULL;
CREATE INDEX todos_state_due ON todos (state, overdue_at, created_at);
CREATE INDEX todos_duplicate_of ON todos (duplicate_of) WHERE duplicate_of IS NOT NULL;
CREATE INDEX todos_approval ON todos (approval_id) WHERE approval_id IS NOT NULL;
CREATE INDEX todos_attempt ON todos (work_attempt) WHERE work_attempt IS NOT NULL;
CREATE INDEX todos_work ON todos (work_kind, work_id) WHERE work_kind IS NOT NULL;
CREATE INDEX todos_chat ON todos (chat_id) WHERE chat_id IS NOT NULL;
CREATE INDEX todos_project ON todos (project_id) WHERE project_id IS NOT NULL;

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

INSERT INTO review_stall_todos (run_id, head_sha, pull_request, todo_id, long_report)
SELECT run_id, head_sha, pull_request, todo_id, long_report FROM review_stall_todos_kept;

DROP TABLE review_stall_todos_kept;
DROP TABLE todo_events_kept;
DROP TABLE todos_kept;

-- Each call (todo_github_calls): its delivery, the item it went to, where it was made (the repository, the issue or
-- pull request, the comment or review by its id and kind, and for a review comment the first comment of its thread,
-- reply_to, which GitHub takes replies to) and by whom, as GitHub's signed delivery says (sender),
-- never its words, which are the item's thread. replied_at is when the control plane answered it on GitHub with the
-- outcome, once the item closed (claude-code-action's tracking comment, src/github/operations/comments); a reopen keeps
-- the close a waiting reply answers in closed_state, closed_reason and closed_evidence. todo_id has
-- no foreign key, as todo_chat_departments has none, so that a later rebuild of todos keeps the calls.
CREATE TABLE todo_github_calls (
    delivery   TEXT    NOT NULL PRIMARY KEY CHECK (length(delivery) BETWEEN 1 AND 128),
    todo_id    TEXT    NOT NULL,
    repository TEXT    NOT NULL CHECK (repository = lower(repository) AND instr(repository, '/') > 1),
    number     INTEGER NOT NULL CHECK (number > 0),
    kind       TEXT    NOT NULL CHECK (kind IN ('comment', 'review_comment', 'review')),
    comment_id INTEGER NOT NULL CHECK (comment_id > 0),
    reply_to   INTEGER NOT NULL DEFAULT 0 CHECK (reply_to >= 0),
    login      TEXT    NOT NULL CHECK (length(login) BETWEEN 1 AND 100),
    url        TEXT    NOT NULL,
    replied_at INTEGER,
    reply_url  TEXT    NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    -- The close a reply still waiting answers, kept when a later call reopens the item before the reply went: the
    -- item's revision that close left, which names that close alone, and its state, reason and evidence (JSON); NULL
    -- while the item's own close is the one to answer.
    closed_revision INTEGER,
    closed_state    TEXT CHECK (closed_state IS NULL OR closed_state IN ('done', 'dropped', 'duplicate')),
    closed_reason   TEXT,
    closed_evidence TEXT
) STRICT;

CREATE INDEX todo_github_calls_todo ON todo_github_calls (todo_id, created_at);
CREATE INDEX todo_github_calls_unreplied ON todo_github_calls (todo_id) WHERE replied_at IS NULL;

-- No delivery's body is kept any more: a call's words go to its item's thread, and nothing else's is needed. The bodies
-- 00188 kept for this issue go now, not when their thirty days end.
UPDATE github_deliveries SET payload = NULL WHERE payload IS NOT NULL;

-- +goose Down
-- The items GitHub's calls made go with their events, and so do items marked their duplicates: the table before has
-- no source for them.
DROP INDEX todo_github_calls_unreplied;
DROP INDEX todo_github_calls_todo;
DROP TABLE todo_github_calls;


PRAGMA defer_foreign_keys = ON;

CREATE TABLE todos_kept AS SELECT * FROM todos
WHERE source_kind <> 'github' AND coalesce(duplicate_of, '') NOT IN (SELECT id FROM todos WHERE source_kind = 'github');
CREATE TABLE todo_events_kept AS SELECT * FROM todo_events WHERE todo_id IN (SELECT id FROM todos_kept);
CREATE TABLE review_stall_todos_kept AS SELECT * FROM review_stall_todos WHERE todo_id IN (SELECT id FROM todos_kept);

DROP INDEX todo_events_todo;
DROP TABLE todo_events;
DROP INDEX todos_idempotency;
DROP INDEX todos_state_due;
DROP INDEX todos_duplicate_of;
DROP INDEX todos_approval;
DROP INDEX todos_attempt;
DROP INDEX todos_work;
DROP INDEX todos_chat;
DROP INDEX todos_project;
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
    why_open         TEXT    CHECK (why_open IN ('asked_directly', 'promised', 'waiting')),
    chat_id          TEXT,
    passed_by        TEXT,
    priority         INTEGER NOT NULL DEFAULT 0 CHECK (priority BETWEEN 0 AND 4),
    priority_reason  TEXT    NOT NULL DEFAULT '' CHECK (length(priority_reason) <= 300),
    project_id       TEXT    REFERENCES departments (id) ON DELETE SET NULL,
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
                   updated_at, why_open, chat_id, passed_by, priority, priority_reason, project_id)
SELECT id, revision, title, notes, done_when, draft, due_kind, due_date, due_zone, due_at, overdue_at,
       assignee_kind, assignee, state, snoozed_until, source_kind, source_id, source_account,
       source_quote, source_ref, created_by_kind, created_by, work_attempt, work_kind, work_id, work_bot,
       work_started_at, approval_id, done_by_kind, done_by, recorded_by_kind, recorded_by, closed_how,
       close_reason, evidence, closed_at, duplicate_of, idempotency_key, idempotency_hash, created_at,
       updated_at, why_open, chat_id, passed_by, priority, priority_reason, project_id
FROM todos_kept;

CREATE UNIQUE INDEX todos_idempotency ON todos (idempotency_key) WHERE idempotency_key IS NOT NULL;
CREATE INDEX todos_state_due ON todos (state, overdue_at, created_at);
CREATE INDEX todos_duplicate_of ON todos (duplicate_of) WHERE duplicate_of IS NOT NULL;
CREATE INDEX todos_approval ON todos (approval_id) WHERE approval_id IS NOT NULL;
CREATE INDEX todos_attempt ON todos (work_attempt) WHERE work_attempt IS NOT NULL;
CREATE INDEX todos_work ON todos (work_kind, work_id) WHERE work_kind IS NOT NULL;
CREATE INDEX todos_chat ON todos (chat_id) WHERE chat_id IS NOT NULL;
CREATE INDEX todos_project ON todos (project_id) WHERE project_id IS NOT NULL;

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

INSERT INTO review_stall_todos (run_id, head_sha, pull_request, todo_id, long_report)
SELECT run_id, head_sha, pull_request, todo_id, long_report FROM review_stall_todos_kept;

DROP TABLE review_stall_todos_kept;
DROP TABLE todo_events_kept;
DROP TABLE todos_kept;
