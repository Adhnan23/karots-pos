-- +goose Up
-- Purchase Orders: a purchase now starts life as a draft (status='draft', the enum
-- default from 0003) and is *received* later. ordered_qty captures what was ordered
-- on the draft so the receive screen can show ordered vs actually-received (overstock:
-- ordered 20, got 25). It stays NULL for legacy/instant GRNs. Only `quantity` (the
-- received amount) ever affects inventory.
ALTER TABLE purchase_items ADD COLUMN ordered_qty DECIMAL(12,3);

-- +goose Down
ALTER TABLE purchase_items DROP COLUMN ordered_qty;
