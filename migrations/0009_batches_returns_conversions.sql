-- +goose Up

-- ============================================================================
-- Batch / lot tracking (FEFO). Each GRN line (and adjustments/returns) creates a
-- batch carrying its own expiry + cost. Sales deplete oldest-expiry-first. The
-- stock table stays as the fast, atomic oversell guard and a cached aggregate
-- (SUM of qty_remaining); these helpers keep the two consistent in one tx.
-- ============================================================================

ALTER TABLE products ADD COLUMN has_expiry BOOLEAN NOT NULL DEFAULT false;

CREATE TABLE stock_batches (
  id               BIGSERIAL     PRIMARY KEY,
  product_id       BIGINT        NOT NULL REFERENCES products (id)       ON DELETE CASCADE,
  purchase_item_id BIGINT        REFERENCES purchase_items (id)          ON DELETE SET NULL,
  batch_no         VARCHAR(50),
  expiry_date      DATE,
  qty_received     DECIMAL(12,3) NOT NULL,
  qty_remaining    DECIMAL(12,3) NOT NULL CHECK (qty_remaining >= 0),
  cost_price       DECIMAL(12,2) NOT NULL DEFAULT 0.00,
  source           VARCHAR(20)   NOT NULL DEFAULT 'purchase', -- purchase|opening|adjust|return|conversion
  created_at       TIMESTAMPTZ   NOT NULL DEFAULT NOW()
);
-- FEFO consume order: earliest expiry first, NULL expiry last, then oldest id.
CREATE INDEX idx_stock_batches_fefo   ON stock_batches (product_id, expiry_date NULLS LAST, id) WHERE qty_remaining > 0;
CREATE INDEX idx_stock_batches_expiry ON stock_batches (expiry_date) WHERE qty_remaining > 0;

-- Seed a batch for every product that already has stock, so SUM(batches) equals
-- stock.quantity from day one (no FEFO drift for pre-existing inventory).
INSERT INTO stock_batches (product_id, qty_received, qty_remaining, cost_price, source)
SELECT s.product_id, s.quantity, s.quantity, p.cost_price, 'opening'
FROM stock s JOIN products p ON p.id = s.product_id
WHERE s.quantity > 0;

-- new movement types for conversions and supplier returns
-- +goose StatementBegin
ALTER TYPE stock_movement_type ADD VALUE IF NOT EXISTS 'conversion';
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TYPE stock_movement_type ADD VALUE IF NOT EXISTS 'purchase_return';
-- +goose StatementEnd

-- ============================================================================
-- Partial sale returns (per-line quantity). returned_qty on the line caps how
-- much can still be returned; status flows completed -> partially_returned ->
-- returned as lines are sent back.
-- ============================================================================

ALTER TABLE sale_items ADD COLUMN returned_qty DECIMAL(12,3) NOT NULL DEFAULT 0.000;

-- +goose StatementBegin
ALTER TYPE sale_status ADD VALUE IF NOT EXISTS 'partially_returned';
-- +goose StatementEnd

