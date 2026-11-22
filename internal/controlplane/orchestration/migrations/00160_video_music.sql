-- The owner's music library for videos (issue #910, rows 6a and 6d): each track the owner uploaded, its file in
-- videos/music/ beside the database named by its id, immutable. media_type is the type ffprobe found and the track is
-- served as; length_ms is its decoded length; bytes is its file's size. A revision's look names a track as a snapshot
-- (look.music), so no foreign key: a deleted track's made revisions keep its name and length.

-- +goose Up
CREATE TABLE video_music (
    id         TEXT    NOT NULL PRIMARY KEY,
    name       TEXT    NOT NULL,
    file       TEXT    NOT NULL,
    media_type TEXT    NOT NULL,
    length_ms  INTEGER NOT NULL,
    bytes      INTEGER NOT NULL,
    created_at INTEGER NOT NULL
) STRICT;

-- +goose Down
DROP TABLE video_music;
