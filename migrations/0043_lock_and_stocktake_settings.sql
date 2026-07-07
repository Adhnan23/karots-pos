-- +goose Up
ALTER TABLE settings
  ADD COLUMN lock_timeout_minutes INT     NOT NULL DEFAULT 0,
  ADD COLUMN stock_take_enabled   BOOLEAN NOT NULL DEFAULT true;

-- +goose Down
ALTER TABLE settings
  DROP COLUMN lock_timeout_minutes,
  DROP COLUMN stock_take_enabled;
