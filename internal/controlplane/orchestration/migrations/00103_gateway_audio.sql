-- Speech-to-text and text-to-speech in the model gateway (issue #694). What the owner set for each audio provider on
-- Settings → Gateway → Audio, as the image providers are kept (00077): its one switch for both kinds, and its place
-- in the order speech-to-text and text-to-speech each ask the providers in (NULL: after those that have one, in the
-- default order Soniox, ElevenLabs). A provider with no row is on, in its default places. Read and written only by
-- the model gateway.
--
-- Each audio request is one usage row, as OmniRoute bills a transcription by its seconds and speech by its characters
-- (src/lib/usage/mediaRequestAccounting.ts:89-91, src/app/api/v1/audio/speech/route.ts:163): the milliseconds of the
-- recording transcribed and the characters spoken, 0 for every other request.

-- +goose Up
CREATE TABLE gateway_audio_providers (
    provider     TEXT    NOT NULL PRIMARY KEY CHECK (length(provider) > 0),
    off          INTEGER NOT NULL DEFAULT 0 CHECK (off IN (0, 1)),
    stt_position INTEGER,
    tts_position INTEGER,
    updated_at   INTEGER NOT NULL
) STRICT;

ALTER TABLE gateway_usage ADD COLUMN audio_ms INTEGER NOT NULL DEFAULT 0;
ALTER TABLE gateway_usage ADD COLUMN characters INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE gateway_usage DROP COLUMN characters;
ALTER TABLE gateway_usage DROP COLUMN audio_ms;
DROP TABLE gateway_audio_providers;
