-- +goose Up
-- v2: longer/typed inputs (textarea, date) and per-field visibility — where a
-- field shows up beyond the product form: the till info popup, the shelf label,
-- the customer receipt.
ALTER TABLE pp_fields DROP CONSTRAINT IF EXISTS pp_fields_type_check;
ALTER TABLE pp_fields ADD CONSTRAINT pp_fields_type_check
    CHECK (type IN ('text','textarea','number','bool','select','date'));

ALTER TABLE pp_fields ADD COLUMN hint             TEXT    NOT NULL DEFAULT '';
ALTER TABLE pp_fields ADD COLUMN show_at_till     BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE pp_fields ADD COLUMN print_on_label   BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE pp_fields ADD COLUMN print_on_receipt BOOLEAN NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE pp_fields DROP COLUMN print_on_receipt;
ALTER TABLE pp_fields DROP COLUMN print_on_label;
ALTER TABLE pp_fields DROP COLUMN show_at_till;
ALTER TABLE pp_fields DROP COLUMN hint;
ALTER TABLE pp_fields DROP CONSTRAINT IF EXISTS pp_fields_type_check;
ALTER TABLE pp_fields ADD CONSTRAINT pp_fields_type_check
    CHECK (type IN ('text','number','bool','select'));
