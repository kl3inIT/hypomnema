-- A meeting recorded live from the browser (issue #820): the owner's microphone, and online a shared tab as a second
-- track, each streamed to Soniox's realtime transcription; the lines are kept as they are spoken.
--
-- meetings: `size` is 0 while it records, and the state `recording` comes before `transcribed`; `source` says how the
-- meeting came (`upload`, every meeting until now, or `live`) and `kind` how a live one was held (`online`, the
-- microphone and a shared tab; `in_person`, the microphone only), as MemoryOS's meeting (V108) keeps its kind.
-- meeting_speakers and meeting_lines: a voice is one of a track's (MemoryOS V108 meeting_speaker keyed by its track,
-- meeting_utterance referencing (track, speaker)): `file` for an upload, `mic` and `tab` for a live meeting, so the
-- label "1" of the tab and the one of the microphone are two voices. meeting_items: an action's voice is named with
-- its track (MemoryOS V197 owner_track, owner_label). Lines are read by when they were said (meeting_lines_time), as
-- two tracks append them out of `n`'s order.
--
-- SQLite cannot change a table's checks in place: the tables are rebuilt, as 00027 rebuilds providers. Dropping
-- meetings would delete the rows that reference it (ON DELETE CASCADE), so every meeting table is kept aside and put
-- back; meeting_notes is rebuilt as it was, its `format` still written 'auto' (00113).

-- +goose Up
PRAGMA defer_foreign_keys = ON;

CREATE TABLE meetings_kept AS SELECT * FROM meetings;
CREATE TABLE meeting_speakers_kept AS SELECT * FROM meeting_speakers;
CREATE TABLE meeting_lines_kept AS SELECT * FROM meeting_lines;
CREATE TABLE meeting_notes_kept AS SELECT * FROM meeting_notes;
CREATE TABLE meeting_items_kept AS SELECT * FROM meeting_items;

DROP INDEX meeting_items_meeting;
DROP TABLE meeting_items;
DROP TABLE meeting_notes;
DROP TABLE meeting_lines;
DROP TABLE meeting_speakers;
DROP INDEX meetings_created;
DROP TABLE meetings;

CREATE TABLE meetings (
    id          TEXT    NOT NULL PRIMARY KEY,
    title       TEXT    NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
    -- 1 when the owner gave the title; otherwise the notes write one.
    titled      INTEGER NOT NULL CHECK (titled IN (0, 1)),
    language    TEXT    NOT NULL DEFAULT '',
    -- JSON arrays of strings.
    people      TEXT    NOT NULL DEFAULT '[]',
    terms       TEXT    NOT NULL DEFAULT '[]',
    file_name   TEXT    NOT NULL,
    media_type  TEXT    NOT NULL,
    -- 0 while a live meeting records; its recording is written when it ends.
    size        INTEGER NOT NULL CHECK (size >= 0),
    duration_ms INTEGER NOT NULL DEFAULT 0,
    state       TEXT    NOT NULL CHECK (state IN ('recording', 'transcribing', 'transcribed', 'failed')),
    failure     TEXT    NOT NULL DEFAULT '',
    attempts    INTEGER NOT NULL DEFAULT 0,
    -- Who heard it.
    provider    TEXT    NOT NULL DEFAULT '',
    model       TEXT    NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    source      TEXT    NOT NULL DEFAULT 'upload' CHECK (source IN ('upload', 'live')),
    -- '' for an upload.
    kind        TEXT    NOT NULL DEFAULT '' CHECK (kind IN ('', 'online', 'in_person')),
    CHECK ((source = 'upload') = (kind = '')),
    CHECK (size > 0 OR source = 'live')
) STRICT;

CREATE INDEX meetings_created ON meetings (created_at DESC, id);

CREATE TABLE meeting_speakers (
    meeting_id TEXT    NOT NULL REFERENCES meetings (id) ON DELETE CASCADE,
    track      TEXT    NOT NULL DEFAULT 'file' CHECK (track IN ('file', 'mic', 'tab')),
    label      TEXT    NOT NULL CHECK (length(label) BETWEEN 1 AND 32),
    position   INTEGER NOT NULL CHECK (position >= 1),
    name       TEXT CHECK (name IS NULL OR length(name) BETWEEN 1 AND 100),
    PRIMARY KEY (meeting_id, track, label)
) STRICT;

CREATE TABLE meeting_lines (
    meeting_id TEXT    NOT NULL,
    n          INTEGER NOT NULL CHECK (n >= 1),
    track      TEXT    NOT NULL DEFAULT 'file' CHECK (track IN ('file', 'mic', 'tab')),
    start_ms   INTEGER NOT NULL CHECK (start_ms >= 0),
    end_ms     INTEGER NOT NULL CHECK (end_ms >= start_ms),
    speaker    TEXT    NOT NULL,
    heard      TEXT    NOT NULL,
    text       TEXT    NOT NULL,
    PRIMARY KEY (meeting_id, n),
    FOREIGN KEY (meeting_id) REFERENCES meetings (id) ON DELETE CASCADE,
    FOREIGN KEY (meeting_id, track, speaker) REFERENCES meeting_speakers (meeting_id, track, label) ON DELETE CASCADE
) STRICT;

