-- The model each audio provider is asked with for speech-to-text and for text-to-speech, as the owner chose it on
-- Settings → Gateway → Audio (issue #1285, row 1; pipecat and LiveKit take the model as a setting). Empty: the
-- provider's newest model, as the code names it (speech.DefaultModel). Read and written only by the model gateway.
--
-- The speed a video's revision is spoken at (issue #1285, row 3; the owner, 2026-11-27: a speed for each video),
-- from 0.7 to 1.3 as Soniox takes it; 0: the provider's own. A revision made from another keeps it.

-- +goose Up
ALTER TABLE gateway_audio_providers ADD COLUMN stt_model TEXT NOT NULL DEFAULT '';
ALTER TABLE gateway_audio_providers ADD COLUMN tts_model TEXT NOT NULL DEFAULT '';
ALTER TABLE video_revisions ADD COLUMN speed REAL NOT NULL DEFAULT 0 CHECK (speed = 0 OR speed BETWEEN 0.7 AND 1.3);

-- +goose Down
ALTER TABLE video_revisions DROP COLUMN speed;
ALTER TABLE gateway_audio_providers DROP COLUMN tts_model;
ALTER TABLE gateway_audio_providers DROP COLUMN stt_model;
