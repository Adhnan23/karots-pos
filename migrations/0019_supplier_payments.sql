-- +goose Up

-- Supplier payments are now tracked per payment, allocated to the specific
-- purchase invoices they settle. Previously paying a supplier only decremented
-- the aggregate suppliers.outstanding_balance — purchases stayed 'received'
-- forever and there was no history. Now each payment records method/reference,
-- splits across invoices (supplier_payment_allocations), advances each
-- purchase's paid_amount/status, and (when cash) leaves the cashier drawer.
CREATE TABLE supplier_payments (
  id          BIGSERIAL PRIMARY KEY,
  supplier_id BIGINT NOT NULL REFERENCES suppliers(id),
  amount      DECIMAL(14,2) NOT NULL,
  method      payment_method NOT NULL DEFAULT 'cash',
  reference   VARCHAR(100),
  note        TEXT,
  paid_by     BIGINT REFERENCES users(id),
  created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_supplier_payments_supplier ON supplier_payments (supplier_id);

CREATE TABLE supplier_payment_allocations (
  id          BIGSERIAL PRIMARY KEY,
  payment_id  BIGINT NOT NULL REFERENCES supplier_payments(id) ON DELETE CASCADE,
  purchase_id BIGINT NOT NULL REFERENCES purchases(id),
  amount      DECIMAL(14,2) NOT NULL
);
CREATE INDEX idx_supplier_payment_alloc_payment  ON supplier_payment_allocations (payment_id);
CREATE INDEX idx_supplier_payment_alloc_purchase ON supplier_payment_allocations (purchase_id);

-- +goose Down
DROP TABLE supplier_payment_allocations;
DROP TABLE supplier_payments;
