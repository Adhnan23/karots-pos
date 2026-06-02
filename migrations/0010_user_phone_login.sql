-- +goose Up

-- ============================================================================
-- Phone-based login. Users now sign in with their phone number + PIN (instead of
-- picking a name from a list), so phone must be present and unique. Backfill any
-- existing NULL phones — the seeded Admin/Cashier get memorable numbers, others
-- get a deterministic placeholder — then enforce UNIQUE + NOT NULL.
-- ============================================================================

UPDATE users SET phone = '0771234567' WHERE name = 'Admin'   AND phone IS NULL;
UPDATE users SET phone = '0771111111' WHERE name = 'Cashier' AND phone IS NULL;
UPDATE users SET phone = '070000' || lpad(id::text, 4, '0') WHERE phone IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_users_phone ON users (phone);
ALTER TABLE users ALTER COLUMN phone SET NOT NULL;

-- +goose Down

ALTER TABLE users ALTER COLUMN phone DROP NOT NULL;
DROP INDEX IF EXISTS idx_users_phone;
