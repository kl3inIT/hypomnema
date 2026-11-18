-- +goose Up
ALTER TABLE bots ADD COLUMN preset TEXT NOT NULL DEFAULT 'full' CHECK (preset IN ('full', 'report'));
ALTER TABLE profile_versions ADD COLUMN preset TEXT NOT NULL DEFAULT 'full' CHECK (preset IN ('full', 'report'));

-- Re-express the old allowlist as exceptions to full, without changing access to any existing tool.
CREATE TEMP TABLE upgraded_tool_policies (kind TEXT, id TEXT, tools TEXT, tools_off TEXT, PRIMARY KEY(kind, id));
INSERT INTO upgraded_tool_policies (kind, id, tools, tools_off)
WITH catalog(tool, pattern) AS (VALUES
    ('memory','memory'), ('show_artifact','show_artifact'), ('routines','routines'),
    ('web_search','web_*'), ('web_fetch','web_*'), ('generate_image','generate_image'),
    ('audio_transcribe','audio_*'), ('audio_speak','audio_*'),
    ('muse_ask','muse_*'), ('muse_chats','muse_*'), ('muse_messages','muse_*'),
    ('superset_dashboards','superset_*'), ('superset_dashboard','superset_*'), ('superset_chart_data','superset_*'),
    ('superset_filter_values','superset_*'), ('superset_api','superset_*'),
    ('whatsapp_chats','whatsapp_*'), ('whatsapp_messages','whatsapp_*'), ('whatsapp_media','whatsapp_*'),
    ('whatsapp_history','whatsapp_*'), ('whatsapp_send','whatsapp_*'),
    ('zalo_chats','zalo_*'), ('zalo_messages','zalo_*'), ('zalo_media','zalo_*'), ('zalo_history','zalo_*'),
    ('zalo_send','zalo_*'), ('node_exec','node_*'), ('node_process','node_*'), ('node_process_kill','node_*'),
    ('agent_browser_open','agent_browser_*'), ('agent_browser_snapshot','agent_browser_*'),
    ('agent_browser_click','agent_browser_*'), ('agent_browser_fill','agent_browser_*'),
    ('agent_browser_type','agent_browser_*'), ('agent_browser_press','agent_browser_*'),
    ('agent_browser_check','agent_browser_*'), ('agent_browser_uncheck','agent_browser_*'),
    ('agent_browser_select','agent_browser_*'), ('agent_browser_scroll','agent_browser_*'),
    ('agent_browser_back','agent_browser_*'), ('agent_browser_forward','agent_browser_*'),
    ('agent_browser_reload','agent_browser_*'), ('agent_browser_wait_ms','agent_browser_*'),
    ('agent_browser_wait_for_selector','agent_browser_*'), ('agent_browser_wait_for_text','agent_browser_*'),
    ('agent_browser_wait_for_load','agent_browser_*'), ('agent_browser_screenshot','agent_browser_*'),
    ('agent_browser_get_text','agent_browser_*'), ('agent_browser_get_url','agent_browser_*'),
    ('agent_browser_get_title','agent_browser_*'), ('agent_browser_eval','agent_browser_*')
), policies(kind, id, tools, tools_off) AS (
    SELECT 'bot', id, tools, tools_off FROM bots
    UNION ALL SELECT 'profile', id, tools, tools_off FROM profile_versions
    UNION ALL SELECT 'undo', id,
        coalesce((SELECT group_concat(value) FROM json_each(setup_changes.before, '$.tools')), ''),
        coalesce((SELECT group_concat(value) FROM json_each(setup_changes.before, '$.tools_off')), '')
    FROM setup_changes WHERE kind = 'bot' AND before IS NOT NULL
), excluded AS (
    SELECT kind, id, tool, pattern FROM policies CROSS JOIN catalog
    WHERE tools <> '' AND NOT EXISTS (
        SELECT 1 FROM json_each('[' || replace(json_quote(tools), ',', '","') || ']') AS entry
        WHERE entry.value = tool OR entry.value = pattern
          OR (substr(entry.value, -1) = '*' AND instr(tool, substr(entry.value, 1, length(entry.value) - 1)) = 1)
    )
), denied_families AS (
    SELECT kind, id, pattern FROM excluded GROUP BY kind, id, pattern
    HAVING count(*) = (SELECT count(*) FROM catalog WHERE catalog.pattern = excluded.pattern)
), denials AS (
    SELECT kind, id, pattern AS entry FROM denied_families
    UNION ALL
    SELECT kind, id, tool FROM excluded WHERE NOT EXISTS (
        SELECT 1 FROM denied_families AS whole
        WHERE whole.kind = excluded.kind AND whole.id = excluded.id AND whole.pattern = excluded.pattern
    )
)
SELECT kind, id, iif(tools = '', iif(
    NOT EXISTS (SELECT 1 FROM nodes) AND NOT EXISTS (SELECT 1 FROM dispatches) AND (
        (kind = 'bot' AND id = 'bot_mainaaaaaaaaaaaaaaaaaaaaaa' AND EXISTS (
            SELECT 1 FROM bots WHERE bots.id = policies.id AND updated_at = 0
        )) OR (kind = 'profile' AND id LIKE 'pfv_seed%')
    ), '', 'device_*'), tools) AS tools,
    coalesce((SELECT group_concat(value) FROM (
        SELECT value FROM json_each('[' || replace(json_quote(tools_off), ',', '","') || ']') WHERE value <> ''
        UNION
        SELECT entry FROM denials
        WHERE denials.kind = policies.kind AND denials.id = policies.id
        ORDER BY value
    )), '') AS tools_off
FROM policies;

UPDATE bots SET
    tools = (SELECT tools FROM upgraded_tool_policies WHERE kind = 'bot' AND id = bots.id),
    tools_off = (SELECT tools_off FROM upgraded_tool_policies WHERE kind = 'bot' AND id = bots.id);
UPDATE profile_versions SET
    tools = (SELECT tools FROM upgraded_tool_policies WHERE kind = 'profile' AND id = profile_versions.id),
    tools_off = (SELECT tools_off FROM upgraded_tool_policies WHERE kind = 'profile' AND id = profile_versions.id);
UPDATE setup_changes SET before = (
    SELECT json_set(setup_changes.before, '$.preset', 'full',
        '$.tools', json(iif(upgrade.tools = '', '[]', '[' || replace(json_quote(upgrade.tools), ',', '","') || ']')),
        '$.tools_off', json(iif(upgrade.tools_off = '', '[]',
            '[' || replace(json_quote(upgrade.tools_off), ',', '","') || ']')))
    FROM upgraded_tool_policies AS upgrade WHERE upgrade.kind = 'undo' AND upgrade.id = setup_changes.id
) WHERE kind = 'bot' AND before IS NOT NULL;
DROP TABLE upgraded_tool_policies;

-- +goose Down
ALTER TABLE profile_versions DROP COLUMN preset;
ALTER TABLE bots DROP COLUMN preset;
