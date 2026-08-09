-- +goose Up
ALTER TABLE recharge_transactions ADD COLUMN reversal_of BIGINT NULL REFERENCES recharge_transactions(id);
ALTER TABLE recharge_transactions ADD COLUMN reversed_at TIMESTAMPTZ NULL;
CREATE INDEX idx_recharge_tx_reversal_of ON recharge_transactions(reversal_of) WHERE reversal_of IS NOT NULL;

-- Widen the type CHECK for the two reload-reversal movements. "reversal" returns
-- the float (a failed reload); "reversal_lost" leaves the float gone (wrong number).
-- Keep every previously allowed type ('opening' was added in 00013).
ALTER TABLE recharge_transactions DROP CONSTRAINT recharge_transactions_type_check;
ALTER TABLE recharge_transactions ADD CONSTRAINT recharge_transactions_type_check
    CHECK (type IN ('deposit','withdrawal','billpay','topup','wallet_in','reload','refill','opening','reversal','reversal_lost'));

-- +goose Down
ALTER TABLE recharge_transactions DROP CONSTRAINT recharge_transactions_type_check;
ALTER TABLE recharge_transactions ADD CONSTRAINT recharge_transactions_type_check
    CHECK (type IN ('deposit','withdrawal','billpay','topup','wallet_in','reload','refill','opening'));
DROP INDEX idx_recharge_tx_reversal_of;
ALTER TABLE recharge_transactions DROP COLUMN reversed_at;
ALTER TABLE recharge_transactions DROP COLUMN reversal_of;
