-- +goose Up
-- free_qty records bonus units a supplier threw in for no extra charge. It never
-- touches the invoice total or supplier balance — only how the received lot is
-- valued: what was paid gets spread across paid + free units, so the effective
-- per-unit cost drops and the freebie shows up as real margin when sold.
ALTER TABLE purchase_items ADD COLUMN free_qty NUMERIC NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE purchase_items DROP COLUMN free_qty;
