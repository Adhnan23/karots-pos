-- +goose NO TRANSACTION
-- +goose Up
-- Stock leaves for reasons that are neither a sale nor a loss:
--   own_use — the shop consumed its own stock (adhesive, cleaning supplies)
--   staff   — staff ate or took stock
-- Adjust books no cost, so using it here would make the money disappear without
-- ever reaching the P&L. Damage books cost as a LOSS, which reads as breakage or
-- theft and hides how much the shop deliberately consumes. Both need their own
-- type so their cost can be reported on its own line.
--
-- NO TRANSACTION: PostgreSQL forbids using an enum value in the same
-- transaction that added it, and goose wraps migrations in one by default.
ALTER TYPE stock_movement_type ADD VALUE IF NOT EXISTS 'own_use';
ALTER TYPE stock_movement_type ADD VALUE IF NOT EXISTS 'staff';

-- +goose Down
-- PostgreSQL cannot drop a value from an enum. Rows of these types would have to
-- be re-typed and the enum rebuilt, which is not a safe automatic rollback, so
-- the values are deliberately left in place. They are inert when unused.
SELECT 1;
