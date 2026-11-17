-- Meetings (issue #721): a recording the owner uploads, its transcript with speakers, and the notes a gateway model
-- writes of it, with the decisions and actions. Kept by orchestration/meetingstore; the recording's bytes are a file
-- beside the database (controlplane/meetings), as Nojoin keeps its recordings on disk.
--
-- A meeting, as MemoryOS's meeting (V108) with the recording kept: its title (written from the meeting when the owner
-- gave none), the language, the people and the words to hear right given before it was heard, and where its
-- transcription stands. A transcription the control plane was stopped in is taken up again; `attempts` bounds that,
-- as MemoryOS's minutes_attempts.

-- +goose Up
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
    size        INTEGER NOT NULL CHECK (size > 0),
    duration_ms INTEGER NOT NULL DEFAULT 0,
    state       TEXT    NOT NULL CHECK (state IN ('transcribing', 'transcribed', 'failed')),
    failure     TEXT    NOT NULL DEFAULT '',
    attempts    INTEGER NOT NULL DEFAULT 0,
    -- Who heard it.
    provider    TEXT    NOT NULL DEFAULT '',
    model       TEXT    NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
) STRICT;

CREATE INDEX meetings_created ON meetings (created_at DESC, id);

-- A voice of the meeting (MemoryOS meeting_speaker, Nojoin RecordingSpeaker): the provider's label, the order it
-- first spoke in, which numbers it "Speaker N" until the owner names it. A voice the owner made for one line is
-- labelled m1, m2….
CREATE TABLE meeting_speakers (
    meeting_id TEXT    NOT NULL REFERENCES meetings (id) ON DELETE CASCADE,
    label      TEXT    NOT NULL CHECK (length(label) BETWEEN 1 AND 32),
    position   INTEGER NOT NULL CHECK (position >= 1),
    name       TEXT CHECK (name IS NULL OR length(name) BETWEEN 1 AND 100),
    PRIMARY KEY (meeting_id, label)
) STRICT;

-- A line of the transcript (MemoryOS meeting_utterance, V200): the voice it is shown as and the label the provider
-- heard, kept as it was.
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

-- The notes of a meeting (MemoryOS V109's minutes columns): the format they are written in, where writing them
-- stands (waiting for the transcript, writing, ready, failed), the model that wrote them, the summary and the
-- format's sections as JSON [{title, points}].
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

-- A decision the meeting reached or an action it handed out (MemoryOS meeting_minutes_item, V197): the action's
-- owner is a voice of the meeting when the notes named one, so naming the voice names the action, else the words
-- said; the quote and line it rests on, and when that line was said.
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

-- What the owner set on Settings → Meetings: the gateway model that writes notes (Hyprnote's LLM connection, Nojoin's
-- settings). One row.
CREATE TABLE meeting_settings (
    id          INTEGER NOT NULL PRIMARY KEY CHECK (id = 1),
    notes_model TEXT    NOT NULL DEFAULT '',
    updated_at  INTEGER NOT NULL
) STRICT;

-- +goose Down
DROP TABLE meeting_settings;
DROP INDEX meeting_items_meeting;
DROP TABLE meeting_items;
DROP TABLE meeting_notes;
DROP TABLE meeting_lines;
DROP TABLE meeting_speakers;
DROP INDEX meetings_created;
DROP TABLE meetings;
