-- +goose Up
ALTER TABLE products DROP COLUMN is_pinned;

-- +goose Down
ALTER TABLE products ADD COLUMN is_pinned BOOLEAN NOT NULL DEFAULT false;
