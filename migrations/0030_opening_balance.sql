-- +goose Up
-- Opening balances captured when onboarding an existing shop: what a customer
-- already owed us (receivable) or what we already owed a supplier (payable) at
-- go-live. Stored distinctly from the live outstanding_balance so the migrated
-- starting figure stays auditable; at creation both columns are set to the same
-- value and subsequent sales/purchases/payments move outstanding_balance from there.
ALTER TABLE customers ADD COLUMN opening_balance NUMERIC(12, 2) NOT NULL DEFAULT 0;
ALTER TABLE suppliers ADD COLUMN opening_balance NUMERIC(12, 2) NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE suppliers DROP COLUMN opening_balance;
ALTER TABLE customers DROP COLUMN opening_balance;
