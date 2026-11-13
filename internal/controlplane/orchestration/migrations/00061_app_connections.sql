-- Each Connection of an Integration that is an app is an app of its own (issue #311), as each of OpenClaw's Feishu
-- accounts has its own appId and appSecret (extensions/feishu/src/config-schema.ts, FeishuAccountConfigSchema) and
-- Hermes has one app a profile. The app's id is here; its secret is the vault secret the Connection names.
--
-- Until now an app Integration had one app, its client (integration_clients), and each of its Connections named
-- the client's secret. Such a Connection keeps that secret, and takes the client's id as its app: it works as it
-- did, as the Lark bot became the Channel main with the same secret (00047). The client is then no longer the
-- Integration's: a Connection of an OAuth Integration names a secret of its own, so only an app's client is moved.

-- +goose Up
ALTER TABLE connections ADD COLUMN app_id TEXT;

UPDATE connections
SET app_id = (
    SELECT ic.client_id FROM integration_clients ic
    WHERE ic.integration = connections.integration AND ic.secret = connections.secret
)
WHERE EXISTS (
    SELECT 1 FROM integration_clients ic
    WHERE ic.integration = connections.integration AND ic.secret = connections.secret
);

DELETE FROM integration_clients
WHERE EXISTS (
    SELECT 1 FROM connections c
    WHERE c.integration = integration_clients.integration AND c.secret = integration_clients.secret
);

-- +goose Down
-- The way back has one app an Integration: the oldest Connection's.
INSERT OR IGNORE INTO integration_clients (integration, client_id, secret, created_at, updated_at)
SELECT c.integration, c.app_id, c.secret, c.created_at, c.updated_at FROM connections c
WHERE c.app_id IS NOT NULL
  AND NOT EXISTS (
      SELECT 1 FROM connections o
      WHERE o.integration = c.integration AND o.app_id IS NOT NULL
        AND (o.created_at < c.created_at OR (o.created_at = c.created_at AND o.label < c.label))
  );

ALTER TABLE connections DROP COLUMN app_id;
