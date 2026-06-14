-- +goose Up
ALTER TABLE users ADD COLUMN receipt_printer TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE users DROP COLUMN IF EXISTS receipt_printer;
