-- +goose Up

-- ============================================================================
-- Losses & recovery. Damaged stock and warranty replacements remove goods from
-- inventory at a cost. Until now that cost was discarded; here we record the
-- worth of each loss on its stock movement, and track when a faulty item is
-- reclaimed from the supplier (a replacement unit, a refund/credit, or written
-- off). This makes the loss and any recovery visible in the finance P&L.
-- ============================================================================

-- The cost worth of the goods moved. Populated for damage, warranty_replacement
-- and recovery (restock-in) movements; 0 for everything else (and for rows that
-- predate this migration). Drives the loss reports and the P&L Losses line.
ALTER TABLE stock_movements ADD COLUMN cost NUMERIC(12,2) NOT NULL DEFAULT 0;

-- When a supplier hands back a replacement for a faulty unit, the goods re-enter
-- inventory via this movement type (a recovery, not a purchase).
-- +goose StatementBegin
ALTER TYPE stock_movement_type ADD VALUE IF NOT EXISTS 'recovery';
-- +goose StatementEnd

-- One row per recovery action recorded against a loss (a replaced warranty unit
-- or a damage write-off).
CREATE TABLE loss_recoveries (
  id              BIGSERIAL     PRIMARY KEY,
  source_type     VARCHAR(16)   NOT NULL,                     -- 'warranty' | 'damage'
  source_id       BIGINT        NOT NULL,                     -- warranty_units.id | stock_movements.id
  product_id      BIGINT        NOT NULL REFERENCES products (id)  ON DELETE RESTRICT,
  supplier_id     BIGINT        REFERENCES suppliers (id)     ON DELETE SET NULL,
  outcome         VARCHAR(16)   NOT NULL,                     -- 'replacement' | 'paid' | 'written_off'
  quantity        NUMERIC(12,3) NOT NULL DEFAULT 1,
  loss_value      NUMERIC(12,2) NOT NULL DEFAULT 0,           -- cost worth of the faulty goods
  recovered_value NUMERIC(12,2) NOT NULL DEFAULT 0,           -- monetary benefit recovered
  note            TEXT,
  handled_by      BIGINT        NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
  created_at      TIMESTAMPTZ   NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_loss_recoveries_source ON loss_recoveries (source_type, source_id);

-- +goose Down
DROP TABLE IF EXISTS loss_recoveries;
ALTER TABLE stock_movements DROP COLUMN IF EXISTS cost;
-- Note: the 'recovery' enum value is left in place; Postgres cannot easily drop
-- an enum value, and it is harmless if unused.
