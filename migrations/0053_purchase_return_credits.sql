-- +goose Up
-- Returning goods to a supplier used to adjust ONLY the aggregate
-- suppliers.outstanding_balance, never the invoice the goods came in on — and a
-- debit note had no link to a purchase at all. So the aggregate and the
-- per-invoice ledger disagreed the moment anything was returned, the returned
-- invoice stayed on the "open" payment queue, and the shop could hand over cash
-- for goods it had already sent back.
--
-- credited_amount is the value returned against an invoice. It is NOT paid_amount:
-- no money moved, and marking a returned invoice "paid" is exactly the lie an
-- earlier defect told. Everything that asks "what is still owed" now reads
-- total - paid_amount - credited_amount.
ALTER TABLE purchases ADD COLUMN credited_amount DECIMAL(12,2) NOT NULL DEFAULT 0.00;

-- Which invoices a debit note credited, and by how much. Mirrors
-- supplier_payment_allocations so a return is auditable the same way a payment is.
CREATE TABLE purchase_return_allocations (
  id                 BIGSERIAL     PRIMARY KEY,
  purchase_return_id BIGINT        NOT NULL REFERENCES purchase_returns (id) ON DELETE CASCADE,
  purchase_id        BIGINT        NOT NULL REFERENCES purchases (id)        ON DELETE CASCADE,
  amount             DECIMAL(12,2) NOT NULL,
  created_at         TIMESTAMPTZ   NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_pr_alloc_return   ON purchase_return_allocations (purchase_return_id);
CREATE INDEX idx_pr_alloc_purchase ON purchase_return_allocations (purchase_id);

-- Which lot actually went back. Returns depleted FEFO, so sending back a damaged
-- NEW delivery drained the OLDEST lot instead — leaving the shop's records
-- claiming stock it no longer has, at a price the till would offer a customer.
ALTER TABLE purchase_return_items ADD COLUMN batch_id BIGINT REFERENCES stock_batches (id) ON DELETE SET NULL;

-- +goose Down
ALTER TABLE purchase_return_items DROP COLUMN batch_id;
DROP TABLE purchase_return_allocations;
ALTER TABLE purchases DROP COLUMN credited_amount;
