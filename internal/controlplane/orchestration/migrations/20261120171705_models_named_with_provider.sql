-- Every model is named with its provider (issue #1002, owner 2026-11-20): a model stored without one is given, once,
-- the provider it resolves to today, before OpenCode Zen (#965) makes many names offered twice. A name this cannot
-- place for certain is left as it is, and is refused when it is next used, with the form to write it in.
--
-- A Task keeps its model as "<provider>/<model>" (orchestration modelRef). A Task's model is given, first, the provider
-- its latest Dispatch ran it under, read from the Dispatch's gateway name "<provider>/<model>"; else the one provider
-- that offers the name. A profile version, a Bot and a chat keep the provider beside the model: one that has none is
-- given the one provider that offers the name. Role routing already names a provider for each entry, and a Routine
-- runs with its Bot's model.

-- +goose Up
UPDATE tasks
SET model = (
    SELECT d.model FROM dispatches d
    WHERE d.task_id = tasks.id
      AND EXISTS (SELECT 1 FROM providers p WHERE d.model = p.name || '/' || tasks.model)
    ORDER BY d.created_at DESC, d.id DESC
    LIMIT 1
)
WHERE model IS NOT NULL AND model <> ''
  AND NOT EXISTS (SELECT 1 FROM providers p WHERE substr(tasks.model, 1, length(p.name) + 1) = p.name || '/')
  AND EXISTS (
    SELECT 1 FROM dispatches d
    WHERE d.task_id = tasks.id
      AND EXISTS (SELECT 1 FROM providers p WHERE d.model = p.name || '/' || tasks.model)
  );

UPDATE tasks
SET model = (SELECT m.provider || '/' || m.name FROM models m WHERE m.name = tasks.model)
WHERE model IS NOT NULL AND model <> ''
  AND NOT EXISTS (SELECT 1 FROM providers p WHERE substr(tasks.model, 1, length(p.name) + 1) = p.name || '/')
  AND (SELECT count(*) FROM models m WHERE m.name = tasks.model) = 1;

UPDATE profile_versions
SET provider = (SELECT m.provider FROM models m WHERE m.name = profile_versions.model)
WHERE model IS NOT NULL AND model <> '' AND (provider IS NULL OR provider = '')
  AND (SELECT count(*) FROM models m WHERE m.name = profile_versions.model) = 1;

UPDATE bots
SET provider = (SELECT m.provider FROM models m WHERE m.name = bots.model)
WHERE model IS NOT NULL AND model <> '' AND (provider IS NULL OR provider = '')
  AND (SELECT count(*) FROM models m WHERE m.name = bots.model) = 1;

UPDATE chats
SET provider = (SELECT m.provider FROM models m WHERE m.name = chats.model)
WHERE model IS NOT NULL AND model <> '' AND (provider IS NULL OR provider = '')
  AND (SELECT count(*) FROM models m WHERE m.name = chats.model) = 1;

-- +goose Down
-- The code before this migration reads a Task's model as a bare name: the provider is taken off again. A provider
-- given to a profile version, a Bot or a chat stays, which that code reads as well.
UPDATE tasks
SET model = substr(model, instr(model, '/') + 1)
WHERE model IS NOT NULL
  AND EXISTS (SELECT 1 FROM providers p WHERE substr(tasks.model, 1, length(p.name) + 1) = p.name || '/');
