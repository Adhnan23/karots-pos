-- +goose Up
-- Same product, same barcode, two lots priced differently: a customer brings an
-- old bottle stickered Rs 100, we scan it, and the receipt prints Rs 120 because
-- products.selling_price is a single value. Batches already carry their own cost
-- and expiry; this gives them their own selling price too.
--
-- selling_price = 0 is the sentinel for "follow the product's current price".
-- There is deliberately NO backfill: every existing batch keeps resolving to
-- today's price, so on day one every product still has exactly one live price,
-- the till never prompts, and no quantity is touched (nothing to recount).
ALTER TABLE stock_batches ADD COLUMN selling_price DECIMAL(12,2) NOT NULL DEFAULT 0.00;

-- Which lot a sale line was rung from. NULL for every line that took the normal
-- FEFO path (which may span several batches); set only when the cashier picked a
-- specific batch at the till, so "why was this price used" has an answer.
ALTER TABLE sale_items ADD COLUMN batch_id BIGINT REFERENCES stock_batches (id) ON DELETE SET NULL;
CREATE INDEX idx_sale_items_batch ON sale_items (batch_id) WHERE batch_id IS NOT NULL;

-- +goose Down
DROP INDEX idx_sale_items_batch;
ALTER TABLE sale_items    DROP COLUMN batch_id;
ALTER TABLE stock_batches DROP COLUMN selling_price;