CREATE INDEX meeting_lines_time ON meeting_lines (meeting_id, start_ms, n);

CREATE TABLE meeting_notes (
    meeting_id TEXT    NOT NULL PRIMARY KEY REFERENCES meetings (id) ON DELETE CASCADE,
    format     TEXT    NOT NULL,
    state      TEXT    NOT NULL CHECK (state IN ('waiting', 'writing', 'ready', 'failed')),
    failure    TEXT    NOT NULL DEFAULT '',
    attempts   INTEGER NOT NULL DEFAULT 0,
    model      TEXT    NOT NULL DEFAULT '',
    summary    TEXT    NOT NULL DEFAULT '',
    sections   TEXT    NOT NULL DEFAULT '[]',
    written_at INTEGER,
    updated_at INTEGER NOT NULL
) STRICT;

CREATE TABLE meeting_items (
    id            TEXT    NOT NULL PRIMARY KEY,
    meeting_id    TEXT    NOT NULL REFERENCES meetings (id) ON DELETE CASCADE,
    kind          TEXT    NOT NULL CHECK (kind IN ('decision', 'action')),
    position      INTEGER NOT NULL CHECK (position >= 0),
    text          TEXT    NOT NULL CHECK (length(text) BETWEEN 1 AND 2000),
    owner         TEXT,
    owner_speaker TEXT,
    due           TEXT,
    done          INTEGER NOT NULL DEFAULT 0 CHECK (done IN (0, 1)),
    quote         TEXT,
    line          INTEGER,
    start_ms      INTEGER,
    -- The track of owner_speaker's voice.
    owner_track   TEXT CHECK (owner_track IS NULL OR owner_track IN ('file', 'mic', 'tab')),
    CHECK (kind = 'action' OR (owner IS NULL AND owner_speaker IS NULL AND due IS NULL)),
    CHECK ((owner_speaker IS NULL) = (owner_track IS NULL))
) STRICT;

CREATE INDEX meeting_items_meeting ON meeting_items (meeting_id, kind, position);

INSERT INTO meetings (id, title, titled, language, people, terms, file_name, media_type, size, duration_ms, state,
                      failure, attempts, provider, model, created_at, updated_at, source, kind)
SELECT id, title, titled, language, people, terms, file_name, media_type, size, duration_ms, state, failure,
       attempts, provider, model, created_at, updated_at, 'upload', ''
FROM meetings_kept;

INSERT INTO meeting_speakers (meeting_id, track, label, position, name)
SELECT meeting_id, 'file', label, position, name FROM meeting_speakers_kept;

INSERT INTO meeting_lines (meeting_id, n, track, start_ms, end_ms, speaker, heard, text)
SELECT meeting_id, n, 'file', start_ms, end_ms, speaker, heard, text FROM meeting_lines_kept;

INSERT INTO meeting_notes (meeting_id, format, state, failure, attempts, model, summary, sections, written_at,
                           updated_at)
SELECT meeting_id, format, state, failure, attempts, model, summary, sections, written_at, updated_at
FROM meeting_notes_kept;

INSERT INTO meeting_items (id, meeting_id, kind, position, text, owner, owner_speaker, due, done, quote, line,
                           start_ms, owner_track)
SELECT id, meeting_id, kind, position, text, owner, owner_speaker, due, done, quote, line, start_ms,
       CASE WHEN owner_speaker IS NULL THEN NULL ELSE 'file' END
FROM meeting_items_kept;

DROP TABLE meeting_items_kept;
DROP TABLE meeting_notes_kept;
DROP TABLE meeting_lines_kept;
DROP TABLE meeting_speakers_kept;
DROP TABLE meetings_kept;

-- +goose Down
-- The live meetings go: the tables before them hold one voice per label and no state `recording`.
PRAGMA defer_foreign_keys = ON;

DELETE FROM meetings WHERE source = 'live';

CREATE TABLE meetings_kept AS SELECT * FROM meetings;
CREATE TABLE meeting_speakers_kept AS SELECT * FROM meeting_speakers;
CREATE TABLE meeting_lines_kept AS SELECT * FROM meeting_lines;
CREATE TABLE meeting_notes_kept AS SELECT * FROM meeting_notes;
CREATE TABLE meeting_items_kept AS SELECT * FROM meeting_items;

DROP INDEX meeting_items_meeting;
DROP TABLE meeting_items;
DROP TABLE meeting_notes;
DROP INDEX meeting_lines_time;
DROP TABLE meeting_lines;
DROP TABLE meeting_speakers;
DROP INDEX meetings_created;
DROP TABLE meetings;

