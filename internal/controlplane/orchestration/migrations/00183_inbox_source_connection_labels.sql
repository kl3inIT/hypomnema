-- +goose Up
-- Repair a saved account ID only when it is not already a label and identifies exactly one Connection.
-- For Zalo and WhatsApp, compare the account user as agentapi.sameAccount does: without a JID suffix,
-- device number, spaces or a leading +. Compare other service IDs in full, including Gmail domains.
WITH candidates AS (
    SELECT t.id, t.source_account,
           CASE t.source_kind WHEN 'gmail' THEN 'google' ELSE t.source_kind END AS integration
    FROM todos AS t
    WHERE t.source_kind IN ('zalo', 'whatsapp', 'lark', 'gmail') AND t.source_account <> ''
),
names AS (
    SELECT id, integration, NULL AS label, source_account AS account
    FROM candidates
    WHERE NOT EXISTS (
        SELECT 1 FROM connections AS c WHERE c.integration = candidates.integration AND c.label = candidates.source_account
    )
    UNION ALL
    SELECT NULL, integration, label, account
    FROM connections
    WHERE integration IN ('zalo', 'whatsapp', 'lark', 'google') AND account <> ''
),
parts AS (
    SELECT id, integration, label, account,
           replace(trim(substr(account, 1, min(instr(account || '@', '@'), instr(account || ':', ':')) - 1),
                        char(9, 10, 11, 12, 13, 32)), ' ', '') AS user
    FROM names
),
keys AS (
    SELECT id, integration, label,
           CASE
               WHEN integration NOT IN ('zalo', 'whatsapp') THEN account
               WHEN substr(user, 1, 1) = '+' THEN substr(user, 2)
               ELSE user
           END AS account_key
    FROM parts
),
matches AS (
    SELECT item.id, min(conn.label) AS label
    FROM keys AS item
    JOIN keys AS conn ON conn.integration = item.integration AND conn.account_key = item.account_key
    WHERE item.id IS NOT NULL AND conn.label IS NOT NULL AND item.account_key <> ''
    GROUP BY item.id
    HAVING count(*) = 1
)
UPDATE todos
SET source_account = (SELECT matches.label FROM matches WHERE matches.id = todos.id)
WHERE id IN (SELECT id FROM matches);

-- +goose Down
-- Corrected labels remain valid on the previous schema.
SELECT 1;
