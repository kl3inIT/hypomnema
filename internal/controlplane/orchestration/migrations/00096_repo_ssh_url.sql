-- +goose Up
-- The SSH address a registered repository is cloned and fetched from (issue #625), such as
-- ssh://git@gitlab.company.example:222/team/app.git; NULL clones it over HTTPS with the host's token, as before.
-- The node authenticates with the control plane's own SSH key, kept in the vault and never here.
ALTER TABLE repos ADD COLUMN ssh_url TEXT;

-- +goose Down
ALTER TABLE repos DROP COLUMN ssh_url;
