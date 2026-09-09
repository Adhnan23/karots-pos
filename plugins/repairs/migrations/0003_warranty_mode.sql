-- +goose Up
-- Two kinds of repair shop: "days" gives one warranty for the whole repair (the
-- default), "parts" is the PC-shop model where each part carries its own warranty
-- and is covered as long as that part is still in warranty. warranty_mode is the
-- shop-wide toggle; repair_parts.warranty_days holds a part's own warranty period
-- (used only in parts mode; 0 = no warranty on that part).
ALTER TABLE repair_config ADD COLUMN warranty_mode TEXT    NOT NULL DEFAULT 'days';
ALTER TABLE repair_parts  ADD COLUMN warranty_days  INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE repair_parts  DROP COLUMN warranty_days;
ALTER TABLE repair_config DROP COLUMN warranty_mode;
