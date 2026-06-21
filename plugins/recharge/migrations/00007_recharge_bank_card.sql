-- +goose Up
-- A bank card is a money source with no float balance to track: the operator pays
-- a bill / hands out money from their own bank card. tracks_float=false devices
-- show in money/bill pickers only, never in reloads, reconciliation or refill, and
-- their float_delta is always zero.
ALTER TABLE recharge_devices ADD COLUMN tracks_float BOOLEAN NOT NULL DEFAULT true;

-- +goose Down
ALTER TABLE recharge_devices DROP COLUMN tracks_float;
