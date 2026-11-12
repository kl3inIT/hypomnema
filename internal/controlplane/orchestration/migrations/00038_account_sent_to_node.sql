-- +goose Up
-- An account whose token the control plane keeps and sends to the node with each Dispatch (issue #150): the token
-- of `claude setup-token`. An account logged in on the control plane for the gateway also names a secret, and is
-- not sent; this column tells the two apart.
ALTER TABLE accounts ADD COLUMN sent_to_node INTEGER NOT NULL DEFAULT 0 CHECK (sent_to_node IN (0, 1));

-- +goose Down
ALTER TABLE accounts DROP COLUMN sent_to_node;
