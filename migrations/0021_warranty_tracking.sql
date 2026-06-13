-- +goose Up

-- ============================================================================
-- Warranty / serial-number tracking. Some goods (electronics etc.) carry a
-- manufacturer warranty and a unique serial/IMEI captured at the time of sale.
-- We record each sold serialized unit, its warranty window, and any claim that
-- ends in a replacement (which restarts the warranty fresh and ships a new
-- unit from stock).
-- ============================================================================

-- Two independent product flags: a product may track serials, carry a warranty,
-- or both. track_serial drives the serial prompt at checkout; warranty_months
-- is the default warranty length stamped onto each sold unit.
ALTER TABLE products ADD COLUMN track_serial    BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE products ADD COLUMN warranty_months INTEGER NOT NULL DEFAULT 0;

-- A replacement hands a new unit to the customer for free; it is a cost, never a
-- sale. The handed-out unit leaves inventory via this movement type so on-hand
-- and finance (inventory valuation) stay correct without inflating revenue.
-- +goose StatementBegin
ALTER TYPE stock_movement_type ADD VALUE IF NOT EXISTS 'warranty_replacement';
-- +goose StatementEnd

-- One row per sold serialized unit, and one per replacement unit issued.
CREATE TABLE warranty_units (
  id                  BIGSERIAL    PRIMARY KEY,
  product_id          BIGINT       NOT NULL REFERENCES products (id)  ON DELETE RESTRICT,
  serial_no           VARCHAR(100) NOT NULL UNIQUE,                 -- lookup key
  sale_id             BIGINT       REFERENCES sales (id)      ON DELETE SET NULL, -- NULL for replacements
  customer_id         BIGINT       REFERENCES customers (id)  ON DELETE SET NULL,
  sold_at             TIMESTAMPTZ  NOT NULL,
  warranty_months     INTEGER      NOT NULL,
  warranty_until      DATE         NOT NULL,                        -- sold_at + warranty_months
  source              VARCHAR(16)  NOT NULL DEFAULT 'sale',         -- 'sale' | 'replacement'
  status              VARCHAR(16)  NOT NULL DEFAULT 'active',       -- 'active' | 'replaced' | 'void'
  replaced_by_unit_id BIGINT       REFERENCES warranty_units (id) ON DELETE SET NULL,
  created_at          TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_warranty_units_product ON warranty_units (product_id);
CREATE INDEX idx_warranty_units_sale    ON warranty_units (sale_id);

-- The audit trail of each claim. A 'replaced' claim points at the new unit.
CREATE TABLE warranty_claims (
  id                  BIGSERIAL    PRIMARY KEY,
  unit_id             BIGINT       NOT NULL REFERENCES warranty_units (id) ON DELETE CASCADE,
  claim_date          DATE         NOT NULL DEFAULT CURRENT_DATE,
  reason              TEXT,
  resolution          VARCHAR(16)  NOT NULL,                        -- 'replaced' | 'repaired' | 'rejected'
  replacement_unit_id BIGINT       REFERENCES warranty_units (id) ON DELETE SET NULL,
  handled_by          BIGINT       NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
  created_at          TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_warranty_claims_unit ON warranty_claims (unit_id);

-- +goose Down
DROP TABLE IF EXISTS warranty_claims;
DROP TABLE IF EXISTS warranty_units;
ALTER TABLE products DROP COLUMN IF EXISTS warranty_months;
ALTER TABLE products DROP COLUMN IF EXISTS track_serial;
-- Note: the 'warranty_replacement' enum value is left in place; Postgres cannot
-- easily drop an enum value, and it is harmless if unused.
