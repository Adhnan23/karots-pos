-- +goose Up
ALTER TABLE settings ADD COLUMN open_cash_drawer BOOLEAN  NOT NULL DEFAULT false;
ALTER TABLE settings ADD COLUMN drawer_kick_pin  SMALLINT NOT NULL DEFAULT 0; -- 0 = pin 2, 1 = pin 5

-- +goose Down
ALTER TABLE settings DROP COLUMN drawer_kick_pin;
ALTER TABLE settings DROP COLUMN open_cash_drawer;
