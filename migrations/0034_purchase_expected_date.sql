-- +goose Up
-- Expected delivery date on a purchase order (when you expect the goods), separate
-- from due_date (when payment is due). Optional; shown on the printed PO.
ALTER TABLE purchases ADD COLUMN expected_date DATE;

-- +goose Down
ALTER TABLE purchases DROP COLUMN expected_date;
