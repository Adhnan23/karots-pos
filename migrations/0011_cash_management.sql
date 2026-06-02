-- +goose Up

-- ============================================================================
-- Cash management. Two parts:
--  1. denominations  — the notes/coins the shop handles (admin can add/remove
--     as new notes are issued). Used to count the drawer by piece count.
--  2. cash_movements — an audit ledger of every cash event in a register
--     session: opening float, mid-shift withdrawals/pay-ins, credit collected,
--     and closing. Amounts are SIGNED (+ into drawer, − out). The cash_register
--     row also keeps the denomination breakdown counted at open and close.
-- ============================================================================

CREATE TABLE denominations (
  id         BIGSERIAL     PRIMARY KEY,
  value      DECIMAL(12,2) NOT NULL UNIQUE,
  is_note    BOOLEAN       NOT NULL DEFAULT true,
  is_active  BOOLEAN       NOT NULL DEFAULT true,
  created_at TIMESTAMPTZ   NOT NULL DEFAULT NOW()
);

-- Sri Lankan rupee coins + notes in circulation (admin can edit later).
INSERT INTO denominations (value, is_note) VALUES
  (1, false), (2, false), (5, false), (10, false),
  (20, true), (50, true), (100, true), (500, true), (1000, true), (2000, true), (5000, true);

ALTER TABLE cash_register ADD COLUMN opening_breakdown JSONB;
ALTER TABLE cash_register ADD COLUMN closing_breakdown JSONB;

CREATE TYPE cash_movement_type AS ENUM (
  'opening', 'sale', 'credit_payment', 'withdrawal', 'pay_in', 'refund', 'closing'
);

CREATE TABLE cash_movements (
  id         BIGSERIAL          PRIMARY KEY,
  session_id BIGINT             NOT NULL REFERENCES cash_register (id) ON DELETE CASCADE,
  user_id    BIGINT             NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
  type       cash_movement_type NOT NULL,
  amount     DECIMAL(14,2)      NOT NULL,
  reason     TEXT,
  breakdown  JSONB,
  created_at TIMESTAMPTZ        NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_cash_movements_session ON cash_movements (session_id);
CREATE INDEX idx_cash_movements_created ON cash_movements (created_at DESC);

-- +goose Down
DROP TABLE cash_movements;
DROP TYPE cash_movement_type;
ALTER TABLE cash_register DROP COLUMN opening_breakdown;
ALTER TABLE cash_register DROP COLUMN closing_breakdown;
DROP TABLE denominations;
