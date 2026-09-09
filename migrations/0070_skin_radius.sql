-- +goose Up
-- Card shape (corner radius) for the 'custom' skin — the built-in skins bake
-- their own shape, but a custom skin lets the vendor choose it alongside the
-- colour. Keyword: sharp | rounded | round.
ALTER TABLE settings ADD COLUMN skin_radius TEXT NOT NULL DEFAULT 'rounded';

-- +goose Down
ALTER TABLE settings DROP COLUMN skin_radius;
