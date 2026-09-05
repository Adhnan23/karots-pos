-- +goose Up
-- Expiry-driven clearance: also flag products with a live lot expiring within
-- this many days, so they can be marked down and sold before they have to be
-- written off. Existing installs default to 14 days.
ALTER TABLE clearance_settings ADD COLUMN expiry_days INTEGER NOT NULL DEFAULT 14;

-- +goose Down
ALTER TABLE clearance_settings DROP COLUMN expiry_days;
