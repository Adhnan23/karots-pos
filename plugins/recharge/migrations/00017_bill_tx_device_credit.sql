-- +goose Up
-- Bill payments / get-money can now be settled against a money-usable device's
-- float (device_id) instead of only a core bank locker, and can be put on a
-- customer's account (customer_id) instead of cash. bank_locker_id becomes
-- nullable — device-sourced and pure-credit rows have none. bank_name still
-- carries the human display label (the device label for device rows, or
-- "On account" for a pure-credit get-money). All additive; existing rows keep
-- their bank source and NULL device/customer.
ALTER TABLE recharge_bill_tx ALTER COLUMN bank_locker_id DROP NOT NULL;
ALTER TABLE recharge_bill_tx ADD COLUMN device_id   BIGINT;
ALTER TABLE recharge_bill_tx ADD COLUMN customer_id BIGINT;

-- +goose Down
ALTER TABLE recharge_bill_tx DROP COLUMN customer_id;
ALTER TABLE recharge_bill_tx DROP COLUMN device_id;
ALTER TABLE recharge_bill_tx ALTER COLUMN bank_locker_id SET NOT NULL;
