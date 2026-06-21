-- +goose Up
-- Service charge collected on a money/bill transaction (deposit/withdrawal/
-- billpay/topup, including bank-card). It is always paid in cash on top of the
-- principal, so it adds to the drawer and is tracked as shop earnings. Reloads
-- never carry a service charge.
ALTER TABLE recharge_transactions ADD COLUMN service_charge NUMERIC(12,2) NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE recharge_transactions DROP COLUMN service_charge;
