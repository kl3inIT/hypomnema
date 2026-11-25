-- name: InsertChat :exec
INSERT INTO chats (id, title, bot_id, provider, model, effort, created_at, updated_at)
VALUES (@id, @title, @bot, @provider, @model, @effort, @now, @now);

-- name: SetChatModel :exec
-- The chat's own model and effort (issue #369); NULL is the Bot's.
UPDATE chats SET provider = @provider, model = @model, effort = @effort WHERE id = @id;

-- name: SetChatContextUsage :exec
-- pi's last report of the chat's context window (issue #384); the chat's updated_at stays, as it orders the list by
-- what was said.
UPDATE chats SET context_tokens = @tokens, context_window = @context_window, context_percent = @percent
WHERE id = @id;

-- name: GetChat :one
SELECT * FROM chats WHERE id = @id;

-- name: GetChatMarks :one
-- What keeps a chat from being its Bot's main chat (issue #923), as OpenMausBot's main thread is never a routine's
-- run nor a Slack channel's thread (server/main-thread.ts mainThread, server/index.ts mainThreadTasks). routine: the
-- chat was opened for a Routine's run (routine_runs), or with a Routine's prompt, which runs pruned later leave
-- behind (orchestration/routines.go promptNotice). channel: a Lark chat, mapped to a person or a group of a Channel
-- (channel_chats), a group's (channel_group, which a group's later chats keep after a reset), one someone other
-- than the owner wrote in (sender_id, migration 00087), or another person's direct chat from before 00087 whose
-- mapping a /new moved: titled "Lark: <name>", as migration 00138 tells them, with a line of the owner's role that
-- opens with the label the Channel put on another person's direct message then, "[Lark message from <name>, who is
-- not the owner. ...]" (channels/channels.go before commit 49050bf6, unchanged since issue #136). The label is asked
-- too, since the web titles a chat by its first line and the owner may begin one with "Lark: ". The label is
-- compared to the letter, not with LIKE, so that a % or an _ in the name is no wildcard. ListChats reads the same marks by the same rules.
SELECT
    CAST(EXISTS (SELECT 1 FROM routine_runs rr WHERE rr.chat_id = c.id)
        OR EXISTS (
            SELECT 1 FROM chat_messages m
            WHERE m.chat_id = c.id AND m.kind = 'wake' AND m.event_seq IS NULL AND m.to_coordinator = 1
              AND m.text LIKE 'Routine %'
        ) AS INTEGER) AS routine,
    CAST(c.channel_group <> 0
        OR EXISTS (SELECT 1 FROM channel_chats cc WHERE cc.chat_id = c.id)
        OR EXISTS (
            SELECT 1 FROM chat_messages m WHERE m.chat_id = c.id AND m.role = 'owner' AND m.sender_id IS NOT NULL
        )
        OR (c.title LIKE 'Lark: %' AND EXISTS (
            SELECT 1 FROM chat_messages m
            WHERE m.chat_id = c.id AND m.role = 'owner' AND m.sender_id IS NULL
              AND substr(m.text, 1, length('[Lark message from ' || substr(c.title, 7) || ', who is not the owner.'))
                  = '[Lark message from ' || substr(c.title, 7) || ', who is not the owner.'
        )) AS INTEGER) AS channel
FROM chats c WHERE c.id = @id;