CREATE TABLE sale_returns (
  id               BIGSERIAL     PRIMARY KEY,
  sale_id          BIGINT        NOT NULL REFERENCES sales (id) ON DELETE CASCADE,
  refund_amount    DECIMAL(14,2) NOT NULL DEFAULT 0.00, -- cash/card refunded to customer
  credit_reduction DECIMAL(14,2) NOT NULL DEFAULT 0.00, -- credit removed from customer balance
  reason           TEXT,
  created_by       BIGINT        NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
  created_at       TIMESTAMPTZ   NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_sale_returns_sale_id ON sale_returns (sale_id);

CREATE TABLE sale_return_items (
  id            BIGSERIAL     PRIMARY KEY,
  return_id     BIGINT        NOT NULL REFERENCES sale_returns (id) ON DELETE CASCADE,
  sale_item_id  BIGINT        NOT NULL REFERENCES sale_items (id)   ON DELETE RESTRICT,
  product_id    BIGINT        NOT NULL REFERENCES products (id)     ON DELETE RESTRICT,
  quantity      DECIMAL(12,3) NOT NULL,
  refund_amount DECIMAL(14,2) NOT NULL
);
CREATE INDEX idx_sale_return_items_return_id ON sale_return_items (return_id);

-- ============================================================================
-- Purchase returns to suppliers (debit notes): goods sent back reduce stock
-- (FEFO) and the supplier payable.
-- ============================================================================

CREATE TABLE purchase_returns (
  id          BIGSERIAL     PRIMARY KEY,
  supplier_id BIGINT        NOT NULL REFERENCES suppliers (id) ON DELETE RESTRICT,
  reference   VARCHAR(50),
  total       DECIMAL(14,2) NOT NULL DEFAULT 0.00,
  reason      TEXT,
  created_by  BIGINT        NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
  created_at  TIMESTAMPTZ   NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_purchase_returns_supplier_id ON purchase_returns (supplier_id);

CREATE TABLE purchase_return_items (
  id                 BIGSERIAL     PRIMARY KEY,
  purchase_return_id BIGINT        NOT NULL REFERENCES purchase_returns (id) ON DELETE CASCADE,
  product_id         BIGINT        NOT NULL REFERENCES products (id)         ON DELETE RESTRICT,
  quantity           DECIMAL(12,3) NOT NULL,
  cost_price         DECIMAL(12,2) NOT NULL,
  subtotal           DECIMAL(14,2) NOT NULL
);
CREATE INDEX idx_purchase_return_items_pr_id ON purchase_return_items (purchase_return_id);

-- ============================================================================
-- Product conversions (e.g. 1 "bag of rice" -> 25 "loose rice (kg)"). A run
-- depletes the parent product's stock (FEFO) and creates a child batch.
-- ============================================================================

CREATE TABLE product_conversions (
  id              BIGSERIAL     PRIMARY KEY,
  from_product_id BIGINT        NOT NULL REFERENCES products (id) ON DELETE CASCADE,
  to_product_id   BIGINT        NOT NULL REFERENCES products (id) ON DELETE CASCADE,
  ratio           DECIMAL(12,3) NOT NULL CHECK (ratio > 0), -- 1 from-unit yields `ratio` to-units
  note            VARCHAR(150),
  is_active       BOOLEAN       NOT NULL DEFAULT true,
  created_at      TIMESTAMPTZ   NOT NULL DEFAULT NOW(),
  CHECK (from_product_id <> to_product_id),
  UNIQUE (from_product_id, to_product_id)
);

CREATE TABLE conversion_runs (
  id              BIGSERIAL     PRIMARY KEY,
  conversion_id   BIGINT        REFERENCES product_conversions (id) ON DELETE SET NULL,
  from_product_id BIGINT        NOT NULL REFERENCES products (id) ON DELETE RESTRICT,
  to_product_id   BIGINT        NOT NULL REFERENCES products (id) ON DELETE RESTRICT,
  from_qty        DECIMAL(12,3) NOT NULL,
  to_qty          DECIMAL(12,3) NOT NULL,
  created_by      BIGINT        NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
  created_at      TIMESTAMPTZ   NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_conversion_runs_created_at ON conversion_runs (created_at DESC);

-- +goose Down
DROP TABLE conversion_runs;
DROP TABLE product_conversions;
DROP TABLE purchase_return_items;
DROP TABLE purchase_returns;
DROP TABLE sale_return_items;
DROP TABLE sale_returns;
ALTER TABLE sale_items DROP COLUMN returned_qty;
DROP TABLE stock_batches;
ALTER TABLE products DROP COLUMN has_expiry;
-- NOTE: enum values added to stock_movement_type / sale_status are not removed
-- (Postgres cannot drop enum values); harmless to leave on a down-migration.
