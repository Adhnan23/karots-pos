-- +goose Up
-- The kind of bill paid (Electricity, Water, ...), a free-text label the cashier
-- picks or types. Suggested from the distinct values already used, mirroring the
-- core expense-category combo. Reference stays the bill/account number.
ALTER TABLE recharge_bill_tx ADD COLUMN bill_type TEXT;

-- +goose Down
ALTER TABLE recharge_bill_tx DROP COLUMN bill_type;
