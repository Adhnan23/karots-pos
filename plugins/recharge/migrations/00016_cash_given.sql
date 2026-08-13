-- +goose Up
-- Record the cash the customer actually handed over for a cash-in transaction
-- (bill payment, wallet deposit) so the slip can print the change given, exactly
-- like a sale receipt. Nullable: older rows and cash-out transactions (get-money,
-- withdrawal) have no tender, and the slip simply omits the change line.
ALTER TABLE recharge_bill_tx ADD COLUMN cash_given NUMERIC(12,2);
ALTER TABLE recharge_transactions ADD COLUMN cash_given NUMERIC(12,2);

-- +goose Down
ALTER TABLE recharge_bill_tx DROP COLUMN cash_given;
ALTER TABLE recharge_transactions DROP COLUMN cash_given;
