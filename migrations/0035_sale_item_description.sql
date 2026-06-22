-- +goose Up
-- Optional per-line description for service lines (e.g. a document job
-- "A4 colour 2-side x20" or a recharge "Dialog Rs500"). When set, receipts and
-- sale history show it instead of the bare product name. NULL for normal lines.
ALTER TABLE sale_items ADD COLUMN description TEXT;

-- +goose Down
ALTER TABLE sale_items DROP COLUMN description;
