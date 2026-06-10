-- +goose Up

-- allow_decimal: whether quantities in this unit may be fractional (sold by
-- weight/volume, e.g. 2.25 kg) or must be whole (pieces, packets, bottles).
ALTER TABLE units ADD COLUMN allow_decimal BOOLEAN NOT NULL DEFAULT false;

-- Weight/volume units are fractional by nature.
UPDATE units SET allow_decimal = true WHERE abbreviation IN ('kg', 'g', 'ltr', 'ml');

-- +goose Down
ALTER TABLE units DROP COLUMN allow_decimal;
