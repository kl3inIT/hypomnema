-- The model and effort a chat runs with (issue #369), as a t3code thread, an Orca session and an Omnigent session
-- keep their own: chosen in the chat's composer, or on the New chat page. Empty is the Bot's: a chat starts with its
-- Bot's model and effort, and the Bot's page keeps the default. The provider is where the model is found, an id in
-- pi's models.json, as a Bot's is.

-- +goose Up
ALTER TABLE chats ADD COLUMN provider TEXT;
ALTER TABLE chats ADD COLUMN model TEXT;
ALTER TABLE chats ADD COLUMN effort TEXT;

-- +goose Down
ALTER TABLE chats DROP COLUMN effort;
ALTER TABLE chats DROP COLUMN model;
ALTER TABLE chats DROP COLUMN provider;
