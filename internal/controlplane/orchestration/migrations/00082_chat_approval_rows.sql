-- An answered Approval stays in its chat as a row (issue #465, the design's item 5 in #254), as t3code keeps an
-- approval request in its timeline (apps/web/src/components/chat/TimelinePendingApprovalRow.tsx) and AI Elements
-- shows an answered confirmation as accepted or rejected (packages/elements/src/confirmation.tsx). The row names its
-- Approval, which is read with it; one row an Approval. Rows kept before this named the Approval only in their text,
-- "(apr_…)", and are matched by it; an answer repeated wrote the notice again, so only the first row keeps the id.

-- +goose Up
ALTER TABLE chat_messages ADD COLUMN approval_id TEXT;

UPDATE chat_messages SET approval_id = (
    SELECT a.id FROM approvals a
    WHERE a.chat_id = chat_messages.chat_id AND instr(chat_messages.text, '(' || a.id || ')') > 0
    LIMIT 1
)
WHERE kind = 'approval';

UPDATE chat_messages SET approval_id = NULL
WHERE approval_id IS NOT NULL
  AND seq NOT IN (SELECT min(seq) FROM chat_messages WHERE approval_id IS NOT NULL GROUP BY approval_id);

CREATE UNIQUE INDEX chat_messages_approval ON chat_messages (approval_id) WHERE approval_id IS NOT NULL;

-- +goose Down
DROP INDEX chat_messages_approval;
ALTER TABLE chat_messages DROP COLUMN approval_id;
