-- +goose Up
-- Per-field flag: also show this field on the core Stock Intake "New item" form,
-- so fields you set while quick-adding products appear on that fast path too.
ALTER TABLE pp_fields ADD COLUMN show_in_intake BOOLEAN NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE pp_fields DROP COLUMN show_in_intake;
