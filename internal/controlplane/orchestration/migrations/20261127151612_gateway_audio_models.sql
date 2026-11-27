-- The model each audio provider is asked with for speech-to-text and for text-to-speech, as the owner chose it on
-- Settings → Gateway → Audio (issue #1285, row 1; pipecat and LiveKit take the model as a setting). Empty: the
-- provider's newest model, as the code names it (speech.DefaultModel). Read and written only by the model gateway.

-- +goose Up
ALTER TABLE gateway_audio_providers ADD COLUMN stt_model TEXT NOT NULL DEFAULT '';
ALTER TABLE gateway_audio_providers ADD COLUMN tts_model TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE gateway_audio_providers DROP COLUMN tts_model;
ALTER TABLE gateway_audio_providers DROP COLUMN stt_model;
