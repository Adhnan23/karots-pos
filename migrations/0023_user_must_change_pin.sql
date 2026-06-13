-- +goose Up

-- ============================================================================
-- Forced PIN change. Staff PINs that the user did not personally choose — the
-- seeded starter accounts and any PIN an admin sets/resets — must be changed by
-- the user on their next login. This flag drives the guard that redirects such
-- users to the change-PIN screen before they can use the rest of the app.
-- ============================================================================
ALTER TABLE users ADD COLUMN must_change_pin BOOLEAN NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE users DROP COLUMN IF EXISTS must_change_pin;