-- name: ListChats :many
-- A page of the chats, of one Bot when @bot names one, the most recently active first, each with the start of its
-- latest line of the owner or its Bot (issue #471): the sidebar's row of a Bot shows its main chat's. A reply of
-- thinking alone has no text and is passed over. The first page (@skip 0) also carries every Bot's main chat, every
-- chat an Approval or a Connection request (issue #471, part D) waits in and, of a Bot with no main chat, the chat
-- it opens (issue #923: its latest that is not a Routine's run), whatever their age, so that a Bot's row is whole: a
-- page later on does not. Each chat carries the marks GetChatMarks reads, by the same rules.
SELECT sqlc.embed(chats),
       CAST(EXISTS (SELECT 1 FROM routine_runs rr WHERE rr.chat_id = chats.id)
           OR EXISTS (
               SELECT 1 FROM chat_messages m
               WHERE m.chat_id = chats.id AND m.kind = 'wake' AND m.event_seq IS NULL AND m.to_coordinator = 1
                 AND m.text LIKE 'Routine %'
           ) AS INTEGER) AS routine,
       CAST(chats.channel_group <> 0
           OR EXISTS (SELECT 1 FROM channel_chats cc WHERE cc.chat_id = chats.id)
           OR EXISTS (
               SELECT 1 FROM chat_messages m
               WHERE m.chat_id = chats.id AND m.role = 'owner' AND m.sender_id IS NOT NULL
           )
           OR (chats.title LIKE 'Lark: %' AND EXISTS (
               SELECT 1 FROM chat_messages m
               WHERE m.chat_id = chats.id AND m.role = 'owner' AND m.sender_id IS NULL
                 AND substr(m.text, 1, length('[Lark message from ' || substr(chats.title, 7) || ', who is not the owner.'))
                     = '[Lark message from ' || substr(chats.title, 7) || ', who is not the owner.'
           )) AS INTEGER) AS channel,
       CAST(coalesce((
           SELECT substr(m.text, 1, 400) FROM chat_messages m
           WHERE m.chat_id = chats.id AND m.role IN ('owner', 'coordinator') AND trim(m.text) <> ''
           ORDER BY m.seq DESC LIMIT 1
       ), '') AS TEXT) AS last_text
FROM chats
WHERE (CAST(@bot AS TEXT) = '' OR chats.bot_id = CAST(@bot AS TEXT))
  AND (
    chats.id IN (
        SELECT p.id FROM chats p
        WHERE CAST(@bot AS TEXT) = '' OR p.bot_id = CAST(@bot AS TEXT)
        ORDER BY p.updated_at DESC, p.id LIMIT @lim OFFSET @skip
    )
    OR (CAST(@skip AS INTEGER) = 0 AND (
        chats.id IN (SELECT b.main_chat FROM bots b WHERE b.main_chat IS NOT NULL)
        OR chats.id IN (SELECT a.chat_id FROM approvals a WHERE a.status = 'pending' AND a.chat_id IS NOT NULL)
        OR chats.id IN (SELECT r.chat_id FROM connection_requests r WHERE r.state = 'pending')
        OR chats.id IN (
            SELECT (
                SELECT l.id FROM chats l
                WHERE l.bot_id = b.id
                  AND NOT EXISTS (SELECT 1 FROM routine_runs rr WHERE rr.chat_id = l.id)
                  AND NOT EXISTS (
                      SELECT 1 FROM chat_messages m
                      WHERE m.chat_id = l.id AND m.kind = 'wake' AND m.event_seq IS NULL AND m.to_coordinator = 1
                        AND m.text LIKE 'Routine %'
                  )
                ORDER BY l.updated_at DESC, l.id LIMIT 1
            )
            FROM bots b WHERE b.main_chat IS NULL
        )
    ))
  )
ORDER BY chats.updated_at DESC, chats.id;

-- name: SetBotMainChat :exec
-- The chat becomes its Bot's main chat (issue #471): the web's New chat, the CLI's --new, the first message to a
-- Bot that has none, or the owner's Set as main chat (issue #923).
UPDATE bots SET main_chat = @chat_id WHERE id = @bot;

-- name: ClearMainChat :exec
-- A deleted chat is no Bot's main chat any more; the next message from the web or the CLI makes another.
UPDATE bots SET main_chat = NULL WHERE main_chat = CAST(@chat_id AS TEXT);

-- name: TouchChat :exec
UPDATE chats SET updated_at = @now WHERE id = @id;

-- name: InsertRunAdoptionNotice :exec
INSERT INTO chat_messages (chat_id, role, kind, text, to_coordinator, created_at)
VALUES (@chat_id, 'system', 'wake', @text, 1, @now);

-- name: InsertChatMessage :one
INSERT INTO chat_messages (chat_id, role, kind, text, parts, attachments, event_seq, sender_id, sender_name, created_at)
VALUES (@chat_id, @role, @kind, @text, @parts, @attachments, @event_seq, @sender_id, @sender_name, @now)
RETURNING seq;

-- name: LastTurnInput :one
-- The chat's last message of those pi is given as a turn (UndeliveredChatMessages says which): a person's message,
-- a wake-up or an answered Approval. Whether it is delivered yet does not matter: what came after the owner's
-- message, given to pi or about to be, ends his turn (issue #468). The line that wakes pi to continue a turn a
-- restart cut is no input of its own: the continued turn is the cut one's (issue #716).
SELECT role, sender_id FROM chat_messages
WHERE chat_id = @chat_id AND (role = 'owner' OR event_seq IS NOT NULL OR to_coordinator = 1) AND kind <> 'continued'
ORDER BY seq DESC
LIMIT 1;

-- name: IsReadRun :one
SELECT EXISTS (SELECT 1 FROM chat_messages WHERE chat_id = chats.id AND read_run = 1)
FROM chats WHERE id = @chat_id;

-- name: ListChatMessages :many
-- Each wake-up with the event it reports (issue #303): the event never changes, so it is read here rather than kept
-- on the row. Other rows have empty event columns. An answered Approval's row is read with its Approval, which no
-- longer changes once answered (issue #465). A page of older history is the rows between ChatPageStart and @before
-- (issue #502).
SELECT sqlc.embed(chat_messages),
       CAST(coalesce(events.kind, '') AS TEXT)        AS event_kind,
       CAST(coalesce(events.run_id, '') AS TEXT)      AS event_run_id,
       CAST(coalesce(events.task_id, '') AS TEXT)     AS event_task_id,
       CAST(coalesce(events.question_id, '') AS TEXT) AS event_question_id,
       CAST(coalesce(events.payload, '') AS TEXT)     AS event_payload,
       CAST(coalesce(artifacts.id, '') AS TEXT)       AS artifact_id,
       CAST(coalesce(artifacts.revision, 0) AS INTEGER) AS artifact_revision,
       CAST(coalesce(artifacts.updated_at, 0) AS INTEGER) AS artifact_updated_at,
       CAST(coalesce(artifact_revisions.title, '') AS TEXT) AS artifact_title,
       CAST(coalesce(artifact_revisions.type, '') AS TEXT)  AS artifact_type,
       CAST(coalesce(approvals.status, '') AS TEXT)      AS approval_status,
       CAST(coalesce(approvals.scope, '') AS TEXT)       AS approval_scope,
       CAST(coalesce(approvals.standing, 0) AS INTEGER)  AS approval_standing,
       CAST(coalesce(approvals.title, '') AS TEXT)       AS approval_title,
       CAST(coalesce(approvals.action_name, '') AS TEXT) AS approval_action_name,
       CAST(coalesce(approvals.decided_at, 0) AS INTEGER) AS approval_decided_at
FROM chat_messages
LEFT JOIN events ON chat_messages.kind = 'wake' AND events.seq = chat_messages.event_seq
LEFT JOIN artifacts ON chat_messages.kind = 'artifact' AND artifacts.message_seq = chat_messages.seq
LEFT JOIN artifact_revisions ON artifact_revisions.artifact_id = artifacts.id
    AND artifact_revisions.revision = artifacts.revision
LEFT JOIN approvals ON chat_messages.kind = 'approval' AND approvals.id = chat_messages.approval_id
WHERE chat_messages.chat_id = @chat_id AND chat_messages.seq > @after AND chat_messages.seq < @before
ORDER BY chat_messages.seq
LIMIT @lim;

-- name: ChatPageStart :one
-- The seq a page of the newest @skip rows below @before starts after: the next older row, which also says that
-- more history is left (issue #502). None when the page reaches the chat's first message.
SELECT seq FROM chat_messages WHERE chat_id = @chat_id AND seq < @before ORDER BY seq DESC LIMIT 1 OFFSET @skip;

-- name: ListChatArtifactMessages :many
-- A chat's artifacts' messages with what each shows now (issue #402): a chat's stream sends one again when its
-- artifact was replaced, as the message is updated in place.
SELECT sqlc.embed(chat_messages),
       artifacts.id         AS artifact_id,
       artifacts.revision   AS artifact_revision,
       artifacts.updated_at AS artifact_updated_at,
       artifact_revisions.title AS artifact_title,
       artifact_revisions.type  AS artifact_type
FROM artifacts
JOIN chat_messages ON chat_messages.seq = artifacts.message_seq
JOIN artifact_revisions ON artifact_revisions.artifact_id = artifacts.id
    AND artifact_revisions.revision = artifacts.revision
WHERE artifacts.chat_id = @chat_id
ORDER BY chat_messages.seq;

-- name: UndeliveredChatMessages :many
-- Owner messages and wake-ups not yet given to pi, oldest first.
SELECT * FROM chat_messages
WHERE chat_id = @chat_id AND delivered_at IS NULL AND (role = 'owner' OR event_seq IS NOT NULL OR to_coordinator = 1)
ORDER BY seq;

-- name: ChatsWithUndelivered :many
SELECT DISTINCT chat_id FROM chat_messages
WHERE delivered_at IS NULL AND (role = 'owner' OR event_seq IS NOT NULL OR to_coordinator = 1);

-- name: MarkChatMessagesDelivered :exec
UPDATE chat_messages SET delivered_at = @now
WHERE chat_id = @chat_id AND seq <= @upto AND delivered_at IS NULL AND (role = 'owner' OR event_seq IS NOT NULL OR to_coordinator = 1);

-- name: ChatWokenAfter :one
-- The last event this chat was woken for.
SELECT CAST(coalesce(max(event_seq), 0) AS INTEGER) AS woken FROM chat_messages WHERE chat_id = @chat_id;

-- name: ChatsWithRuns :many
SELECT DISTINCT chat_id FROM runs WHERE chat_id IS NOT NULL;

-- name: ChatRunEvents :many
-- Events of the chat's Runs after a cursor that need a coordinator (wakeText
-- in chats.go says the same), so events that wake nobody never fill the page.
SELECT e.* FROM events e JOIN runs r ON r.id = e.run_id
WHERE r.chat_id = @chat_id AND e.seq > @after AND e.seq > r.wake_after
  AND (
    e.kind IN ('escalation', 'run.finished', 'run.canceled')
    OR (e.kind = 'task.status'
        AND json_extract(e.payload, '$.to') IN ('succeeded', 'failed', 'blocked', 'canceled'))
    OR (e.kind = 'question.opened' AND json_extract(e.payload, '$.addressed_to') = 'coordinator')
  )
ORDER BY e.seq
LIMIT @lim;

-- name: TitleUntitledChat :exec
-- A chat still under the default title takes one from its first owner message.
UPDATE chats SET title = @title WHERE id = @id AND title = 'Chat';

-- name: InsertChatApproval :exec
-- The owner answered an Approval asked in this chat (issue #465): the row stays where the answer was given. pi is
-- given it only once TellChatOfApproval marks it for the coordinator.
INSERT INTO chat_messages (chat_id, role, kind, text, approval_id, created_at)
VALUES (@chat_id, 'system', 'approval', @text, @approval_id, @now);

-- name: TellChatOfApproval :execrows
-- The coordinator is given an answered Approval's row as a wake-up, though no event of a Run is behind it (issue
-- #154): its request had ended, so it must run the command again. Once only.
UPDATE chat_messages SET to_coordinator = 1 WHERE approval_id = @approval_id AND to_coordinator = 0;

-- name: CountChatsOfBot :one
SELECT count(*) FROM chats WHERE bot_id = @bot;

-- name: ListChatIDsOfBot :many
SELECT id FROM chats WHERE bot_id = @bot ORDER BY id;

-- A chat deleted (issue #484), on its own or with its Bot (issue #330): what refers to it is let go of or removed
-- first, in this order. Runs, Routines, their runs (routines.sql) and approvals keep their rows and lose the chat; its gateway keys
-- are revoked and kept, so the gateway's usage records stay whole; its artifacts go with their revisions.

-- name: DetachRunsFromChat :exec
UPDATE runs SET chat_id = NULL WHERE chat_id = CAST(@chat_id AS TEXT);

-- name: DetachApprovalsFromChat :exec
UPDATE approvals SET chat_id = NULL WHERE chat_id = CAST(@chat_id AS TEXT);

-- name: DeleteSetupChangesOfChat :exec
DELETE FROM setup_changes WHERE chat_id = @chat_id;

-- name: ListArtifactBlobsOfChat :many
-- The bytes the chat's artifacts keep: those no other artifact keeps go after them.
SELECT DISTINCT f.sha256 FROM artifact_files f JOIN artifacts a ON a.id = f.artifact_id WHERE a.chat_id = @chat_id;

-- name: DeleteArtifactsOfChat :exec
-- Their revisions and files go with them (ON DELETE CASCADE).
DELETE FROM artifacts WHERE chat_id = @chat_id;

-- name: DeleteChatMessages :exec
DELETE FROM chat_messages WHERE chat_id = @chat_id;

-- name: DeleteChat :exec
DELETE FROM chats WHERE id = @id;

-- name: InsertChatQueued :one
INSERT INTO chat_queue (chat_id, text, attachments, sender_id, sender_name, created_at)
VALUES (@chat_id, @text, @attachments, @sender_id, @sender_name, @now)
RETURNING *;

-- name: ListChatQueue :many
SELECT * FROM chat_queue WHERE chat_id = @chat_id ORDER BY id;

-- name: GetChatQueued :one
SELECT * FROM chat_queue WHERE chat_id = @chat_id AND id = @id;

-- name: DeleteChatQueued :execrows
DELETE FROM chat_queue WHERE chat_id = @chat_id AND id = @id;

-- name: ChatsWithQueue :many
SELECT DISTINCT chat_id FROM chat_queue;

-- name: DeleteChatQueue :exec
DELETE FROM chat_queue WHERE chat_id = @chat_id;

-- name: MarkChatMessageDelivered :exec
UPDATE chat_messages SET delivered_at = @now WHERE chat_id = @chat_id AND seq = @seq AND delivered_at IS NULL;

-- name: CountChatQueue :one
SELECT count(*) FROM chat_queue WHERE chat_id = @chat_id;

-- name: MarkChatTurn :exec
-- A Bot's turn started in the chat (issue #716), in the pi process the token names; the attempts stay, so a
-- continued turn keeps its count. A new turn is not cut yet (issue #1162).
UPDATE chats SET turn_started_at = @now, turn_token = @token, turn_cut_at = NULL WHERE id = @id;

-- name: SettleChatTurn :exec
-- The turn of the pi process the token names ended: no mark, no attempts. A newer turn's mark, of another pi
-- process, stays (Hermes's compare-and-swap clear).
UPDATE chats SET turn_started_at = NULL, turn_token = NULL, turn_attempts = 0, turn_cut_at = NULL
WHERE id = @id AND turn_token = @token;

-- name: ClearChatTurn :exec
-- The chat's cut turn is not continued: no mark, no attempts, whoever marked it.
UPDATE chats SET turn_started_at = NULL, turn_token = NULL, turn_attempts = 0, turn_cut_at = NULL WHERE id = @id;

-- name: CutChatTurns :many
-- The chats whose turn was marked and never settled: at start, the turns a restart cut.
SELECT id, CAST(turn_started_at AS INTEGER) AS turn_started_at, turn_cut_at, turn_attempts FROM chats
WHERE turn_started_at IS NOT NULL
ORDER BY id;

-- name: CutMarkedTurns :exec
-- The control plane begins to stop (issue #1162): every marked turn is cut now, written before the wait for the
-- turns, as Hermes's gateway marks its running sessions before its drain (gateway/run_shutdown.py). A later write
-- of the same stop moves the cut to its own time, as Hermes marks again when the drain times out.
UPDATE chats SET turn_cut_at = @now WHERE turn_started_at IS NOT NULL;

-- name: ContinueChatTurn :exec
-- The chat is woken to continue its cut turn: one attempt more, and the mark starts again now, no pi's yet and not
-- cut.
UPDATE chats SET turn_attempts = turn_attempts + 1, turn_started_at = @now, turn_token = NULL, turn_cut_at = NULL
WHERE id = @id;

-- name: VoidChatContinuations :exec
-- A line that woke pi to continue a cut turn and was never given to it goes to pi no more (issue #716): a later
-- start continues the turn again with a line of its own, or does not continue it.
UPDATE chat_messages SET delivered_at = @now WHERE chat_id = @chat_id AND kind = 'continued' AND delivered_at IS NULL;

-- name: VoidContinuations :exec
-- VoidChatContinuations for every chat, when restarts are in a loop.
UPDATE chat_messages SET delivered_at = @now WHERE kind = 'continued' AND delivered_at IS NULL;

-- name: InsertChatRestartLine :one
-- A line of the control plane about a restart: continued, given to pi too, or cut, not given to pi.
INSERT INTO chat_messages (chat_id, role, kind, text, to_coordinator, created_at)
VALUES (@chat_id, 'system', @kind, @text, @to_coordinator, @now)
RETURNING seq;

-- name: ListRestartBoots :many
SELECT at FROM restart_boots ORDER BY at DESC;

-- name: DeleteRestartBoots :exec
DELETE FROM restart_boots;

-- name: InsertRestartBoot :exec
INSERT INTO restart_boots (at) VALUES (@at);

-- name: InsertChatBackground :exec
-- A command a chat's pi started in the background (issue #1058). The same id again keeps the first row.
INSERT INTO chat_background_commands (chat_id, id, pi, started_at) VALUES (@chat_id, @id, @pi, @now)
ON CONFLICT (chat_id, id) DO NOTHING;

-- name: DeleteChatBackground :one
-- Takes out the row of a command that ended or was lost (issue #1084: a command is its chat's, whichever pi runs).
DELETE FROM chat_background_commands WHERE chat_id = @chat_id AND id = @id
RETURNING started_at;

-- name: ListChatBackground :many
-- The commands that run, which the coordinator finds again among the units of the machine (issue #1084).
SELECT chat_id, id, started_at FROM chat_background_commands ORDER BY started_at, chat_id, id;
