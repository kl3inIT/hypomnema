-- +goose Up
-- The SSH address a registered repository is cloned and fetched from (issue #625), such as
-- ssh://git@gitlab.company.example:222/team/app.git; NULL clones it over HTTPS with the host's token, as before.
-- The node authenticates with the control plane's own SSH key, kept in the vault and never here.
ALTER TABLE repos ADD COLUMN ssh_url TEXT;
-- The SSH host's public keys, one "<type> <base64>" a line, that the owner confirmed: the node trusts that host
-- under them alone (StrictHostKeyChecking=yes). Set whenever ssh_url is.
ALTER TABLE repos ADD COLUMN ssh_host_key TEXT;

-- +goose Down
ALTER TABLE repos DROP COLUMN ssh_host_key;
ALTER TABLE repos DROP COLUMN ssh_url;
