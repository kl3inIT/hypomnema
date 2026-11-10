-- +goose Up
-- What a node has of the tools the control plane told it to have (issue #103), as a JSON array of
-- {name, version, error}. NULL is a node that has not said.
ALTER TABLE nodes ADD COLUMN tools TEXT;

-- +goose Down
ALTER TABLE nodes DROP COLUMN tools;
