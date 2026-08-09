-- +goose NO TRANSACTION
-- +goose Up
-- A counter expense paid from the drawer was recorded as a generic 'withdrawal',
-- so in the drawer ledger a bill looked the same as the owner taking cash. Give
-- expenses their own movement type. It behaves exactly like a withdrawal for the
-- drawer/close math (an outflow); only the label differs.
-- NO TRANSACTION: PostgreSQL forbids using an enum value in the same transaction
-- that adds it; goose wraps migrations in a tx unless told otherwise.
ALTER TYPE cash_movement_type ADD VALUE IF NOT EXISTS 'expense';

-- +goose Down
-- PostgreSQL cannot drop an enum value; the added 'expense' value is left in place.
SELECT 1;
