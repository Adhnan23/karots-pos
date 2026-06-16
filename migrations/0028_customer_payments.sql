-- +goose Up
-- Per-payment log for customer credit repayments. Previously a repayment only
-- decremented customers.outstanding_balance with no record; this table lets the
-- customer statement show repayment history with a running balance.
CREATE TABLE customer_payments (
  id          BIGSERIAL     PRIMARY KEY,
  customer_id BIGINT        NOT NULL REFERENCES customers (id) ON DELETE CASCADE,
  amount      DECIMAL(14,2) NOT NULL,
  method      VARCHAR(20)   NOT NULL DEFAULT 'cash',
  reference   VARCHAR(100),
  note        TEXT,
  created_by  BIGINT        REFERENCES users (id) ON DELETE SET NULL,
  created_at  TIMESTAMPTZ   NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_customer_payments_customer ON customer_payments (customer_id, created_at);

-- +goose Down
DROP TABLE customer_payments;
