-- Why a to-do a Routine's run found starts open (issue #966; the owner's triage of 2026-11-20 on #838):
-- asked_directly, someone tagged or messaged the owner and he has not answered; promised, the owner promised it
-- himself. NULL for every other item, which starts as its source allows (inboxstore's mayCreate).

-- +goose Up
ALTER TABLE todos ADD COLUMN why_open TEXT CHECK (why_open IN ('asked_directly', 'promised'));

-- +goose Down
ALTER TABLE todos DROP COLUMN why_open;
