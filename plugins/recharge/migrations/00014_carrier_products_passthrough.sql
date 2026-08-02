-- +goose Up
-- Existing carrier service products predate the core pass_through flag. Mark them
-- so the core P&L stops counting resold airtime face value as revenue/profit; the
-- shop's real earning (service charge + float commission) is reported separately.
UPDATE products SET pass_through = true
WHERE id IN (SELECT product_id FROM recharge_carriers WHERE product_id IS NOT NULL);

-- +goose Down
UPDATE products SET pass_through = false
WHERE id IN (SELECT product_id FROM recharge_carriers WHERE product_id IS NOT NULL);
