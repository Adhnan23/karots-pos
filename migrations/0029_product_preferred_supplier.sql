-- +goose Up
-- Optional preferred/default supplier per product, so the catalog records who the
-- shop usually buys an item from (set during bulk import or in the product form).
-- It is a reference only — no purchase/payable is implied.
ALTER TABLE products
  ADD COLUMN preferred_supplier_id BIGINT REFERENCES suppliers (id) ON DELETE SET NULL;

CREATE INDEX idx_products_preferred_supplier ON products (preferred_supplier_id);

-- +goose Down
DROP INDEX IF EXISTS idx_products_preferred_supplier;
ALTER TABLE products DROP COLUMN preferred_supplier_id;
