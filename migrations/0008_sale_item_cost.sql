-- +goose Up
-- Snapshot the unit cost at sale time so profit (revenue − COGS) is accurate
-- even after a product's cost price later changes.
ALTER TABLE sale_items ADD COLUMN cost_price DECIMAL(12,2) NOT NULL DEFAULT 0.00;

-- +goose Down
ALTER TABLE sale_items DROP COLUMN cost_price;
