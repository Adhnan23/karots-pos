-- +goose Up
-- A custom brand colour (hex) used when skin = 'custom': the full --brand-*
-- ramp is derived from this one colour at render time. Blank until chosen.
ALTER TABLE settings ADD COLUMN skin_custom TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE settings DROP COLUMN skin_custom;
