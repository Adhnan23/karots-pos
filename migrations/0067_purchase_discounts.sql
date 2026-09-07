-- +goose Up
-- Discounts on receiving, mirroring the sell side. A supplier may knock money
-- off a line (per item) or off the whole invoice, as a flat amount or a percent.
--
-- Like free_qty, a discount lowers how the received lot is VALUED, not just what
-- is owed: the discounted amount is spread across the units so the effective
-- per-unit cost drops and the saving shows up as real margin when the goods sell.
--
-- purchases.discount already holds the resolved bill-discount AMOUNT; these add
-- the entered value and whether it was a flat amount or a percent, so a draft can
-- be re-rendered and re-received exactly as typed. Per-line columns mirror
-- sale_items (discount = resolved amount, discount_type/value = as entered).
ALTER TABLE purchases      ADD COLUMN discount_type  TEXT    NOT NULL DEFAULT 'fixed';
ALTER TABLE purchases      ADD COLUMN discount_value NUMERIC NOT NULL DEFAULT 0;
ALTER TABLE purchase_items ADD COLUMN discount       NUMERIC NOT NULL DEFAULT 0;
ALTER TABLE purchase_items ADD COLUMN discount_type  TEXT    NOT NULL DEFAULT 'fixed';
ALTER TABLE purchase_items ADD COLUMN discount_value NUMERIC NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE purchase_items DROP COLUMN discount_value;
ALTER TABLE purchase_items DROP COLUMN discount_type;
ALTER TABLE purchase_items DROP COLUMN discount;
ALTER TABLE purchases      DROP COLUMN discount_value;
ALTER TABLE purchases      DROP COLUMN discount_type;
