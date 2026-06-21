-- +goose Up
-- Pinned products surface first on the cashier default grid (which otherwise
-- shows the most-sold items), so the till opens to the shop's common items
-- instead of an empty search.
ALTER TABLE products ADD COLUMN is_pinned BOOLEAN NOT NULL DEFAULT false;
CREATE INDEX idx_products_pinned ON products (is_pinned) WHERE is_pinned = true;

-- +goose Down
DROP INDEX IF EXISTS idx_products_pinned;
ALTER TABLE products DROP COLUMN is_pinned;
