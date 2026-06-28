-- +goose Up
CREATE SEQUENCE debt_receipt_seq;
ALTER TABLE customer_payments
  ADD COLUMN receipt_no     TEXT,
  ADD COLUMN balance_before NUMERIC(14,2),
  ADD COLUMN balance_after  NUMERIC(14,2);
UPDATE customer_payments
  SET receipt_no = 'DP-' || lpad(nextval('debt_receipt_seq')::text, 6, '0')
  WHERE receipt_no IS NULL;
CREATE UNIQUE INDEX customer_payments_receipt_no_key ON customer_payments(receipt_no);

-- +goose Down
DROP INDEX IF EXISTS customer_payments_receipt_no_key;
ALTER TABLE customer_payments
  DROP COLUMN receipt_no,
  DROP COLUMN balance_before,
  DROP COLUMN balance_after;
DROP SEQUENCE IF EXISTS debt_receipt_seq;
