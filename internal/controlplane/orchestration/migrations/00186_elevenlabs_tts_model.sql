-- ElevenLabs on a model the owner chooses (issue #1283, row 1). tts_model is the text-to-speech model an audio
-- provider speaks with, chosen on Settings -> Gateway -> Audio from the provider's own list; empty: its default
-- (eleven_v4 for ElevenLabs). Soniox has one model and never gets one.
--
-- pin_model pins the model of a revision made from another (a look change, a retry), beside pin_provider and
-- pin_voice (00152): the voice it was spoken with is spoken by the same model, so its clips are reused; empty: the
-- provider's default, as for a pin made before this migration.

-- +goose Up
ALTER TABLE gateway_audio_providers ADD COLUMN tts_model TEXT NOT NULL DEFAULT '';
ALTER TABLE video_revisions ADD COLUMN pin_model TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE video_revisions DROP COLUMN pin_model;
ALTER TABLE gateway_audio_providers DROP COLUMN tts_model;
