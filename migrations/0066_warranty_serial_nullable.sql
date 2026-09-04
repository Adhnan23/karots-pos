-- +goose Up
-- Warranty units can now exist without a serial number: non-serial products
-- (lights, etc.) sold with a warranty are claimed by RECEIPT number, and an
-- on-demand unit (serial NULL) is created only when a replacement is actually
-- recorded — never at sale time. Serial-tracked items are unchanged.
ALTER TABLE warranty_units ALTER COLUMN serial_no DROP NOT NULL;

-- +goose Down
-- Non-serial (NULL) units must go before the column can be NOT NULL again.
DELETE FROM warranty_units WHERE serial_no IS NULL;
ALTER TABLE warranty_units ALTER COLUMN serial_no SET NOT NULL;
