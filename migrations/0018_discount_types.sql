-- +goose Up

-- Discounts can now be a fixed amount or a percentage, both at the bill level
-- and per line item. The existing `discount` columns keep storing the RESOLVED
-- amount (so totals/reports/receipts are unchanged); these new columns record
-- what the cashier entered so receipts can show "10%" and the intent is kept.
ALTER TABLE sales ADD COLUMN discount_type  VARCHAR(8)    NOT NULL DEFAULT 'fixed';
ALTER TABLE sales ADD COLUMN discount_value DECIMAL(14,2) NOT NULL DEFAULT 0;

ALTER TABLE sale_items ADD COLUMN discount_type  VARCHAR(8)    NOT NULL DEFAULT 'fixed';
ALTER TABLE sale_items ADD COLUMN discount_value DECIMAL(12,2) NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE sale_items DROP COLUMN discount_value;
ALTER TABLE sale_items DROP COLUMN discount_type;
ALTER TABLE sales DROP COLUMN discount_value;
ALTER TABLE sales DROP COLUMN discount_type;
