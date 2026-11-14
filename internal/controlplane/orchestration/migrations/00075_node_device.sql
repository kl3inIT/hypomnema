-- Whether a node holds an Android emulator and can run it now (issue #417): it says so at each hello, as it says
-- whether it opens a terminal (00035). `device` is what the node's configuration names, `kvm` whether its user can
-- use /dev/kvm, without which the emulator does not run.

-- +goose Up
ALTER TABLE nodes ADD COLUMN device TEXT NOT NULL DEFAULT '' CHECK (device IN ('', 'android'));
ALTER TABLE nodes ADD COLUMN kvm INTEGER NOT NULL DEFAULT 0 CHECK (kvm IN (0, 1));

-- +goose Down
ALTER TABLE nodes DROP COLUMN kvm;
ALTER TABLE nodes DROP COLUMN device;
