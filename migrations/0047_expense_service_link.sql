-- +goose Up
-- A toner change or a machine repair is a real cost of running the photocopy
-- counter, but it is not consumed per copy — so it must not enter COGS, where
-- it would distort every line's margin. Tagging the expense to the service it
-- belongs to lets that service's report subtract it without touching the core
-- P&L, which keeps counting it once, as an operating expense.
ALTER TABLE expenses ADD COLUMN service_product_id BIGINT
	REFERENCES products(id) ON DELETE SET NULL;

CREATE INDEX idx_expenses_service_product ON expenses(service_product_id)
	WHERE service_product_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_expenses_service_product;
ALTER TABLE expenses DROP COLUMN service_product_id;
