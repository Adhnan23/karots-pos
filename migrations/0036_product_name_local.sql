-- +goose Up
-- Rename the product's local-language name from name_si (Sinhala-specific) to the
-- neutral name_local, since shops also label products in Tamil (or any script).
ALTER TABLE products RENAME COLUMN name_si TO name_local;

-- +goose Down
ALTER TABLE products RENAME COLUMN name_local TO name_si;
