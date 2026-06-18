-- +goose Up
-- is_service marks a product as a non-stocked "service" line (e.g. a mobile-recharge
-- top-up). Service products are sold like any product but carry no inventory: they are
-- hidden from the catalogue/search and from stock reports, and the till may pass a
-- per-line price override. This is a generic core seam used by plugins; core itself
-- never sets it true. Additive only — safe to apply to a live database.
ALTER TABLE products ADD COLUMN is_service BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down
ALTER TABLE products DROP COLUMN is_service;
