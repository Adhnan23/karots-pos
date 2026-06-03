-- +goose Up

-- Parked/suspended carts: a cashier can hold the current cart to serve the next
-- customer, then resume it. The cart itself is stored opaquely as JSON (the
-- terminal's line items) and replayed client-side on resume.
CREATE TABLE held_sales (
  id          BIGSERIAL     PRIMARY KEY,
  cashier_id  BIGINT        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  label       VARCHAR(120),
  sale_type   VARCHAR(20)   NOT NULL DEFAULT 'retail',
  customer_id BIGINT        REFERENCES customers (id) ON DELETE SET NULL,
  discount    DECIMAL(14,2) NOT NULL DEFAULT 0.00,
  cart        JSONB         NOT NULL,
  item_count  INT           NOT NULL DEFAULT 0,
  total       DECIMAL(14,2) NOT NULL DEFAULT 0.00,
  created_at  TIMESTAMPTZ   NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_held_sales_cashier ON held_sales (cashier_id, created_at DESC);

-- +goose Down
DROP TABLE held_sales;
