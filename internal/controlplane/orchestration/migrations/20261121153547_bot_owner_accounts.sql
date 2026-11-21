-- What a Bot may do with the owner's own accounts (issue #1023, the owner's choice of 2026-11-21 on #1037): use, as
-- every Bot until now, or read, which refuses at once, in every chat and Routine of the Bot, each write and send
-- through his Lark sign-in, his Google, his Zalo and his WhatsApp, whatever his approvals say. Only the owner sets it.
-- +goose Up
ALTER TABLE bots ADD COLUMN owner_accounts TEXT NOT NULL DEFAULT 'use' CHECK (owner_accounts IN ('use', 'read'));

-- +goose Down
ALTER TABLE bots DROP COLUMN owner_accounts;
