-- +goose Up
-- Money coming back FROM a supplier had no path at all. Returning goods left the
-- supplier's balance negative — they owe us — and nothing could ever settle it in
-- cash. The same hole sat under loss recovery's "supplier paid" outcome, which
-- reduced the payable but moved no money into any drawer.
--
-- A refund is the mirror of a supplier payment, so it is shaped like one. The
-- cash side is booked by the web layer through cashflow.Move (External -> till or
-- locker), exactly as supplier payments and customer-credit collection are, so
-- this table stays free of any drawer dependency.
--
-- Deliberately NOT a negative supplier_payments row: every report that sums
-- payments would silently net refunds against them, and the slip would read
-- backwards.
CREATE TABLE supplier_refunds (
  id          BIGSERIAL     PRIMARY KEY,
  supplier_id BIGINT        NOT NULL REFERENCES suppliers (id),
  amount      DECIMAL(14,2) NOT NULL CHECK (amount > 0),
  method      payment_method NOT NULL DEFAULT 'cash',
  reference   VARCHAR(100),
  note        TEXT,
  received_by BIGINT        REFERENCES users (id),
  created_at  TIMESTAMPTZ   NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_supplier_refunds_supplier ON supplier_refunds (supplier_id);
CREATE INDEX idx_supplier_refunds_created  ON supplier_refunds (created_at);

-- +goose Down
DROP TABLE supplier_refunds;
