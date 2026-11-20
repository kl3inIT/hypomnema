-- The look of each video revision (issue #908, rows 1, 4 and 15; #937): its caption preset and position, the frame
-- drawn around the phone's picture and the background it stands on, as JSON, always the resolved snapshot the
-- revision is made with. Every revision made before keeps the captions it had and no frame ("legacy" + "full"),
-- queued ones included, so a revision queued before this migration is made as it was asked. pin_provider and
-- pin_voice pin the voice of a revision made from another (a look change, a retry): only that provider is asked,
-- with exactly that voice, even an empty one (its own default); an empty pin_provider pins nothing.
--
-- video_settings is the look a new video gets when its make sends none (one row, as meeting_settings): Clean
-- captions, an Android frame, the midnight background, until the owner's makes change it.

-- +goose Up
ALTER TABLE video_revisions ADD COLUMN look TEXT NOT NULL
    DEFAULT '{"captions":{"preset":"legacy"},"frame":{"chrome":"full"}}';
ALTER TABLE video_revisions ADD COLUMN pin_provider TEXT NOT NULL DEFAULT '';
ALTER TABLE video_revisions ADD COLUMN pin_voice TEXT NOT NULL DEFAULT '';

CREATE TABLE video_settings (
    id         INTEGER NOT NULL PRIMARY KEY CHECK (id = 1),
    look       TEXT    NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;

INSERT INTO video_settings (id, look, updated_at)
VALUES (1, '{"captions":{"preset":"clean","position":"preset"},' ||
           '"frame":{"chrome":"android","padding":48,"radius":40,"shadow":true,"safe_top":false},' ||
           '"background":{"preset":"midnight"}}', 0);

-- +goose Down
DROP TABLE video_settings;
ALTER TABLE video_revisions DROP COLUMN pin_voice;
ALTER TABLE video_revisions DROP COLUMN pin_provider;
ALTER TABLE video_revisions DROP COLUMN look;
