-- +goose Up

-- receipt_width: thermal roll width for printed bills, '80' (default) or '58' mm.
ALTER TABLE settings ADD COLUMN receipt_width VARCHAR(3) NOT NULL DEFAULT '80';

-- +goose Down
ALTER TABLE settings DROP COLUMN receipt_width;
