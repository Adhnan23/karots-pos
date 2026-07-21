-- +goose Up
-- Quantities carried 3 decimal places, which is fine for sheets and bottles but
-- makes yield-based consumption impossible: a toner rated for 5000 copies is
-- 0.0002 per copy, which truncates to 0.000 and deducts nothing at all. Six
-- decimals covers a yield of 1,000,000.
--
-- Widening a numeric's scale rewrites no data and cannot fail on existing rows;
-- every current value is representable in the wider type.
ALTER TABLE stock           ALTER COLUMN quantity      TYPE numeric(14,6);
ALTER TABLE stock_batches   ALTER COLUMN qty_received  TYPE numeric(14,6);
ALTER TABLE stock_batches   ALTER COLUMN qty_remaining TYPE numeric(14,6);
ALTER TABLE stock_movements ALTER COLUMN quantity      TYPE numeric(14,6);

-- +goose Down
-- Narrowing rounds values that use the extra precision. Round explicitly first
-- so the change is visible in the data rather than silently applied by the cast.
UPDATE stock           SET quantity      = round(quantity, 3);
UPDATE stock_batches   SET qty_received  = round(qty_received, 3),
                          qty_remaining = round(qty_remaining, 3);
UPDATE stock_movements SET quantity      = round(quantity, 3);

ALTER TABLE stock           ALTER COLUMN quantity      TYPE numeric(12,3);
ALTER TABLE stock_batches   ALTER COLUMN qty_received  TYPE numeric(12,3);
ALTER TABLE stock_batches   ALTER COLUMN qty_remaining TYPE numeric(12,3);
ALTER TABLE stock_movements ALTER COLUMN quantity      TYPE numeric(12,3);
