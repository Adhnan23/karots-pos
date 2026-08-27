-- +goose Up
-- Per-user permission: see product cost/margin in the till info popup.
-- Off by default; admins/managers/system always may. Mirrors can_manage_credit.
ALTER TABLE users ADD COLUMN can_see_cost boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE users DROP COLUMN can_see_cost;