CREATE TABLE meetings (
    id          TEXT    NOT NULL PRIMARY KEY,
    title       TEXT    NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
    titled      INTEGER NOT NULL CHECK (titled IN (0, 1)),
    language    TEXT    NOT NULL DEFAULT '',
    people      TEXT    NOT NULL DEFAULT '[]',
    terms       TEXT    NOT NULL DEFAULT '[]',
    file_name   TEXT    NOT NULL,
    media_type  TEXT    NOT NULL,
    size        INTEGER NOT NULL CHECK (size > 0),
    duration_ms INTEGER NOT NULL DEFAULT 0,
    state       TEXT    NOT NULL CHECK (state IN ('transcribing', 'transcribed', 'failed')),
    failure     TEXT    NOT NULL DEFAULT '',
    attempts    INTEGER NOT NULL DEFAULT 0,
    provider    TEXT    NOT NULL DEFAULT '',
    model       TEXT    NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
) STRICT;

CREATE INDEX meetings_created ON meetings (created_at DESC, id);

CREATE TABLE meeting_speakers (
    meeting_id TEXT    NOT NULL REFERENCES meetings (id) ON DELETE CASCADE,
    label      TEXT    NOT NULL CHECK (length(label) BETWEEN 1 AND 32),
    position   INTEGER NOT NULL CHECK (position >= 1),
    name       TEXT CHECK (name IS NULL OR length(name) BETWEEN 1 AND 100),
    PRIMARY KEY (meeting_id, label)
) STRICT;

CREATE TABLE meeting_lines (
    meeting_id TEXT    NOT NULL,
    n          INTEGER NOT NULL CHECK (n >= 1),
    start_ms   INTEGER NOT NULL CHECK (start_ms >= 0),
    end_ms     INTEGER NOT NULL CHECK (end_ms >= start_ms),
    speaker    TEXT    NOT NULL,
    heard      TEXT    NOT NULL,
    text       TEXT    NOT NULL,
    PRIMARY KEY (meeting_id, n),
    FOREIGN KEY (meeting_id) REFERENCES meetings (id) ON DELETE CASCADE,
    FOREIGN KEY (meeting_id, speaker) REFERENCES meeting_speakers (meeting_id, label) ON DELETE CASCADE
) STRICT;

CREATE TABLE meeting_notes (
    meeting_id TEXT    NOT NULL PRIMARY KEY REFERENCES meetings (id) ON DELETE CASCADE,
    format     TEXT    NOT NULL,
    state      TEXT    NOT NULL CHECK (state IN ('waiting', 'writing', 'ready', 'failed')),
    failure    TEXT    NOT NULL DEFAULT '',
    attempts   INTEGER NOT NULL DEFAULT 0,
    model      TEXT    NOT NULL DEFAULT '',
    summary    TEXT    NOT NULL DEFAULT '',
    sections   TEXT    NOT NULL DEFAULT '[]',
    written_at INTEGER,
    updated_at INTEGER NOT NULL
) STRICT;

CREATE TABLE meeting_items (
    id            TEXT    NOT NULL PRIMARY KEY,
    meeting_id    TEXT    NOT NULL REFERENCES meetings (id) ON DELETE CASCADE,
    kind          TEXT    NOT NULL CHECK (kind IN ('decision', 'action')),
    position      INTEGER NOT NULL CHECK (position >= 0),
    text          TEXT    NOT NULL CHECK (length(text) BETWEEN 1 AND 2000),
    owner         TEXT,
    owner_speaker TEXT,
    due           TEXT,
    done          INTEGER NOT NULL DEFAULT 0 CHECK (done IN (0, 1)),
    quote         TEXT,
    line          INTEGER,
    start_ms      INTEGER,
    CHECK (kind = 'action' OR (owner IS NULL AND owner_speaker IS NULL AND due IS NULL))
) STRICT;

CREATE INDEX meeting_items_meeting ON meeting_items (meeting_id, kind, position);

INSERT INTO meetings (id, title, titled, language, people, terms, file_name, media_type, size, duration_ms, state,
                      failure, attempts, provider, model, created_at, updated_at)
SELECT id, title, titled, language, people, terms, file_name, media_type, size, duration_ms, state, failure,
       attempts, provider, model, created_at, updated_at
FROM meetings_kept;

INSERT INTO meeting_speakers (meeting_id, label, position, name)
SELECT meeting_id, label, position, name FROM meeting_speakers_kept;

INSERT INTO meeting_lines (meeting_id, n, start_ms, end_ms, speaker, heard, text)
SELECT meeting_id, n, start_ms, end_ms, speaker, heard, text FROM meeting_lines_kept;

INSERT INTO meeting_notes SELECT * FROM meeting_notes_kept;

INSERT INTO meeting_items (id, meeting_id, kind, position, text, owner, owner_speaker, due, done, quote, line,
                           start_ms)
SELECT id, meeting_id, kind, position, text, owner, owner_speaker, due, done, quote, line, start_ms
FROM meeting_items_kept;

DROP TABLE meeting_items_kept;
DROP TABLE meeting_notes_kept;
DROP TABLE meeting_lines_kept;
DROP TABLE meeting_speakers_kept;
DROP TABLE meetings_kept;
