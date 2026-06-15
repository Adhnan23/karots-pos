-- +goose Up
-- A hidden, developer-only recovery admin. It never appears in the user list or
-- login picker and cannot be edited/deactivated from the UI, so an owner can
-- never lock everyone out of the install.
ALTER TABLE users ADD COLUMN is_system BOOLEAN NOT NULL DEFAULT false;

-- PIN-change policy is now opt-in. Default: an admin-set PIN is used as-is (the
-- user just logs in), and cashiers may change their own PIN.
ALTER TABLE settings ADD COLUMN force_pin_change         BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE settings ADD COLUMN allow_cashier_pin_change BOOLEAN NOT NULL DEFAULT true;

-- Adopt the new default for existing staff: clear any pending forced change so
-- nobody is prompted unless the admin turns the setting on.
UPDATE users SET must_change_pin = false;

-- +goose Down
ALTER TABLE settings DROP COLUMN IF EXISTS allow_cashier_pin_change;
ALTER TABLE settings DROP COLUMN IF EXISTS force_pin_change;
ALTER TABLE users    DROP COLUMN IF EXISTS is_system;
