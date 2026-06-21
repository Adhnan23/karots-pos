-- +goose Up
-- Reload is now recorded in the device ledger too (so float is attributed to a
-- specific device, not summed per carrier from sale_items). Widen the type CHECK
-- to include 'reload'. Additive; device_id stays populated by handlers.
ALTER TABLE recharge_transactions DROP CONSTRAINT recharge_transactions_type_check;
ALTER TABLE recharge_transactions ADD CONSTRAINT recharge_transactions_type_check
    CHECK (type IN ('deposit','withdrawal','billpay','topup','wallet_in','reload'));

-- +goose Down
ALTER TABLE recharge_transactions DROP CONSTRAINT recharge_transactions_type_check;
ALTER TABLE recharge_transactions ADD CONSTRAINT recharge_transactions_type_check
    CHECK (type IN ('deposit','withdrawal','billpay','topup','wallet_in'));
