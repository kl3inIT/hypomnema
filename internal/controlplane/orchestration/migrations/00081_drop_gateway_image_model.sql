-- Each image provider makes images with one model, not chosen (issue #453, owner, 2026-11-14): Codex gpt-image-2,
-- Antigravity gemini-3.1-flash-image, as codex-rs's image extension (IMAGE_MODEL) and Hermes (API_MODEL) send one.
-- The Codex backend answered every model name with an image, a made-up one too, so the model the owner set on
-- Settings → Gateway → Images (00077) changed nothing; it goes.

-- +goose Up
ALTER TABLE gateway_image_providers DROP COLUMN model;

-- +goose Down
ALTER TABLE gateway_image_providers ADD COLUMN model TEXT CHECK (model IS NULL OR length(model) > 0);
