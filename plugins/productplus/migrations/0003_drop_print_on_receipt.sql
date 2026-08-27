-- +goose Up
-- Custom fields are not printed on sales receipts after all — they belong on the
-- barcode/shelf label instead. Drop the unused toggle.
ALTER TABLE pp_fields DROP COLUMN print_on_receipt;

-- +goose Down
ALTER TABLE pp_fields ADD COLUMN print_on_receipt BOOLEAN NOT NULL DEFAULT false;
