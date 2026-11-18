-- The proxy a Connection's requests to its service go through (issue #850), as Metabase's database details carry
-- their own tunnel: a service in a private network is reached through a local SOCKS5 or HTTP proxy, such as a
-- userspace Tailscale's. NULL goes direct.

-- dial_address is where its requests are dialed instead of their host's, as curl's --resolve: a temporary way round
-- a name that does not resolve where the control plane runs. NULL dials the host.

-- +goose Up
ALTER TABLE connections ADD COLUMN proxy TEXT;
ALTER TABLE connections ADD COLUMN dial_address TEXT;

-- +goose Down
ALTER TABLE connections DROP COLUMN dial_address;
ALTER TABLE connections DROP COLUMN proxy;
