-- Meetings' words and the owner's notes formats (issue #747).
--
-- The words to hear right are one list the owner keeps on Settings → Meetings and every transcription is given, as
-- Nojoin keeps its glossary in its settings and OpenNoteTaker its DEEPGRAM_KEYTERMS, in place of a list asked with
-- each upload. The words a meeting was uploaded with until now join the list, so none is lost; the meetings' `terms`
-- column is no longer written or read and stays only because a column is not dropped from a released table.
-- The words found elsewhere (the names given to speakers, the Bots', the repositories') are read where they are kept
-- and never copied here.
--
-- The owner's own notes formats, beside the built-in ones of meetings/formats.go: a name and its sections, each a
-- title and a short instruction, as Hyprnote keeps a template's sections as JSON (crates/db-app templates
-- sections_json) and Meetily a template's sections as a list of a title and an instruction.

-- +goose Up
CREATE TABLE meeting_words (
    word       TEXT    NOT NULL PRIMARY KEY CHECK (length(word) BETWEEN 1 AND 100),
    created_at INTEGER NOT NULL
) STRICT;

INSERT OR IGNORE INTO meeting_words (word, created_at)
SELECT trim(t.value), min(m.created_at)
FROM meetings m, json_each(m.terms) t
WHERE t.type = 'text' AND length(trim(t.value)) BETWEEN 1 AND 100
GROUP BY trim(t.value)
ORDER BY min(m.created_at)
LIMIT 100;

CREATE TABLE meeting_formats (
    id         TEXT    NOT NULL PRIMARY KEY,
    name       TEXT    NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    -- JSON [{title, about}].
    sections   TEXT    NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;

-- +goose Down
DROP TABLE meeting_formats;
DROP TABLE meeting_words;
