-- +goose Up

-- ============================================================================
-- Fix: users.phone was never actually unique. Migration 0001 created a
-- NON-unique index named idx_users_phone; migration 0010 then tried
-- `CREATE UNIQUE INDEX IF NOT EXISTS idx_users_phone` — but because an index
-- with that name already existed, IF NOT EXISTS made it a silent no-op, so the
-- unique constraint was never applied. Phone is the login identifier
-- (auth.FindByPhone), so duplicate phones make login ambiguous: GetContext
-- returns whichever active row sorts first.
--
-- Enforce uniqueness among ACTIVE users only (a partial index), so a retired
-- staff member's phone can be reused for a new hire after deactivation, while
-- login stays unambiguous (FindByPhone already filters is_active = true).
-- ============================================================================

-- De-duplicate first so the unique index can be built: if two or more ACTIVE
-- users share a phone, keep the lowest id and deactivate the rest (they could
-- not reliably log in anyway). No-op when there are no duplicates.
WITH ranked AS (
    SELECT id, row_number() OVER (PARTITION BY phone ORDER BY id) AS rn
    FROM users
    WHERE is_active = true
)
UPDATE users u
SET is_active = false
FROM ranked
WHERE u.id = ranked.id AND ranked.rn > 1;

DROP INDEX IF EXISTS idx_users_phone;
CREATE UNIQUE INDEX idx_users_phone ON users (phone) WHERE is_active = true;

-- +goose Down

DROP INDEX IF EXISTS idx_users_phone;
CREATE INDEX idx_users_phone ON users (phone);
