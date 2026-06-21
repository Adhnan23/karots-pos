-- +goose Up
-- Admin "supplier float refill" is a distinct movement from the cashier till
-- top-up: it increases a device's float and books a shop expense, but does NOT
-- touch any cash drawer (the admin pays the supplier directly). Give it its own
-- type so the ledger keeps the two cash semantics honest. Additive.
ALTER TABLE recharge_transactions DROP CONSTRAINT recharge_transactions_type_check;
ALTER TABLE recharge_transactions ADD CONSTRAINT recharge_transactions_type_check
    CHECK (type IN ('deposit','withdrawal','billpay','topup','wallet_in','reload','refill'));

-- +goose Down
ALTER TABLE recharge_transactions DROP CONSTRAINT recharge_transactions_type_check;
ALTER TABLE recharge_transactions ADD CONSTRAINT recharge_transactions_type_check
    CHECK (type IN ('deposit','withdrawal','billpay','topup','wallet_in','reload'));
