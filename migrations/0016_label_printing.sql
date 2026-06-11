-- +goose Up

-- Per-shop printer mapping and default label dimensions.
-- receipt_printer / label_printer hold the CUPS queue name selected in Settings
-- (empty = fall back to the RECEIPT_PRINTER / LABEL_PRINTER env var, or the
-- system default queue). The label_*_mm columns are the default sticker size,
-- overridable per print on the Barcode Labels page.
ALTER TABLE settings ADD COLUMN receipt_printer  VARCHAR(100) NOT NULL DEFAULT '';
ALTER TABLE settings ADD COLUMN label_printer    VARCHAR(100) NOT NULL DEFAULT '';
ALTER TABLE settings ADD COLUMN label_width_mm   INT NOT NULL DEFAULT 50;
ALTER TABLE settings ADD COLUMN label_height_mm  INT NOT NULL DEFAULT 25;
ALTER TABLE settings ADD COLUMN label_gap_mm     INT NOT NULL DEFAULT 2;

-- +goose Down
ALTER TABLE settings DROP COLUMN label_gap_mm;
ALTER TABLE settings DROP COLUMN label_height_mm;
ALTER TABLE settings DROP COLUMN label_width_mm;
ALTER TABLE settings DROP COLUMN label_printer;
ALTER TABLE settings DROP COLUMN receipt_printer;
