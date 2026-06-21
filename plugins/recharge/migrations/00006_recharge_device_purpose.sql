-- +goose Up
-- A device can hold a recharge float, a money-transfer float, or both. Some
-- carriers (e.g. Mobitel) keep two SEPARATE floats on the same SIM — model each
-- as its own device row, tagged by purpose, so it only appears in the matching
-- picker (reload vs money). Both default true → existing devices work everywhere.
ALTER TABLE recharge_devices ADD COLUMN for_recharge BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE recharge_devices ADD COLUMN for_money    BOOLEAN NOT NULL DEFAULT true;

-- +goose Down
ALTER TABLE recharge_devices DROP COLUMN for_recharge;
ALTER TABLE recharge_devices DROP COLUMN for_money;
