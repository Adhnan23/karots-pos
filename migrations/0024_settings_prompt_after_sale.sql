-- +goose Up
ALTER TABLE settings ADD COLUMN prompt_after_sale BOOLEAN NOT NULL DEFAULT true;

-- +goose Down
ALTER TABLE settings DROP COLUMN IF EXISTS prompt_after_sale;
