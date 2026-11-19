-- Videos made of the takes agents film (issue #818, rows 1, 2 and 5). A video belongs to one take; each make or
-- change request adds an immutable revision of it (rev 1, 2, ...; parent: the revision it was made from), with the
-- lines, voice, captions and aspect it is made with, the provider, model and voice that spoke it, its length and its
-- state: queued, voicing, composing, captioning, then review, approved or changes_requested, or failed at a stage
-- with a cause. At most one revision of a video is approved. requested_by and request_id make a create safe to
-- repeat: the same pair answers the same revision. Read and written only by internal/controlplane/videos; its files
-- are videos/<video>/<rev>/ beside the database.
--
-- The voice an audio provider speaks with when none is asked (row 5), set on Settings -> Gateway -> Audio; empty:
-- the provider's own default.

-- +goose Up
CREATE TABLE videos (
    id           TEXT    NOT NULL PRIMARY KEY CHECK (length(id) > 0),
    take_id      TEXT    NOT NULL UNIQUE CHECK (length(take_id) > 0),
    title        TEXT    NOT NULL,
    made_by      TEXT    NOT NULL,
    made_by_name TEXT    NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL
) STRICT;

CREATE TABLE video_revisions (
    video_id     TEXT    NOT NULL REFERENCES videos (id),
    rev          INTEGER NOT NULL CHECK (rev > 0),
    parent       INTEGER,
    state        TEXT    NOT NULL CHECK (state IN ('queued', 'voicing', 'composing', 'captioning', 'review',
                                                   'approved', 'changes_requested', 'failed')),
    lines        TEXT    NOT NULL,
    voice        TEXT    NOT NULL DEFAULT '',
    captions     INTEGER NOT NULL CHECK (captions IN (0, 1)),
    aspect       TEXT    NOT NULL,
    provider     TEXT    NOT NULL DEFAULT '',
    model        TEXT    NOT NULL DEFAULT '',
    used_voice   TEXT    NOT NULL DEFAULT '',
    length_ms    INTEGER NOT NULL DEFAULT 0,
    failed_stage TEXT    NOT NULL DEFAULT '',
    failed_cause TEXT    NOT NULL DEFAULT '',
    note         TEXT    NOT NULL DEFAULT '',
    warnings     TEXT    NOT NULL DEFAULT '[]',
    requested_by TEXT    NOT NULL,
    request_id   TEXT    NOT NULL DEFAULT '',
    request_hash TEXT    NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL,
    PRIMARY KEY (video_id, rev)
) STRICT;

CREATE UNIQUE INDEX video_revisions_request ON video_revisions (requested_by, request_id) WHERE request_id != '';
CREATE INDEX video_revisions_state ON video_revisions (state, created_at);

ALTER TABLE gateway_audio_providers ADD COLUMN voice TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE gateway_audio_providers DROP COLUMN voice;
DROP INDEX video_revisions_state;
DROP INDEX video_revisions_request;
DROP TABLE video_revisions;
DROP TABLE videos;
